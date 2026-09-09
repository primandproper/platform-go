package shredding

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/cryptography/shredding/internal/shreddingdb"
	"github.com/primandproper/platform-go/v14/cryptography/shredding/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// DefaultTablePrefix is the namespace the keys table carries when none is
// configured, which is none — rendering shredding_subject_keys.
//
// The shredding_ segment is the schema's, not the caller's: a table always says
// which package created it. A namespace must not end in '_'; database/ddl
// supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger, distinctly from the Keys that
// sits above it — a trace wants the KMS round trip and the row read
// distinguishable, and one scope for both makes every unwrap look like a query.
const storeName = serviceName + "_store"

// maxShredAttempts bounds the retry in Shred.
//
// The loop converges in two passes: the only way an attempt fails is a mint
// landing between the update and the tombstone insert, and once a tombstone
// exists no further mint can win. The third attempt is slack, not a design
// margin.
const maxShredAttempts = 3

// ErrShredContended indicates a shred that lost its race repeatedly. It is not
// reachable without something minting keys for a subject in a tight loop while
// that subject is being erased.
var ErrShredContended = platformerrors.New("shredding key row changed under the shred repeatedly")

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema
// cryptography/shredding/migrations renders. It is exported, and returned by
// NewSQLStore, so a caller who has chosen SQL storage can depend on that choice
// rather than on the Store seam every backing shares.
type SQLStore struct {
	client database.Client
	q      shreddingdb.Querier
	o11y   observability.Observer

	mintConflictCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	// prefix is the namespace the table name carries. It is kept because the
	// generated querier was built from it and the migrations are validated
	// against it, not because anything here renders a name from it any more.
	prefix string
}

// NewSQLStore builds a Store over the given database.
//
// Which database that is, is the decision this package's guarantee rests on.
// Pointing it at the same one the protected data lives in is supported and is
// usually wrong: a restore of that database's snapshot brings back wrapped keys
// that were shredded since, and with them everything those keys opened. See the
// package documentation.
//
// The dialect comes from the client, so the two cannot disagree. The prefix must
// still match the one the migrations were rendered with — nothing here can check
// that, and a mismatch surfaces as a missing table on the first query rather
// than at construction.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "shredding dialect %q", d)
	}

	s := &SQLStore{
		client: client,
		prefix: DefaultTablePrefix,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.prefix); err != nil {
		return nil, err
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see cryptography/shredding/internal/queries.
	qd, err := shreddingdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := shreddingdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the shredding querier")
	}

	s.q = q

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One counter, and it is the one nothing above this layer can see: two
	// replicas minting a key for the same subject at the same time. The loser
	// throws its key away and reads the winner's, which is correct and silent —
	// but a rate that is not near zero means something is encrypting for a
	// subject far more often than it is reading one back, which is worth
	// knowing before the KMS bill says so.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	if s.mintConflictCounter, err = mp.NewInt64Counter(storeName + "_mint_conflicts"); err != nil {
		return nil, platformerrors.Wrap(err, "creating shredding store mint conflict counter")
	}

	return s, nil
}

// shreddingdbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is
// a construction failure like any other, and it names the dialect, rather than
// panicking or leaning on shreddingdb.New refusing the empty string.
func shreddingdbDialect(d dialect.Dialect) (shreddingdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return shreddingdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return shreddingdb.DialectMySQL, nil
	case dialect.SQLite:
		return shreddingdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "shredding dialect %q", d)
	}
}

func (s *SQLStore) Load(ctx context.Context, subject Subject) (*Record, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(subjectIDKey, subject.ID),
		observability.WithValue(subjectTypeKey, subject.Type),
	)
	defer op.End()

	if err := subject.validate(); err != nil {
		return nil, op.Error(err, "loading shredding key")
	}

	row, err := s.q.GetSubjectKey(ctx, s.client.Reader(), shreddingdb.GetSubjectKeyParams{
		SubjectType: subject.Type,
		SubjectID:   subject.ID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Not an error at this layer. A subject with no row is the normal
			// state of every subject before the first thing is encrypted about
			// them, and the caller decides whether that is a mint or a miss.
			return nil, ErrNoKey
		}

		return nil, op.Error(err, "loading shredding key")
	}

	record := recordFrom(&row)

	op.Set(shreddedAtKey, record.Shredded())

	return record, nil
}

func (s *SQLStore) Insert(ctx context.Context, record *Record) (bool, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if record == nil {
		return false, op.Error(platformerrors.ErrNilInputParameter, "inserting shredding key")
	}

	op.Set(subjectIDKey, record.Subject.ID).Set(subjectTypeKey, record.Subject.Type)

	if err := record.Subject.validate(); err != nil {
		return false, op.Error(err, "inserting shredding key")
	}

	if len(record.Wrapped) == 0 {
		return false, op.Error(ErrKeyMaterialMissing, "inserting shredding key")
	}

	// A mint: key material, and no destruction time. The statement is the one
	// the tombstone below runs, because writing a row for a subject who has
	// none is the same write either way — see internal/queries.
	affected, err := s.q.InsertSubjectKey(ctx, s.client.Writer(), shreddingdb.InsertSubjectKeyParams{
		SubjectType: record.Subject.Type,
		SubjectID:   record.Subject.ID,
		WrappedKey:  record.Wrapped,
		CreatedAt:   record.CreatedAt.UTC(),
		ShreddedAt:  nil,
	})
	if err != nil {
		return false, op.Error(err, "inserting shredding key")
	}

	op.Set(rowsAffectedKey, affected)

	if affected == 0 {
		s.mintConflictCounter.Add(ctx, 1)
	}

	return affected > 0, nil
}

func (s *SQLStore) Shred(ctx context.Context, subject Subject, at time.Time) (Receipt, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(subjectIDKey, subject.ID),
		observability.WithValue(subjectTypeKey, subject.Type),
	)
	defer op.End()

	if err := subject.validate(); err != nil {
		return Receipt{}, op.Error(err, "shredding key")
	}

	for range maxShredAttempts {
		receipt, done, err := s.shredOnce(ctx, subject, at)
		if err != nil {
			return Receipt{}, op.Error(err, "shredding key")
		}

		if done {
			op.Set(destroyedKey, receipt.Destroyed).Set(shreddedAtKey, receipt.ShreddedAt)

			return receipt, nil
		}
	}

	return Receipt{}, op.Error(ErrShredContended, "shredding key")
}

// shredOnce is one pass of the destruction, reporting whether it settled.
//
// An unsettled pass means a key was minted for this subject between the update
// and the tombstone insert. The next pass finds that row and destroys it, and
// no third mint can intervene because the loser of an insert race never retries.
func (s *SQLStore) shredOnce(ctx context.Context, subject Subject, at time.Time) (Receipt, bool, error) {
	destroyedAt := at.UTC()

	// The key material goes and the row stays. Both timestamps are bound to the
	// one instant rather than to two clock reads: this is the only statement
	// that rewrites a key row, so last_updated_at and shredded_at describe one
	// event and there is nothing for them to disagree about.
	//
	// The wrapped key is bound nil rather than written as a literal NULL, which
	// is the one thing the generated statement says less forcefully than the
	// text it replaced. Nothing else calls it, and what makes the destruction
	// once-only is the statement's own guard on shredded_at IS NULL, which no
	// argument can relax.
	affected, err := s.q.ShredSubjectKey(ctx, s.client.Writer(), shreddingdb.ShredSubjectKeyParams{
		WrappedKey:    nil,
		ShreddedAt:    &destroyedAt,
		LastUpdatedAt: &destroyedAt,
		SubjectType:   subject.Type,
		SubjectID:     subject.ID,
	})
	if err != nil {
		return Receipt{}, false, platformerrors.Wrap(err, "destroying key material")
	}

	if affected > 0 {
		return Receipt{Subject: subject, ShreddedAt: destroyedAt, Destroyed: true}, true, nil
	}

	// No live row. Either the subject never had a key, or it has already been
	// shredded; a tombstone that inserts cleanly settles the first case.
	//
	// Shredding somebody nothing was ever encrypted for still writes a row,
	// because the tombstone is what stops a key being minted for them
	// afterwards. Erasure that only works for subjects who happened to have
	// data already is erasure that fails in exactly the case nobody tests.
	affected, err = s.q.InsertSubjectKey(ctx, s.client.Writer(), shreddingdb.InsertSubjectKeyParams{
		SubjectType: subject.Type,
		SubjectID:   subject.ID,
		WrappedKey:  nil,
		CreatedAt:   destroyedAt,
		ShreddedAt:  &destroyedAt,
	})
	if err != nil {
		return Receipt{}, false, platformerrors.Wrap(err, "writing shredding tombstone")
	}

	if affected > 0 {
		return Receipt{Subject: subject, ShreddedAt: destroyedAt, Destroyed: false}, true, nil
	}

	record, err := s.Load(ctx, subject)
	if err != nil {
		if errors.Is(err, ErrNoKey) {
			// The row was there a moment ago and is not now, which only happens
			// if something deleted it outside this package. Go round again.
			return Receipt{}, false, nil
		}

		return Receipt{}, false, err
	}

	if !record.Shredded() {
		return Receipt{}, false, nil
	}

	// Somebody else destroyed it. Theirs is the timestamp that goes in the
	// record, because theirs is the moment the ciphertext became noise.
	return Receipt{Subject: subject, ShreddedAt: *record.ShreddedAt, Destroyed: false}, true, nil
}

// recordFrom converts the generated row into this package's Record.
//
// It is a struct literal on purpose, and it is the whole of what this package
// does with the generated types. A renamed or retyped column changes the
// generated struct and this function stops compiling; the scan-by-position
// pairing it replaced reported the same mistake as a runtime scan error, or
// worse, as two same-typed columns silently transposed.
//
// The two timestamps are normalized to UTC because every one this package
// writes is UTC and so every one it hands back should be: Postgres returns a
// time in the session's zone, MySQL in the server's, and SQLite whatever the
// stored text parsed as, so a caller comparing two of those, or rendering one
// into JSON, would get an answer that depends on where the row was read.
func recordFrom(row *shreddingdb.GetSubjectKeyRow) *Record {
	record := &Record{
		CreatedAt: row.CreatedAt.UTC(),
		Subject:   Subject{Type: row.SubjectType, ID: row.SubjectID},
		Wrapped:   row.WrappedKey,
	}

	if row.ShreddedAt != nil {
		shreddedAt := row.ShreddedAt.UTC()
		record.ShreddedAt = &shreddedAt
	}

	return record
}
