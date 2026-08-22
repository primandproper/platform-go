package shredding

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/shredding/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
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
	tables *tables
	o11y   observability.Observer

	mintConflictCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	dialect         dialect.Dialect
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
		client:  client,
		dialect: d,
		tables:  newTables(DefaultTablePrefix),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.tables.prefix()); err != nil {
		return nil, err
	}

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// One counter, and it is the one nothing above this layer can see: two
	// replicas minting a key for the same subject at the same time. The loser
	// throws its key away and reads the winner's, which is correct and silent —
	// but a rate that is not near zero means something is encrypting for a
	// subject far more often than it is reading one back, which is worth
	// knowing before the KMS bill says so.
	mp := metrics.EnsureMetricsProvider(s.metricsProvider)

	var err error
	if s.mintConflictCounter, err = mp.NewInt64Counter(storeName + "_mint_conflicts"); err != nil {
		return nil, platformerrors.Wrap(err, "creating shredding store mint conflict counter")
	}

	return s, nil
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

	query, args := s.tables.buildSelectRecord(s.dialect, subject)

	record, err := scanRecord(s.client.Reader().QueryRowContext(ctx, query, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Not an error at this layer. A subject with no row is the normal
			// state of every subject before the first thing is encrypted about
			// them, and the caller decides whether that is a mint or a miss.
			return nil, ErrNoKey
		}

		return nil, op.Error(err, "loading shredding key")
	}

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

	query, args := s.tables.buildInsertRecord(s.dialect, record)

	affected, err := s.exec(ctx, query, args)
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
	query, args := s.tables.buildShred(s.dialect, subject, at)

	affected, err := s.exec(ctx, query, args)
	if err != nil {
		return Receipt{}, false, platformerrors.Wrap(err, "destroying key material")
	}

	if affected > 0 {
		return Receipt{Subject: subject, ShreddedAt: at.UTC(), Destroyed: true}, true, nil
	}

	// No live row. Either the subject never had a key, or it has already been
	// shredded; a tombstone that inserts cleanly settles the first case.
	query, args = s.tables.buildInsertTombstone(s.dialect, subject, at)

	affected, err = s.exec(ctx, query, args)
	if err != nil {
		return Receipt{}, false, platformerrors.Wrap(err, "writing shredding tombstone")
	}

	if affected > 0 {
		return Receipt{Subject: subject, ShreddedAt: at.UTC(), Destroyed: false}, true, nil
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

// exec runs a write and reports the rows it touched.
func (s *SQLStore) exec(ctx context.Context, query string, args []any) (int64, error) {
	result, err := s.client.Writer().ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, platformerrors.Wrap(err, "reading rows affected")
	}

	return affected, nil
}

// scanRecord reads one row in the order recordColumns declares.
func scanRecord(scanner database.Scanner) (*Record, error) {
	var (
		record     Record
		wrapped    []byte
		shreddedAt sql.NullTime
	)

	if err := scanner.Scan(
		&record.Subject.Type,
		&record.Subject.ID,
		&wrapped,
		&record.CreatedAt,
		&shreddedAt,
	); err != nil {
		return nil, err
	}

	record.Wrapped = wrapped

	if shreddedAt.Valid {
		at := shreddedAt.Time.UTC()
		record.ShreddedAt = &at
	}

	return &record, nil
}
