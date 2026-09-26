package grants

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/authentication/grants/internal/grantsdb"
	"github.com/primandproper/platform-go/v14/authentication/grants/internal/queries"
	"github.com/primandproper/platform-go/v14/authentication/grants/migrations"

	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
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
	serviceName = "grants"

	scopeKey    = serviceName + ".scope"
	idKey       = serviceName + ".id"
	subjectKey  = serviceName + ".subject"
	providerKey = serviceName + ".provider"
	reasonKey   = serviceName + ".revocation_reason"
	countKey    = serviceName + ".count"
)

// DefaultTablePrefix is the namespace the grant table carries when none is
// configured, which is none — rendering oauth2_grants. A namespace must not end
// in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans, logger, and instruments.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema
// authentication/grants/migrations renders.
type SQLStore struct {
	q           grantsdb.Querier
	encryptor   encryption.EncryptorDecryptor
	o11y        observability.Observer
	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer and the instruments
	// are built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	prefix          string
}

// NewSQLStore builds a Store over the given database, sealing every token with
// encryptor.
//
// The client is read at construction and not kept: it supplies the dialect, and
// every statement runs on the executor a caller hands over. The encryptor is
// kept, and required — a primitives-go encryption.Keyring in ordinary wiring,
// whose rotation is what lets a deployment change keys without re-encrypting
// the table on the day it does.
//
// Observability is optional and defaults to nothing.
func NewSQLStore(
	client database.Client,
	encryptor encryption.EncryptorDecryptor,
	opts ...SQLStoreOption,
) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if encryptor == nil {
		return nil, ErrNilEncryptor
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "grant store dialect %q", d)
	}

	s := &SQLStore{prefix: DefaultTablePrefix, encryptor: encryptor}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if err := migrations.ValidatePrefix(s.prefix); err != nil {
		return nil, err
	}

	qd, err := grantsdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := grantsdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the grant querier")
	}

	s.q = q

	if s.instruments, err = metrics.NewOperationSet(s.metricsProvider, storeName); err != nil {
		return nil, err
	}

	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	return s, nil
}

// grantsdbDialect maps this module's dialect names onto the generated package's.
func grantsdbDialect(d dialect.Dialect) (grantsdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return grantsdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return grantsdb.DialectMySQL, nil
	case dialect.SQLite:
		return grantsdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated grant queries for dialect %q", d)
	}
}

// failed counts a failed operation and hands the error back unchanged.
func (s *SQLStore) failed(ctx context.Context, err error) error {
	s.instruments.Failed(ctx)

	return err
}

// Put stores the grant a consent produced, replacing whatever the subject held
// at that provider.
//
// Two statements on the caller's transaction: an upsert onto the subject's key,
// and the read-back. The upsert takes a revoked row as readily as a live one,
// so a subject who disconnects and reconnects has one grant, not a history of
// them. Two first-time consents racing for one key converge on the later one
// rather than failing: the second writer waits on the first's row and then
// replaces it. A delete of the key followed by an insert would fail that race
// on Postgres and deadlock it on MySQL. See the put statement in
// internal/queries.
func (s *SQLStore) Put(ctx context.Context, tx database.Tx, scope tenancy.Scope, consent *Consent) (*Grant, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "storing grant"))
	}

	if err := consent.validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing grant"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing grant"))
	}

	op.Set(subjectKey, consent.Subject).Set(providerKey, consent.Provider)

	params, err := s.putParams(ctx, scope, consent)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing grant"))
	}

	op.Set(idKey, params.ID)

	if err = s.q.PutGrant(ctx, tx, params); err != nil {
		return nil, s.failed(ctx, op.Error(err, "writing the grant row"))
	}

	grant, err := s.readLive(ctx, tx, scope, params.ID)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the stored grant"))
	}

	return grant, nil
}

// putParams seals a validated consent's tokens and renders it as the upsert's
// arguments, under a freshly minted id and with no revocation reason, which is
// what clears a revoked row the consent replaces.
func (s *SQLStore) putParams(ctx context.Context, scope tenancy.Scope, c *Consent) (grantsdb.PutGrantParams, error) {
	scopes, err := encodeScopes(c.GrantedScopes)
	if err != nil {
		return grantsdb.PutGrantParams{}, err
	}

	access, err := s.seal(ctx, scope, c.Subject, c.Provider, queries.AccessTokenColumn, c.Tokens.AccessToken)
	if err != nil {
		return grantsdb.PutGrantParams{}, err
	}

	refresh, err := s.seal(ctx, scope, c.Subject, c.Provider, queries.RefreshTokenColumn, c.Tokens.RefreshToken)
	if err != nil {
		return grantsdb.PutGrantParams{}, err
	}

	return grantsdb.PutGrantParams{
		ID:                   identifiers.New(),
		Scope:                scope,
		Subject:              c.Subject,
		Provider:             c.Provider,
		ProviderAccountID:    c.ProviderAccountID,
		GrantedScopes:        scopes,
		AccessToken:          access,
		AccessTokenExpiresAt: expiryPtr(c.Tokens.Expiry),
		RefreshToken:         refresh,
		RevocationReason:     "",
	}, nil
}

// Get reads the live grant one subject gave one provider.
func (s *SQLStore) Get(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject, provider string,
) (*Grant, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, subject),
		observability.WithValue(providerKey, provider),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "reading grant"))
	}

	if subject == "" {
		return nil, s.failed(ctx, op.Error(ErrEmptySubject, "reading grant"))
	}

	if provider == "" {
		return nil, s.failed(ctx, op.Error(ErrEmptyProvider, "reading grant"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading grant"))
	}

	row, err := s.q.GetGrantForProvider(ctx, q, grantsdb.GetGrantForProviderParams{
		Scope:    scope,
		Subject:  subject,
		Provider: provider,
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(notFound(err), "reading grant"))
	}

	converted := grantsdb.GetGrantRow(row)

	grant, err := s.openRow(ctx, &converted)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading grant"))
	}

	op.Set(idKey, grant.ID)

	return grant, nil
}

// Refreshed stores a refresh's tokens, provided the grant still holds the access
// token the caller refreshed from. See Store.Refreshed.
//
// The comparison happens twice, and the second is the one that counts. The
// first is in Go, against the token the read just opened, because the caller
// holds a plaintext and the column holds a ciphertext no plaintext compares
// equal to. It turns the ordinary stale case into ErrStaleRefresh before any
// sealing is spent. The second is in the UPDATE's predicate, against the exact
// ciphertext that read returned: a replica that refreshed between this read and
// this write has changed the column, the predicate matches nothing, and the
// count of zero is the refusal. That is the compare-and-set; the first check is
// only its fast path.
func (s *SQLStore) Refreshed(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	id, expectedAccessToken string,
	tokens *Tokens,
) (*Grant, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(idKey, id),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "storing refreshed grant"))
	}

	if id == "" {
		return nil, s.failed(ctx, op.Error(ErrGrantNotFound, "storing refreshed grant"))
	}

	if err := tokens.validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing refreshed grant"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing refreshed grant"))
	}

	row, err := s.q.GetGrant(ctx, tx, grantsdb.GetGrantParams{ID: id, Scope: scope})
	if err != nil {
		return nil, s.failed(ctx, op.Error(notFound(err), "storing refreshed grant"))
	}

	op.Set(subjectKey, row.Subject).Set(providerKey, row.Provider)

	held, err := s.open(ctx, scope, row.Subject, row.Provider, queries.AccessTokenColumn, row.AccessToken)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing refreshed grant"))
	}

	if subtle.ConstantTimeCompare([]byte(held), []byte(expectedAccessToken)) != 1 {
		return nil, s.failed(ctx, op.Error(ErrStaleRefresh, "storing refreshed grant"))
	}

	params, err := s.refreshParams(ctx, scope, &row, tokens)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing refreshed grant"))
	}

	count, err := s.q.RefreshGrant(ctx, tx, params)
	if err = guardCount(count, err, ErrStaleRefresh); err != nil {
		return nil, s.failed(ctx, op.Error(err, "storing refreshed grant"))
	}

	grant, err := s.readLive(ctx, tx, scope, id)
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the refreshed grant"))
	}

	return grant, nil
}

// refreshParams seals a refresh's tokens for the row they replace, and names the
// ciphertext the row held as the value the write requires it still to hold.
//
// A refresh carrying no refresh token keeps the stored one by rebinding its
// ciphertext unchanged: the row's key is the same, so it still opens.
func (s *SQLStore) refreshParams(
	ctx context.Context,
	scope tenancy.Scope,
	row *grantsdb.GetGrantRow,
	tokens *Tokens,
) (grantsdb.RefreshGrantParams, error) {
	access, err := s.seal(ctx, scope, row.Subject, row.Provider, queries.AccessTokenColumn, tokens.AccessToken)
	if err != nil {
		return grantsdb.RefreshGrantParams{}, err
	}

	refresh := row.RefreshToken
	if tokens.RefreshToken != "" {
		if refresh, err = s.seal(ctx, scope, row.Subject, row.Provider, queries.RefreshTokenColumn, tokens.RefreshToken); err != nil {
			return grantsdb.RefreshGrantParams{}, err
		}
	}

	return grantsdb.RefreshGrantParams{
		AccessToken:          access,
		AccessTokenExpiresAt: expiryPtr(tokens.Expiry),
		RefreshToken:         refresh,
		ID:                   row.ID,
		Scope:                scope,
		ExpectedAccessToken:  row.AccessToken,
	}, nil
}

// Revoke marks a live grant revoked and empties its tokens. See Store.Revoke.
//
// Two writes and a read, on the caller's transaction. The first records the
// reason and empties both token columns, and its row count is the answer to
// whether there was a live grant to revoke. The second is the archive, which
// stamps archived_at from the database's clock — the same clock every other
// stamp on the row comes from. The read-back is the one statement here that
// looks only at revoked rows, because every other single-row read filters them
// out.
func (s *SQLStore) Revoke(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	id string,
	reason RevocationReason,
) (*Grant, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(idKey, id),
		observability.WithValue(reasonKey, string(reason)),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "revoking grant"))
	}

	if id == "" {
		return nil, s.failed(ctx, op.Error(ErrGrantNotFound, "revoking grant"))
	}

	if !reason.valid() {
		return nil, s.failed(ctx, op.Error(platformerrors.Wrapf(ErrUnknownRevocationReason, "%q", reason), "revoking grant"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "revoking grant"))
	}

	count, err := s.q.RevokeGrant(ctx, tx, grantsdb.RevokeGrantParams{
		RevocationReason: string(reason),
		AccessToken:      []byte{},
		RefreshToken:     []byte{},
		ID:               id,
		Scope:            scope,
	})
	if err = guardCount(count, err, ErrGrantNotFound); err != nil {
		return nil, s.failed(ctx, op.Error(err, "revoking grant"))
	}

	count, err = s.q.ArchiveGrant(ctx, tx, grantsdb.ArchiveGrantParams{ID: id, Scope: scope})
	if err = guardCount(count, err, ErrGrantNotFound); err != nil {
		return nil, s.failed(ctx, op.Error(err, "stamping the grant's revocation"))
	}

	row, err := s.q.GetRevokedGrant(ctx, tx, grantsdb.GetRevokedGrantParams{ID: id, Scope: scope})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "reading back the revoked grant"))
	}

	converted := grantsdb.GetGrantRow(row)

	return metadataFromRow(&converted), nil
}

// ListAllForSubject reads every grant one subject holds, revoked ones included,
// opening no token. See Store.ListAllForSubject.
//
// The statement behind it is the set-keyed read, and this binds a set of one.
func (s *SQLStore) ListAllForSubject(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subject string,
) ([]*Grant, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, subject),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if q == nil {
		return nil, s.failed(ctx, op.Error(ErrNilExecutor, "listing a subject's grants"))
	}

	if subject == "" {
		return nil, s.failed(ctx, op.Error(ErrEmptySubject, "listing a subject's grants"))
	}

	if err := scope.Validate(); err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing a subject's grants"))
	}

	rows, err := s.q.ListGrantsForSubjects(ctx, q, grantsdb.ListGrantsForSubjectsParams{
		Scope:    scope,
		Subjects: []string{subject},
	})
	if err != nil {
		return nil, s.failed(ctx, op.Error(err, "listing a subject's grants"))
	}

	grants := make([]*Grant, 0, len(rows))
	for i := range rows {
		grants = append(grants, grantFromListRow(&rows[i]))
	}

	op.SpanOnly(countKey, len(grants))

	return grants, nil
}

// DeleteForSubject destroys every grant one subject holds in the scope. See
// Store.DeleteForSubject.
//
// The count is not guarded: this names a subject rather than a row, and a
// subject with no grants is the ordinary case an erasure runs into.
func (s *SQLStore) DeleteForSubject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subject string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subjectKey, subject),
	)
	defer op.End()
	defer op.Time(ctx, nil, s.instruments.Latency)()

	s.instruments.Attempt(ctx)

	if tx == nil {
		return 0, s.failed(ctx, op.Error(ErrNilExecutor, "erasing a subject's grants"))
	}

	if subject == "" {
		return 0, s.failed(ctx, op.Error(ErrEmptySubject, "erasing a subject's grants"))
	}

	if err := scope.Validate(); err != nil {
		return 0, s.failed(ctx, op.Error(err, "erasing a subject's grants"))
	}

	deleted, err := s.q.DeleteGrantsForSubject(ctx, tx, grantsdb.DeleteGrantsForSubjectParams{
		Scope:   scope,
		Subject: subject,
	})
	if err != nil {
		return 0, s.failed(ctx, op.Error(err, "erasing a subject's grants"))
	}

	op.Set(countKey, deleted)

	return deleted, nil
}

// readLive reads one live grant by id and opens its tokens. It is the read-back
// every write that leaves a live row answers with.
func (s *SQLStore) readLive(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, id string) (*Grant, error) {
	row, err := s.q.GetGrant(ctx, q, grantsdb.GetGrantParams{ID: id, Scope: scope})
	if err != nil {
		return nil, notFound(err)
	}

	return s.openRow(ctx, &row)
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

// notFound maps a driver's empty-result error onto ErrGrantNotFound, leaving
// anything else alone — a read that found nothing and a read that failed are
// different answers, and a worker told "no grant" when the database was
// unreachable would stop syncing an account that is still connected.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGrantNotFound
	}

	return err
}
