package passkeys

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passkeys/internal/passkeysdb"
	"github.com/primandproper/platform-go/v14/authentication/passkeys/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

const (
	// serviceName scopes this package's spans, logger, and instruments.
	serviceName = "passkeys"

	scopeKey = serviceName + ".scope"
	// rowIDKey is the row's identifier and credentialIDKey is the
	// authenticator's. They are two facts and the span records them under two
	// names, because a debugging session that conflated them would be reading
	// one passkey's history as another's.
	rowIDKey        = serviceName + ".credential_row_id"
	credentialIDKey = serviceName + ".credential_id"
	userKey         = serviceName + ".user_id"
	signCountKey    = serviceName + ".sign_count"
	countKey        = serviceName + ".count"
)

// DefaultTablePrefix is the namespace the credential table carries when none is
// configured, which is none — rendering webauthn_credentials.
//
// The webauthn_ segment is the schema's, not the caller's: a table always says
// which specification decided its shape. Setting a namespace of "ddb" renders
// ddb_webauthn_credentials, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans, logger, and instruments.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema
// authentication/passkeys/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
type SQLStore struct {
	q           passkeysdb.Querier
	o11y        observability.Observer
	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer and the instruments
	// are built from it. Read s.o11y.Logger() for the logger this store actually
	// uses; this one may be nil, because supplying none is how a caller asks for
	// no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds a Store over the given database.
//
// The client is read at construction and not kept. It supplies the dialect, so
// the store and the database cannot disagree about which SQL to emit, and that
// is the whole of what it is for: every statement this store runs goes on the
// executor its caller hands over, so there is no connection of its own to hold.
// The prefix must still match the one the migrations were rendered with —
// nothing here can check that, and a mismatch surfaces as a missing table on the
// first query rather than at construction.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider, and records to noop instruments.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "passkey store dialect %q", d)
	}

	s := &SQLStore{prefix: DefaultTablePrefix}

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
	// substitution; see authentication/passkeys/internal/passkeysdb.
	qd, err := passkeysdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := passkeysdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the passkey querier")
	}

	s.q = q

	if s.instruments, err = metrics.NewOperationSet(s.metricsProvider, storeName); err != nil {
		return nil, err
	}

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	return s, nil
}

// passkeysdbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect.
func passkeysdbDialect(d dialect.Dialect) (passkeysdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return passkeysdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return passkeysdb.DialectMySQL, nil
	case dialect.SQLite:
		return passkeysdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated passkey queries for dialect %q", d)
	}
}

// failed counts a failed operation and hands the error back unchanged.
//
// It takes the already-recorded error rather than the description, so that the
// description stays a literal at the call site: a helper that formatted it would
// be a printf wrapper whose format string is never constant.
//
// Errors is a subset of Requests rather than a series beside it, so this counts
// only the failure; the attempt was counted when the operation began.
func (s *SQLStore) failed(ctx context.Context, err error) error {
	s.instruments.Failed(ctx)

	return err
}

// CreateCredential stores one registered passkey through the caller's
// transaction and answers with the row it wrote.
//
// The three statements are one unit: the collision check, the insert, and the
// read-back. They run on the caller's transaction, so the row commits with
// whatever the consumer is recording beside it — and the credential handed back
// is the one this transaction wrote, visible here before anybody else can see it.
//
// The read-back is GetCredential rather than a statement of its own. A row this
// transaction just inserted is not archived, so the ordinary keyed read reaches
// it, and reading the whole row costs the same one round trip while answering
// with what the database holds instead of with what the caller assembled plus a
// timestamp. MySQL has no RETURNING, so the second statement is what every
// dialect here does rather than a concession one of them makes.
func (s *SQLStore) CreateCredential(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	credential *Credential,
) (*Credential, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "registering passkey"))
	}

	if err := credential.ValidateWithContext(ctx); err != nil {
		return nil, s.failed(ctx, op.Error(err, "registering passkey"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "registering passkey"))
	}

	if err := checkScope(scope, credential); err != nil {
		return nil, s.failed(ctx, op.Error(err, "registering passkey"))
	}

	id := newID(credential.ID)

	op.Set(rowIDKey, id).
		Set(userKey, credential.BelongsToUser).
		Set(credentialIDKey, encodeCredentialID(credential.CredentialID))

	transports, err := encodeTransports(ctx, credential.Transports)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "registering passkey"))
	}

	if err = s.ensureCredentialFree(ctx, tx, scope, credential.CredentialID); err != nil {
		return nil, s.failed(ctx, op.Error(err, "registering passkey"))
	}

	if err = s.q.CreateCredential(ctx, tx, createCredentialParams(scope, id, transports, credential)); err != nil {
		return nil, s.failed(ctx, op.Error(err, "writing the passkey credential row"))
	}

	row, err := s.q.GetCredential(ctx, tx, passkeysdb.GetCredentialParams{ID: id, Scope: scope})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the registered passkey"))
	}

	written, err := credentialFromRow(ctx, &row)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the registered passkey"))
	}

	return written, nil
}

// ensureCredentialFree is the collision check the create runs before it writes,
// so a credential already enrolled reports ErrCredentialRegistered rather than a
// driver-specific constraint violation the caller would have to parse a SQLSTATE
// out of.
//
// The index is what actually guarantees uniqueness; this read is what turns the
// ordinary case into a sentinel. Two registrations racing for one credential
// still reach the index, and the loser gets the driver's error — which is
// correct, rare, and the reason this check is not presented as the guarantee.
//
// It reads live rows only, because the index does. That is the opposite of
// mediaregistry's equivalent and for the opposite reason: archiving a passkey
// genuinely frees the authenticator, so a check that also refused archived rows
// would refuse a registration the index would have accepted.
func (s *SQLStore) ensureCredentialFree(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	credentialID []byte,
) error {
	_, err := s.q.GetCredentialByCredentialID(ctx, q, passkeysdb.GetCredentialByCredentialIDParams{
		CredentialID: credentialID,
		Scope:        scope,
	})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return platformerrors.Wrap(err, "checking passkey credential uniqueness")
	default:
		return ErrCredentialRegistered
	}
}

// GetCredentialByCredentialID reads the scope's live passkey for one credential
// ID, which is the read a login runs.
func (s *SQLStore) GetCredentialByCredentialID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	credentialID []byte,
) (*Credential, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(credentialIDKey, encodeCredentialID(credentialID)),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "reading passkey credential"))
	}

	if len(credentialID) == 0 {
		return nil, s.failed(ctx, op.Error(ErrEmptyCredentialID, "reading passkey credential"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading passkey credential"))
	}

	row, err := s.q.GetCredentialByCredentialID(ctx, q, passkeysdb.GetCredentialByCredentialIDParams{
		CredentialID: credentialID,
		Scope:        scope,
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(notFound(err, ErrCredentialNotFound), "reading passkey credential"))
	}

	credential, err := credentialFromLookupRow(ctx, &row)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading passkey credential"))
	}

	return credential, nil
}

// GetCredentialsForUser reads every live passkey one user has, oldest first.
func (s *SQLStore) GetCredentialsForUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) ([]*Credential, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userKey, userID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing a user's passkeys"))
	}

	if userID == "" {
		return nil, s.failed(ctx, op.Error(ErrEmptyUserID, "listing a user's passkeys"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing a user's passkeys"))
	}

	rows, err := s.q.ListCredentialsForUser(ctx, q, passkeysdb.ListCredentialsForUserParams{
		Scope:         scope,
		BelongsToUser: userID,
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing a user's passkeys"))
	}

	credentials := make([]*Credential, 0, len(rows))

	for i := range rows {
		credential, convErr := credentialFromListRow(ctx, &rows[i])
		if convErr != nil {
			return nil, s.failed(ctx, op.Error(convErr, "listing a user's passkeys"))
		}

		credentials = append(credentials, credential)
	}

	op.SpanOnly(countKey, len(credentials))

	return credentials, nil
}

// ListAllCredentialsForUser reads every passkey one user has registered in the
// scope, revoked ones included. See Store.ListAllCredentialsForUser for why it
// is a second method rather than an argument on the live read.
//
// The statement behind it is the set-keyed read, and this binds a set of one.
// That is the shape querygen renders a many-row read with a projection of its
// own in — which is what this read needs, since the archived column has to be
// out of the predicate list and in the answer — and nothing here batches; see
// authentication/passkeys/internal/queries.
func (s *SQLStore) ListAllCredentialsForUser(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
) ([]*Credential, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userKey, userID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing every passkey a user has registered"))
	}

	if userID == "" {
		return nil, s.failed(ctx, op.Error(ErrEmptyUserID, "listing every passkey a user has registered"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing every passkey a user has registered"))
	}

	rows, err := s.q.ListCredentialsForUsers(ctx, q, passkeysdb.ListCredentialsForUsersParams{
		Scope: scope,
		Users: []string{userID},
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing every passkey a user has registered"))
	}

	credentials := make([]*Credential, 0, len(rows))

	for i := range rows {
		credential, convErr := credentialFromSubjectListRow(ctx, &rows[i])
		if convErr != nil {
			return nil, s.failed(ctx, op.Error(convErr, "listing every passkey a user has registered"))
		}

		credentials = append(credentials, credential)
	}

	op.SpanOnly(countKey, len(credentials))

	return credentials, nil
}

// RecordUse writes the authenticator's signature counter back and answers with
// the row it left. See Store.RecordUse for why its error is the caller's to
// surface.
//
// The count the statement reports is the answer, and on MySQL it counts rows
// *changed* rather than matched — which is why last_updated_at is in the SET
// list. Every run of this statement moves the stamp, so a write that reaches a
// live row reports one on all three engines.
func (s *SQLStore) RecordUse(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	credentialRowID string,
	signCount uint32,
	at time.Time,
) (*Credential, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(rowIDKey, credentialRowID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	op.SpanOnly(signCountKey, signCount)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "recording passkey use"))
	}

	if credentialRowID == "" {
		return nil, s.failed(ctx, op.Error(ErrCredentialNotFound, "recording passkey use"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "recording passkey use"))
	}

	used := at.UTC()

	count, err := s.q.RecordCredentialUse(ctx, tx, passkeysdb.RecordCredentialUseParams{
		SignCount:  storedSignCount(signCount),
		LastUsedAt: &used,
		ID:         credentialRowID,
		Scope:      scope,
	})
	if err = guardCount(count, err, ErrCredentialNotFound); err != nil {
		return nil, s.failed(ctx, op.Error(err, "recording passkey use"))
	}

	row, err := s.q.GetCredential(ctx, tx, passkeysdb.GetCredentialParams{ID: credentialRowID, Scope: scope})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the recorded passkey use"))
	}

	credential, err := credentialFromRow(ctx, &row)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the recorded passkey use"))
	}

	return credential, nil
}

// ArchiveCredentialForUser revokes one of a user's passkeys and answers with the
// row it hid.
func (s *SQLStore) ArchiveCredentialForUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	credentialRowID, userID string,
) (*Credential, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(rowIDKey, credentialRowID),
		observability.WithValue(userKey, userID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "revoking passkey"))
	}

	if userID == "" {
		return nil, s.failed(ctx, op.Error(ErrEmptyUserID, "revoking passkey"))
	}

	if credentialRowID == "" {
		return nil, s.failed(ctx, op.Error(ErrCredentialNotFound, "revoking passkey"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "revoking passkey"))
	}

	count, err := s.q.ArchiveCredentialForUser(ctx, tx, passkeysdb.ArchiveCredentialForUserParams{
		ID:            credentialRowID,
		Scope:         scope,
		BelongsToUser: userID,
	})
	if err = guardCount(count, err, ErrCredentialNotFound); err != nil {
		return nil, s.failed(ctx, op.Error(err, "revoking passkey"))
	}

	row, err := s.q.GetArchivedCredential(ctx, tx, passkeysdb.GetArchivedCredentialParams{
		ID:    credentialRowID,
		Scope: scope,
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the revoked passkey"))
	}

	credential, err := credentialFromArchivedRow(ctx, &row)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the revoked passkey"))
	}

	return credential, nil
}

// DeleteCredentialsForUser destroys every passkey one user holds in the scope,
// revoked ones included, and answers with how many rows went. See
// Store.DeleteCredentialsForUser for why an erasure deletes where a revocation
// archives.
//
// The count is not guarded. Every other write here reports ErrCredentialNotFound
// for a statement that moved nothing, because every other write names a row
// somebody is holding; this one names a person, and a person with no passkeys is
// the ordinary case an erasure runs into rather than a failure to report.
func (s *SQLStore) DeleteCredentialsForUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userKey, userID),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return 0, s.failed(ctx, op.Error(ErrNilExecutor, "erasing a user's passkeys"))
	}

	if userID == "" {
		return 0, s.failed(ctx, op.Error(ErrEmptyUserID, "erasing a user's passkeys"))
	}

	if err := scope.Validate(); err != nil {
		return 0, s.failed(ctx, op.Error(err, "erasing a user's passkeys"))
	}

	deleted, err := s.q.DeleteCredentialsForUser(ctx, tx, passkeysdb.DeleteCredentialsForUserParams{
		Scope:         scope,
		BelongsToUser: userID,
	})
	if err != nil {
		return 0, s.failed(ctx, op.Error(err, "erasing a user's passkeys"))
	}

	op.Set(countKey, deleted)

	return deleted, nil
}

// checkScope settles which tenant a write is for.
//
// The scope the call named is the one the statement binds, so a credential that
// names a different one is refused rather than corrected: the two disagreeing is
// a caller holding one tenant's passkey and writing it into another, which is a
// stale value or a mix-up and is not a thing to guess at. One that names none
// adopts the argument. tenancy.Scope tells the zero value apart from Global(), so
// "unset" here is genuinely unset rather than the global scope spelled shortly.
//
// Nothing is written back onto the argument. The create takes a pointer the
// caller still holds, and what the write settled is on the value it returns.
func checkScope(scope tenancy.Scope, credential *Credential) error {
	if credential.Scope != (tenancy.Scope{}) && credential.Scope != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"credential names %q, the write names %q", credential.Scope, scope)
	}

	return nil
}

// guardCount reads a guarded write's answer: an error is an error, and a write
// that moved nothing is the missing sentinel rather than a success.
func guardCount(count int64, err, missing error) error {
	if err != nil {
		return err
	}

	if count == 0 {
		return missing
	}

	return nil
}

// notFound maps a driver's empty-result error onto this package's sentinel,
// leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to a person
// as "that passkey is not registered" — which, on the path that decides whether
// somebody may sign in, is the difference between an outage and being locked out
// of an account they still own.
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}

// newID returns the identifier a write should carry: the one the caller set, or
// a fresh one.
//
// A caller-supplied id matters where the row referencing the passkey is written
// in the same transaction and has to name it before this write returns.
func newID(existing string) string {
	if existing != "" {
		return existing
	}

	return identifiers.New()
}

// encodeCredentialID renders a credential ID for a span or a log line.
//
// Base64url without padding is how WebAuthn spells a credential ID on the wire,
// so a line here and a line in a browser's console name the same value the same
// way. It is not a secret — every assertion carries it in the clear — but it is
// an identifier, and recording raw bytes would put unprintable characters into
// whatever reads the span.
func encodeCredentialID(credentialID []byte) string {
	return base64.RawURLEncoding.EncodeToString(credentialID)
}
