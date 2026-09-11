package oauth2clients

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/internal/oauth2clientsdb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlguard"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultTablePrefix is the namespace the registered clients table carries when
// none is configured, which is none — rendering oauth2_registered_clients.
//
// The oauth2 name is the schema's, not the caller's: a table always says which
// package created it. Setting a namespace of "ddb" renders
// ddb_oauth2_registered_clients, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans, logger and instruments.
const storeName = serviceName + "_store"

// The observability keys this package attaches to its operations. The client_id
// is the protocol identifier and is not a secret — it travels in every
// /authorize URL — so it is safe to record. The secret digest never is, and
// there is no key for it.
const (
	scopeKey    = serviceName + ".scope"
	clientKey   = serviceName + ".id"
	clientIDKey = serviceName + ".client_id"
	ownerKey    = serviceName + ".belongs_to_user"
	countKey    = serviceName + ".count"
)

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed [Store], against the schema
// authentication/oauth2clients/migrations renders.
//
// It is exported, and returned by [NewSQLStore], so a caller who has chosen SQL
// storage can depend on that choice rather than on the seam every backing
// shares.
type SQLStore struct {
	q    oauth2clientsdb.Querier
	o11y observability.Observer

	// The two things a write here means when it affects no row. See
	// internal/sqlguard.
	//
	// There are two because the answer is two different facts. An UPDATE or a
	// DELETE that matched nothing is the row not being there, which is what
	// missing describes. An INSERT that wrote nothing is the client_id already
	// being in use — the statement skips a conflicting row rather than raising,
	// so zero is how a duplicate arrives without parsing a dialect's SQLSTATE —
	// and that is what taken describes. One guard carrying both would have to
	// pick one sentinel and one line to log, which is the drift internal/sqlguard
	// exists to prevent rather than a saving.
	missing sqlguard.Guard
	taken   sqlguard.Guard

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger         logging.Logger
	tracerProvider tracing.Provider
	prefix         string
}

// NewSQLStore builds a registered client store over the given database.
//
// The dialect comes from the client, so the two cannot disagree. The prefix must
// still match the one the migrations were rendered with — nothing here can check
// that, and a mismatch surfaces as a missing table on the first query rather
// than at construction.
//
// The client is taken for its dialect and for nothing else, and the store keeps
// no reference to it. Every write is handed a database.Tx and every read an
// executor, so there is no statement this store runs on a connection of its own:
// no Writer() for the writes, no Reader() for the reads, and no CurrentTime(),
// because every timestamp in this table is the database server's. A consumer
// with nothing to join opens a transaction with Client.WithTransaction and
// passes the Tx it is handed; [Service] is what opens one for the operations
// that have a hook to run inside it.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger and traces to a noop provider. It takes no metrics provider,
// and that is a statement rather than an omission — this store ships no
// instruments, because what is worth counting about a registry is the
// operations over it rather than the statements under them, and [Service] is
// where those are counted.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "oauth2clients dialect %q", d)
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
	// substitution.
	qd, err := querierDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := oauth2clientsdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the oauth2clients querier")
	}

	s.q = q
	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	// Neither guard carries a MissCounter, which is the same statement this
	// store's constructor makes about instruments generally: what is worth
	// counting about a registry is the operations over it, and Service is where
	// those are counted. sqlguard reports a miss on the span and in the log
	// either way.
	s.missing = sqlguard.Guard{
		NotFound:  ErrClientNotFound,
		Namespace: serviceName,
		IDKey:     clientKey,
		Message:   "oauth2 client was gone before the write could reach it",
		Reason:    "oauth2 client %q is not there to write to",
	}

	s.taken = sqlguard.Guard{
		NotFound:  ErrClientIDTaken,
		Namespace: serviceName,
		IDKey:     clientKey,
		Message:   "minted oauth2 client identifier was already registered",
		Reason:    "oauth2 client %q was minted with an identifier already in use",
	}

	return s, nil
}

// TablePrefix returns the namespace this store's table carries, for a caller
// rendering the migrations it needs.
func (s *SQLStore) TablePrefix() string { return s.prefix }

// CreateClient records a registration. See [Store.CreateClient].
func (s *SQLStore) CreateClient(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	client *Client,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilTransaction, "creating oauth2 client")
	}

	if client == nil {
		return op.Error(ErrNilClient, "creating oauth2 client")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "creating oauth2 client")
	}

	// The scope is the argument's, and a registration that names a different one
	// is refused rather than corrected. A registration that names none adopts
	// it, which is the same reading comments takes of a reply naming no target.
	switch {
	case client.Scope == tenancy.Scope{}:
		client.Scope = scope
	case client.Scope != scope:
		return op.Error(ErrScopeMismatch, "creating oauth2 client")
	}

	if err := validateClient(client); err != nil {
		return op.Error(err, "creating oauth2 client")
	}

	op.SpanOnly(clientKey, client.ID)
	op.SpanOnly(clientIDKey, client.ClientID)

	count, err := s.q.CreateRegisteredClient(ctx, tx, oauth2clientsdb.CreateRegisteredClientParams{
		ID:            client.ID,
		Scope:         client.Scope,
		BelongsToUser: client.BelongsToUser,
		Name:          client.Name,
		Description:   client.Description,
		ClientID:      client.ClientID,
		SecretHash:    client.SecretHash,
		RedirectUris:  encodeStrings(client.RedirectURIs),
		Scopes:        encodeStrings(client.Scopes),
	})
	// A duplicate client_id is not a caller error and there is nothing for them
	// to correct: the identifier was minted here from crypto/rand. See the taken
	// guard for why zero rows is how it arrives.
	if writeErr := s.taken.Count(ctx, op, count, err, client.ID, "create", "creating oauth2 client"); writeErr != nil {
		return writeErr
	}

	// The creation time is the database's, so the value the caller handed over
	// still holds the zero time. Reading it back on the same transaction is what
	// keeps a response from saying 0001-01-01 for a row written a moment ago.
	created, err := s.q.GetRegisteredClientCreatedAt(ctx, tx,
		oauth2clientsdb.GetRegisteredClientCreatedAtParams{ID: client.ID})
	if err != nil {
		return op.Error(err, "reading back the creation time of oauth2 client %q", client.ID)
	}

	client.CreatedAt = created.CreatedAt

	return nil
}

// UpdateClient revises one live registration. See [Store.UpdateClient].
func (s *SQLStore) UpdateClient(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	id string,
	input *UpdateInput,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(clientKey, id),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilTransaction, "updating oauth2 client")
	}

	if input == nil {
		return op.Error(ErrNilInput, "updating oauth2 client")
	}

	if id == "" {
		return op.Error(ErrEmptyID, "updating oauth2 client")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating oauth2 client")
	}

	if err := validateDescriptive(input.Name, input.RedirectURIs); err != nil {
		return op.Error(err, "updating oauth2 client %q", id)
	}

	count, err := s.q.UpdateRegisteredClient(ctx, tx, oauth2clientsdb.UpdateRegisteredClientParams{
		ID:           id,
		Scope:        scope,
		Name:         input.Name,
		Description:  input.Description,
		RedirectUris: encodeStrings(input.RedirectURIs),
		Scopes:       encodeStrings(input.Scopes),
	})

	return s.missing.Count(ctx, op, count, err, id, "update", "updating oauth2 client")
}

// ArchiveClient withdraws one registration. See [Store.ArchiveClient].
func (s *SQLStore) ArchiveClient(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	id string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(clientKey, id),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilTransaction, "archiving oauth2 client")
	}

	if id == "" {
		return op.Error(ErrEmptyID, "archiving oauth2 client")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving oauth2 client")
	}

	count, err := s.q.ArchiveRegisteredClient(ctx, tx,
		oauth2clientsdb.ArchiveRegisteredClientParams{ID: id, Scope: scope})

	return s.missing.Count(ctx, op, count, err, id, "archive", "archiving oauth2 client")
}

// GetClient reads one live registration by row id. See [Store.GetClient].
func (s *SQLStore) GetClient(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	id string,
) (*Client, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(clientKey, id),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading oauth2 client")
	}

	if id == "" {
		return nil, op.Error(ErrEmptyID, "reading oauth2 client")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading oauth2 client")
	}

	row, err := s.q.GetRegisteredClient(ctx, q,
		oauth2clientsdb.GetRegisteredClientParams{ID: id, Scope: scope})
	if err != nil {
		return nil, op.Error(notFound(err, ErrClientNotFound), "reading oauth2 client %q", id)
	}

	client, err := clientFromRow(&row)
	if err != nil {
		return nil, op.Error(err, "reading oauth2 client %q", id)
	}

	return client, nil
}

// ResolveClientID reads a registration by the identifier the client sent, across
// every registry. See [Store.ResolveClientID] for why it takes no scope.
func (s *SQLStore) ResolveClientID(
	ctx context.Context,
	q database.SQLQueryExecutor,
	clientID string,
) (*Client, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(clientIDKey, clientID))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "resolving oauth2 client identifier")
	}

	if clientID == "" {
		return nil, op.Error(ErrEmptyClientID, "resolving oauth2 client identifier")
	}

	row, err := s.q.GetRegisteredClientByClientID(ctx, q,
		oauth2clientsdb.GetRegisteredClientByClientIDParams{ClientID: clientID})
	if err != nil {
		return nil, op.Error(notFound(err, ErrClientNotFound),
			"resolving oauth2 client identifier %q", clientID)
	}

	// The row type is the get's projection with the same fields in the same
	// order, so this converts rather than restating: the day the two stop being
	// identical this line stops building.
	client, err := clientFromRow((*oauth2clientsdb.GetRegisteredClientRow)(&row))
	if err != nil {
		return nil, op.Error(err, "resolving oauth2 client identifier %q", clientID)
	}

	// The scope this read resolved, recorded on the span. It is the whole point
	// of the method, and it is the value an operator needs when they are looking
	// at why an authorization request was refused.
	op.SpanOnly(scopeKey, client.Scope.String())

	return client, nil
}

// ListClients pages one registry's registrations. See [Store.ListClients].
func (s *SQLStore) ListClients(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Client], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing oauth2 clients")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing oauth2 clients")
	}

	filter = pageFilter(filter)
	w := windowFrom(filter)

	rows, err := sortedRows(filter,
		func() ([]oauth2clientsdb.ListRegisteredClientsRow, error) {
			return s.q.ListRegisteredClients(ctx, q, oauth2clientsdb.ListRegisteredClientsParams{
				CreatedAfter: w.createdAfter, CreatedBefore: w.createdBefore,
				UpdatedAfter: w.updatedAfter, UpdatedBefore: w.updatedBefore,
				IncludeArchived: w.includeArchived, Scope: scope,
				PageCursor: w.pageCursor, ResultLimit: w.resultLimit,
			})
		},
		func() ([]oauth2clientsdb.ListRegisteredClientsDescendingRow, error) {
			return s.q.ListRegisteredClientsDescending(ctx, q,
				oauth2clientsdb.ListRegisteredClientsDescendingParams{
					CreatedAfter: w.createdAfter, CreatedBefore: w.createdBefore,
					UpdatedAfter: w.updatedAfter, UpdatedBefore: w.updatedBefore,
					IncludeArchived: w.includeArchived, Scope: scope,
					PageCursor: w.pageCursor, ResultLimit: w.resultLimit,
				})
		},
		func(r oauth2clientsdb.ListRegisteredClientsDescendingRow) oauth2clientsdb.ListRegisteredClientsRow {
			return oauth2clientsdb.ListRegisteredClientsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing oauth2 clients")
	}

	return s.drain(op, rows, filter, "listing oauth2 clients")
}

// ListClientsForOwner pages one person's registrations. See
// [Store.ListClientsForOwner].
func (s *SQLStore) ListClientsForOwner(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
	filter *filtering.QueryFilter,
) (*filtering.QueryFilteredResult[Client], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(ownerKey, userID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing a user's oauth2 clients")
	}

	// Refused rather than answered with the administered rows. A self-service
	// listing that quietly widened to the registrations nobody owns would hand a
	// caller with no session the deployment's own credentials.
	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "listing a user's oauth2 clients")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing a user's oauth2 clients")
	}

	filter = pageFilter(filter)
	w := windowFrom(filter)

	rows, err := sortedRows(filter,
		func() ([]oauth2clientsdb.ListRegisteredClientsForOwnerRow, error) {
			return s.q.ListRegisteredClientsForOwner(ctx, q,
				oauth2clientsdb.ListRegisteredClientsForOwnerParams{
					CreatedAfter: w.createdAfter, CreatedBefore: w.createdBefore,
					UpdatedAfter: w.updatedAfter, UpdatedBefore: w.updatedBefore,
					IncludeArchived: w.includeArchived, Scope: scope,
					BelongsToUser: userID,
					PageCursor:    w.pageCursor, ResultLimit: w.resultLimit,
				})
		},
		func() ([]oauth2clientsdb.ListRegisteredClientsForOwnerDescendingRow, error) {
			return s.q.ListRegisteredClientsForOwnerDescending(ctx, q,
				oauth2clientsdb.ListRegisteredClientsForOwnerDescendingParams{
					CreatedAfter: w.createdAfter, CreatedBefore: w.createdBefore,
					UpdatedAfter: w.updatedAfter, UpdatedBefore: w.updatedBefore,
					IncludeArchived: w.includeArchived, Scope: scope,
					BelongsToUser: userID,
					PageCursor:    w.pageCursor, ResultLimit: w.resultLimit,
				})
		},
		//nolint:lll // The conversion names two generated types; splitting it reads worse than the width.
		func(r oauth2clientsdb.ListRegisteredClientsForOwnerDescendingRow) oauth2clientsdb.ListRegisteredClientsForOwnerRow {
			return oauth2clientsdb.ListRegisteredClientsForOwnerRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing a user's oauth2 clients")
	}

	// The two list statements are one projection rendered twice with different
	// predicates, so this converts rather than restating.
	base := make([]oauth2clientsdb.ListRegisteredClientsRow, 0, len(rows))
	for i := range rows {
		base = append(base, oauth2clientsdb.ListRegisteredClientsRow(rows[i]))
	}

	return s.drain(op, base, filter, "listing a user's oauth2 clients")
}

// drain converts a page of rows and reports it as the filtered result both
// paged reads answer with.
//
// The cursor is the id, because both statements order by it. A cursor naming a
// position in an order the query does not use is a page that skips rows and
// repeats others, with nothing reporting an error.
func (s *SQLStore) drain(
	op observability.Operation,
	listed []oauth2clientsdb.ListRegisteredClientsRow,
	filter *filtering.QueryFilter,
	operation string,
) (*filtering.QueryFilteredResult[Client], error) {
	rows := make([]pageRow, 0, len(listed))

	for i := range listed {
		row, err := clientPageRow(&listed[i])
		if err != nil {
			return nil, op.Error(err, "%s", operation)
		}

		rows = append(rows, row)
	}

	op.SpanOnly(countKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(c *Client) string { return c.ID }, filter), nil
}

// querierDialect maps this module's dialect onto the generated package's.
func querierDialect(d dialect.Dialect) (oauth2clientsdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return oauth2clientsdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return oauth2clientsdb.DialectMySQL, nil
	case dialect.SQLite:
		return oauth2clientsdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated oauth2clients queries for dialect %q", d)
	}
}

// notFound maps a driver's empty-result error onto this package's sentinel,
// leaving anything else alone.
//
// A read that found nothing and a read that failed are different answers, and
// collapsing them is how "the database was unreachable" gets reported to an
// unauthenticated caller as "no such client".
func notFound(err, sentinel error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return sentinel
	}

	return err
}
