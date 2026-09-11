package database

import (
	"context"
	"database/sql"
	stderrors "errors"
	"math"
	"time"

	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/links/database/internal/linksdb"
	"github.com/primandproper/platform-go/v14/links/database/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
)

// serviceName names the loggers, spans, and instruments this store emits. The
// counters that describe what an operation meant live on the Minter; the
// sweeper's live here, because nothing above this layer knows a sweep happened.
const serviceName = "links_database"

// The observability keys this store sets that the Minter above it cannot. A
// subject is one of them: the Minter refuses to label a metric with it, for the
// cardinality reason its own documentation gives, but a span is one operation
// rather than a time series, and the whole point of the operation being traced
// is which subject it withdrew links for.
const (
	subjectKey = "links.subject"
	revokedKey = "links.revoked"
)

// DefaultTablePrefix is the namespace the action link table carries when none
// is configured, which is none — rendering plain "action_links".
//
// The action_links segment is the schema's, not the caller's: a table always
// says which package created it. Setting a namespace of "ddb" renders
// ddb_action_links, for a database shared between applications. A namespace
// must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

var _ links.Store = (*Store)(nil)

// Store keeps action link records in a SQL table, against the schema
// links/database/migrations renders.
//
// It is exported, and returned by New, so a caller holding one can depend on
// this type rather than on the links.Store seam. It does one thing more than
// links.Store describes — Sweep removes rows past their purge deadline — and
// that stays off the interface: reclaiming rows is this storage's own
// housekeeping, not something a Minter asks anybody for.
type Store struct {
	db    database.Client
	q     linksdb.Querier
	codec encoding.Codec
	clock clock.Clock
	o11y  observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter
}

// New builds a Store over a database client.
//
// It takes no locker, which is the whole point of it. Single use here is the
// affected row count of an UPDATE guarded on the link not yet having been
// resolved, evaluated by the server inside one transaction — the same
// guarantee a distributed lock would have been run for, decided by a server
// the application already has.
//
// Reads go through the write pool, deliberately. A link is written by whatever
// builds the email and read by the click that follows, and replica lag turns
// that into a link that is "not found" and then works when reloaded — which for
// a single-use credential is a link the user has already been told is broken.
// These rows are small, single-key, and short-lived; they are not the reads
// worth scaling out.
//
// It does not create the table. Hand migrations.SQL to your own migration run.
func New(cfg *Config, db database.Client, opts ...Option) (*Store, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if db == nil {
		return nil, ErrNilClient
	}

	d := db.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "action link store dialect %q", d)
	}

	if err := migrations.ValidatePrefix(cfg.TablePrefix); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	s := &Store{
		db:    db,
		codec: o.codec,
		clock: o.clock,
		o11y:  observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see internal/linksdb.
	qd, err := querierDialect(d)
	if err != nil {
		return nil, err
	}

	// The table's name lives nowhere else in this package: the canonical
	// spelling is internal/queries' and the separator is database/ddl's, so a
	// namespaced deployment cannot end up with two renderings of one name.
	if s.q, err = linksdb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the action link querier")
	}

	if s.sweptCounter, s.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go s.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return s, nil
}

// querierDialect maps this module's dialect names onto the generated package's.
// The set is closed on both sides — New has already rejected anything d.Valid()
// declines — so the default arm is reachable only when this module learns a
// dialect the generated package was not generated for. That is a construction
// failure like any other, and it names the dialect.
func querierDialect(d dialect.Dialect) (linksdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return linksdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return linksdb.DialectMySQL, nil
	case dialect.SQLite:
		return linksdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated action link queries for dialect %q", d)
	}
}

// Put writes a freshly minted link's row.
//
// It is a plain INSERT, so an id already in the table is a failed write rather
// than a replaced row. The id is the digest of the token, so a collision means
// the generator produced the same token twice, and handing the second caller a
// URL that redeems the first caller's link is the one outcome worse than a
// failed mint.
func (s *Store) Put(ctx context.Context, id links.ID, record *links.Record) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	metadata, err := s.encode(ctx, record)
	if err != nil {
		return op.Error(err, "encoding action link metadata")
	}

	if err = s.q.InsertLink(ctx, s.db.Writer(), linksdb.InsertLinkParams{
		ID:         string(id),
		Action:     string(record.Action),
		Subject:    string(record.Subject),
		Metadata:   metadata,
		State:      int64(record.State),
		Version:    int64(record.Version),
		CreatedAt:  record.CreatedAt.UTC(),
		ExpiresAt:  record.ExpiresAt.UTC(),
		ResolvedAt: nil,
		PurgeAfter: record.PurgeAfter.UTC(),
	}); err != nil {
		return op.Error(err, "storing action link row")
	}

	return nil
}

// Get reads a record without consuming it.
func (s *Store) Get(ctx context.Context, id links.ID) (*links.Record, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	record, err := s.read(ctx, s.db.Writer(), id)
	if err != nil {
		if isStoreAnswer(err) {
			return nil, err
		}

		return nil, op.Error(err, "reading action link row")
	}

	return record, nil
}

// Resolve moves an active link into a terminal state inside one transaction.
//
// This is the operation the database store exists for. The read and the write
// that spends the link are one transaction, and it is the write that decides:
// two requests answering one token at the same instant both read the row
// active, the first one's UPDATE matches, and the second one's finds
// resolved_at already set and reports no rows. A read that decided, with an
// update afterwards, would hand the link to both.
//
// The count discriminates on every engine. MySQL reports rows *changed* rather
// than matched, which is what makes a zero count ambiguous for a statement that
// might write the values a row already held — but this one always moves
// resolved_at from NULL to an instant, so a row it matched is a row it changed.
//
// A link that answers for itself leaves the transaction through its error
// return, which rolls back. There is nothing to preserve: every one of those
// answers is reached before the UPDATE, or by a re-read after an UPDATE that
// wrote nothing.
func (s *Store) Resolve(
	ctx context.Context,
	id links.ID,
	to links.State,
	at, purgeAfter time.Time,
) (*links.Record, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	// Assigned inside the transaction and read after it, because a refusal has
	// to carry the record too: the action it names is what keeps a metric
	// labeled by action from going blank exactly when one flow's links start
	// failing.
	var found *links.Record

	if err := s.db.WithTransaction(ctx, func(q database.Tx) error {
		record, txErr := s.read(ctx, q, id)
		if txErr != nil {
			return txErr
		}

		found = record

		// Checked before the write rather than folded into its predicate. A
		// guard comparing expires_at against the server's clock would make the
		// boundary a different comparison on each of the three engines, would
		// put a second clock opposite the one that stamped the column, and
		// would collapse "expired" and "already resolved" into one affected-row
		// count of zero.
		if txErr = record.Usable(at); txErr != nil {
			return txErr
		}

		resolvedAt := at.UTC()

		affected, txErr := s.q.ResolveLink(ctx, q, linksdb.ResolveLinkParams{
			State:      int64(to),
			ResolvedAt: &resolvedAt,
			PurgeAfter: purgeAfter.UTC(),
			ID:         string(id),
		})
		if txErr != nil {
			return platformerrors.Wrap(txErr, "resolving action link row")
		}

		if affected == 0 {
			// Somebody else resolved the link between the read and the write.
			// The row is read again rather than answered from the count,
			// because the count says only that this caller lost — and "already
			// redeemed" and "revoked" are two different sentences for whoever
			// is holding the token.
			var loser *links.Record

			if loser, txErr = s.read(ctx, q, id); txErr != nil {
				return txErr
			}

			found = loser

			// Judged against the winner's own stamp rather than against the
			// clock this call was handed. A link somebody spent a moment ago,
			// whose deadline has since gone by, is "already redeemed" rather
			// than "expired" — the more useful of the two true sentences, and
			// the one a support conversation turns on.
			//
			// A row whose resolved_at is set cannot read as usable, so the
			// fallback is unreachable rather than merely unlikely. It is here
			// because the alternative to a wrong sentence is no answer at all,
			// and a resolution that cannot say what happened has to fail closed
			// rather than report success.
			if txErr = loser.Usable(loser.ResolvedAt); txErr != nil {
				return txErr
			}

			return links.ErrLinkAlreadyRedeemed
		}

		resolved := *record
		resolved.State = to
		resolved.ResolvedAt = resolvedAt
		resolved.PurgeAfter = purgeAfter

		found = &resolved

		return nil
	}); err != nil {
		if isStoreAnswer(err) {
			return found, err
		}

		return nil, op.Error(err, "resolving action link")
	}

	return found, nil
}

// RevokeForSubject moves every unresolved link for a subject into
// StateRevoked, and reports how many it moved.
//
// subject is a column here, so "withdraw everything this person still has
// outstanding" is one statement rather than a walk of the application's audit
// log with a Revoke per result. That is what makes it a links.Store method
// rather than an optional capability — see the interface, where the column is
// the requirement.
//
// There is no transaction, because there is nothing to make atomic. One
// statement is already one transaction on all three engines, and the guard that
// makes the resolution single-use — resolved_at IS NULL — is the same guard
// here, evaluated per row by the server. A redemption of one of these links
// racing this write is therefore decided the way two redemptions are: whichever
// reaches the row first moves it, the other finds resolved_at set, and this
// call's count excludes the row it lost.
//
// The count discriminates on every engine for the reason Resolve's does. MySQL
// reports rows changed rather than matched, which makes a zero ambiguous for a
// statement that might write values a row already held; this one always moves
// resolved_at from NULL to an instant, so a row it matched is a row it changed.
//
// It takes no scope, and the rows it moves may belong to any number of tenants.
// Revoking a person's links should cross whatever tenants that person belongs
// to rather than stop inside one — see links/database/migrations for why this
// table has no scope column at all.
func (s *Store) RevokeForSubject(
	ctx context.Context,
	subject links.Subject,
	at, purgeAfter time.Time,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if subject == "" {
		return 0, op.Error(links.ErrEmptySubject, "checking action link subject")
	}

	op.Set(subjectKey, string(subject))

	resolvedAt := at.UTC()

	revoked, err := s.q.RevokeSubjectLinks(ctx, s.db.Writer(), linksdb.RevokeSubjectLinksParams{
		State:      int64(links.StateRevoked),
		ResolvedAt: &resolvedAt,
		PurgeAfter: purgeAfter.UTC(),
		Subject:    string(subject),
	})
	if err != nil {
		return 0, op.Error(err, "revoking action link rows for subject")
	}

	op.Set(revokedKey, revoked)

	return revoked, nil
}

// read resolves an ID to the row stored under it, reporting ErrLinkNotFound
// when there is none and ErrStaleRecord when what is there was written by a
// different shape of the links package.
func (s *Store) read(
	ctx context.Context,
	q database.SQLQueryExecutor,
	id links.ID,
) (*links.Record, error) {
	row, err := s.q.GetLink(ctx, q, linksdb.GetLinkParams{ID: string(id)})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return nil, links.ErrLinkNotFound
		}

		return nil, platformerrors.Wrap(err, "reading action link row")
	}

	record := &links.Record{
		// Read back as UTC unconditionally: Postgres hands back a time in the
		// session's zone, and every deadline is decided by comparing these
		// against a UTC instant the Minter supplied.
		CreatedAt:  row.CreatedAt.UTC(),
		ExpiresAt:  row.ExpiresAt.UTC(),
		PurgeAfter: row.PurgeAfter.UTC(),
		Action:     links.Action(row.Action),
		Subject:    links.Subject(row.Subject),
		Version:    int(row.Version),
		State:      stateFrom(row.State),
	}

	if row.ResolvedAt != nil {
		record.ResolvedAt = row.ResolvedAt.UTC()
	}

	if !record.Current() {
		return nil, platformerrors.Wrapf(links.ErrStaleRecord, "record version %d", record.Version)
	}

	if err = s.decode(ctx, row.Metadata, record); err != nil {
		return nil, err
	}

	return record, nil
}

// encode renders a record's metadata as the bytes the column holds, or nil for
// a link that carries none — which is most of them, and which is why the column
// is nullable rather than holding an encoded empty map.
func (s *Store) encode(ctx context.Context, record *links.Record) ([]byte, error) {
	if len(record.Metadata) == 0 {
		return nil, nil
	}

	metadata, err := s.codec.Marshal(ctx, record.Metadata)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding action link metadata")
	}

	return metadata, nil
}

// decode reads the metadata column back onto a record.
//
// A blob this store cannot decode fails the read rather than yielding a record
// with no metadata. Metadata is what a handler acts on — the page to land on,
// the invited role — so silently dropping it would redeem the link and then do
// the wrong thing with it, where refusing costs a remint.
func (s *Store) decode(ctx context.Context, metadata []byte, record *links.Record) error {
	if len(metadata) == 0 {
		return nil
	}

	var values map[string]string
	if err := s.codec.Unmarshal(ctx, metadata, &values); err != nil {
		return platformerrors.Wrap(err, "decoding action link metadata")
	}

	record.Metadata = values

	return nil
}

// stateFrom narrows the column's integer onto links.State.
//
// A value outside the range is mapped to the zero State rather than wrapped
// around, which links.Record.Usable refuses as a state it does not know. That
// is the direction that fails closed: silently truncating 256 to 0 would be one
// thing, and silently truncating it to a value that happens to spell "active"
// would be a link this binary honors on somebody else's say-so.
func stateFrom(value int64) links.State {
	if value <= 0 || value > math.MaxUint8 {
		return 0
	}

	return links.State(value)
}

// isStoreAnswer reports whether err is the store answering about a link rather
// than failing to answer. The two travel out of a transaction together and are
// separated here, because only the second is an operator's problem.
func isStoreAnswer(err error) bool {
	return stderrors.Is(err, links.ErrLinkNotFound) ||
		stderrors.Is(err, links.ErrStaleRecord) ||
		stderrors.Is(err, links.ErrLinkAlreadyRedeemed) ||
		stderrors.Is(err, links.ErrLinkExpired) ||
		stderrors.Is(err, links.ErrLinkRevoked)
}
