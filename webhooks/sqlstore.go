package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/primandproper/platform-go/v14/webhooks/internal/webhooksdb"
	"github.com/primandproper/platform-go/v14/webhooks/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"
)

// DefaultTablePrefix is the namespace the webhooks tables carry when none is
// configured, which is none — rendering webhooks_endpoints and its four siblings.
//
// The webhooks_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_webhooks_endpoints, for a database shared between applications. A namespace must
// not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

// storeName scopes the store's spans and logger. It is deliberately not
// serviceName: a trace showing a delivery going out and the rows that moved
// wants those distinguishable, and one scope for both would make a claim read
// like an HTTP call in every span listing.
const storeName = serviceName + "_store"

var _ Store = (*SQLStore)(nil)

// SQLStore is the SQL-backed Store, against the schema webhooks/migrations
// renders.
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam every backing
// shares.
type SQLStore struct {
	client database.Client
	q      webhooksdb.Querier
	o11y   observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	// Read s.o11y.Logger() for the logger this store actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger         logging.Logger
	tracerProvider tracing.Provider
	prefix         string
}

// NewSQLStore builds a Store over the given database.
//
// The client is not what the consumer writes run on. Every write an application
// calls on its own behalf is handed a database.Tx and executes on that, so this
// store opens no transaction of its own for SaveEndpoint, ArchiveEndpoint,
// AddSubscription or ArchiveSubscription, and its reads run on the executor
// their caller names. What the client is still here for is the delivery
// machinery — Claim, MarkDelivered, RecordFailure, RecordAttempt, Requeue,
// Backlog and Reap own their statements, because there is no consumer
// transaction for them to join — and the dialect, which is read off it at
// construction so the two cannot disagree.
//
// The prefix must still match the one the migrations were rendered with —
// nothing here can check that, and a mismatch surfaces as a missing table on the
// first query rather than at construction.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger and traces to a noop provider.
//
// It takes no clock. Every timestamp this store writes is the database server's
// — the convention columns are stamped by the statements themselves, and the
// instants a caller cares about (when a delivery was enqueued, when a dispatch
// was leased, how far back a reap goes) are arguments rather than clock reads.
// That is database/querygen's rule rather than a preference taken here: a row's
// created_at and the filter window compared against it have to come from one
// clock, or two application instances a second apart write rows a window
// excludes at random.
func NewSQLStore(client database.Client, opts ...SQLStoreOption) (*SQLStore, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	d := client.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "webhooks dialect %q", d)
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
	// substitution; see webhooks/internal/webhooksdb.
	qd, err := webhooksdbDialect(d)
	if err != nil {
		return nil, err
	}

	q, err := webhooksdb.New(qd, ddl.Qualify(s.prefix))
	if err != nil {
		return nil, platformerrors.Wrap(err, "building the webhooks querier")
	}

	s.q = q
	s.o11y = observability.NewObserver(storeName, s.logger, s.tracerProvider)

	return s, nil
}

// webhooksdbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSQLStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is
// a construction failure like any other, and it names the dialect rather than
// panicking or leaning on webhooksdb.New refusing the empty string.
func webhooksdbDialect(d dialect.Dialect) (webhooksdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return webhooksdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return webhooksdb.DialectMySQL, nil
	case dialect.SQLite:
		return webhooksdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated webhooks queries for dialect %q", d)
	}
}

// ErrNilDatabaseClient indicates a nil database.Client. It wraps
// errors.ErrNilInputParameter, so a caller may check either.
var ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webhooks database client")

// SaveEndpoint upserts the endpoint and reconciles its subscription set, both
// through the caller's transaction.
//
// It takes the transaction rather than opening one for two reasons that happen
// to point the same way. A half-registered endpoint would either receive events
// it no longer subscribes to or silently receive none, so the upsert and the
// reconciliation have to commit together; and a registration is almost never
// the only thing a consumer writes — the audit entry naming who registered it
// belongs in the same commit, and a store that opened its own transaction would
// have committed the endpoint while that entry was still refusable.
//
// The scope is the argument rather than Endpoint.Scope, so what the statement
// binds is what the caller named. An endpoint that names no scope adopts it; one
// naming a different tenant is ErrScopeMismatch. The row and the predicate
// therefore cannot disagree.
func (s *SQLStore) SaveEndpoint(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpoint *Endpoint) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "saving webhook endpoint")
	}

	if endpoint == nil {
		return op.Error(ErrNilEndpoint, "saving webhook endpoint")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "saving webhook endpoint %q", endpoint.ID)
	}

	if err := adoptEndpointScope(scope, endpoint); err != nil {
		return op.Error(err, "saving webhook endpoint %q", endpoint.ID)
	}

	op.Set(endpointIDKey, endpoint.ID).
		Set(endpointURLKey, endpoint.URL)

	headers, err := json.Marshal(endpoint.Headers)
	if err != nil {
		return op.Error(err, "marshaling webhook endpoint headers")
	}

	events := endpoint.EventTypes()

	if err = s.checkEndpointScope(ctx, tx, scope, endpoint.ID); err != nil {
		return op.Error(err, "saving webhook endpoint %q", endpoint.ID)
	}

	if err = s.q.UpsertEndpoint(ctx, tx, webhooksdb.UpsertEndpointParams{
		ID:             endpoint.ID,
		Scope:          scope,
		CreatedBy:      ownerOrNil(endpoint.CreatedBy),
		Name:           endpoint.Name,
		URL:            endpoint.URL,
		ContentType:    endpoint.ContentType,
		SecretCurrent:  endpoint.Secret.Current,
		SecretPrevious: secretOrNil(endpoint.Secret.Previous),
		Headers:        headers,
		Disabled:       endpoint.Disabled,
	}); err != nil {
		return op.Error(err, "upserting webhook endpoint")
	}

	// One statement per named event type rather than one multi-row write.
	// The cardinality is a caller's, so a multi-row VALUES list has no
	// static text for sqlc to check; what these cost is a round trip each,
	// inside the transaction the caller is already holding.
	//
	// Each carries a freshly generated id that the converging case throws
	// away: a row that already names this (endpoint, event type) is revived
	// in place and keeps the id it had, which is why the caller's
	// Subscriptions are filled from a read below rather than from these.
	for _, event := range events {
		if err = s.q.UpsertSubscription(ctx, tx, webhooksdb.UpsertSubscriptionParams{
			ID:         identifiers.New(),
			EndpointID: endpoint.ID,
			EventType:  event.String(),
		}); err != nil {
			return op.Error(err, "upserting webhook endpoint subscription")
		}
	}

	if endpoint.Subscriptions, err = s.reconcileSubscriptions(ctx, tx, endpoint.ID, events); err != nil {
		return op.Error(err, "saving webhook endpoint %q", endpoint.ID)
	}

	return nil
}

// adoptEndpointScope settles which tenant a save is for, and writes the answer
// back onto the endpoint.
//
// The scope the call named is the one the statement binds, so an endpoint that
// names a different one is refused rather than corrected: the two disagreeing is
// a caller holding one tenant's endpoint and writing it into another, which is a
// stale value or a mix-up and is not a thing to guess at. An endpoint that names
// none adopts the argument. tenancy.Scope tells the zero value apart from
// Global(), so "unset" here is genuinely unset rather than the global scope
// spelled shortly.
func adoptEndpointScope(scope tenancy.Scope, endpoint *Endpoint) error {
	if endpoint.Scope != (tenancy.Scope{}) && endpoint.Scope != scope {
		return platformerrors.Wrapf(ErrScopeMismatch,
			"endpoint names %q, the write names %q", endpoint.Scope, scope)
	}

	endpoint.Scope = scope

	return nil
}

// reconcileSubscriptions retires every live subscription the save did not name
// and returns the ones that survive.
//
// They are archived rather than deleted, which is the difference between "this
// endpoint no longer receives order.created" and "this endpoint never did". The
// second is not true, and the delivery log that says otherwise outlives the
// subscription.
//
// It reads the live set and archives from it, one statement per retired pair,
// rather than issuing one write carrying the set that survived. A negated set is
// the one predicate whose empty case means opposite things on the two dialect
// families — Postgres's empty `<> ALL` matches everything where an expanded
// `NOT IN (NULL)` matches nothing — so the statement this corpus will not render
// is exactly the one whose failure mode is archiving an endpoint's whole
// subscription set. The read is not an extra round trip either: the caller is
// owed the rows that are live afterwards, ids included, and this is that read.
//
// An empty event list archives all of them. That is not the same as doing
// nothing: a caller saving an endpoint with no subscriptions is refused by
// Validate long before this, but a Store implementation is not the place to
// re-derive that, and "the set is empty" has one honest meaning here.
func (s *SQLStore) reconcileSubscriptions(
	ctx context.Context,
	q database.SQLQueryExecutor,
	endpointID string,
	events []EventType,
) ([]Subscription, error) {
	live, err := s.subscriptionsFor(ctx, q, endpointID)
	if err != nil {
		return nil, err
	}

	named := make(map[EventType]struct{}, len(events))
	for _, event := range events {
		named[event] = struct{}{}
	}

	kept := make([]Subscription, 0, len(live))

	for i := range live {
		if _, ok := named[live[i].EventType]; ok {
			kept = append(kept, live[i])

			continue
		}

		if _, err = s.q.ArchiveSubscriptionByPair(ctx, q, webhooksdb.ArchiveSubscriptionByPairParams{
			EndpointID: endpointID,
			EventType:  live[i].EventType.String(),
		}); err != nil {
			return nil, platformerrors.Wrap(err, "archiving retired webhook endpoint subscription")
		}
	}

	return kept, nil
}

// GetEndpoint reads one of the scope's endpoints and its subscriptions, on the
// caller's executor. An endpoint registered in another scope reads as absent.
//
// The executor is the caller's rather than a reader of the store's own, so a
// caller inside a transaction reads that transaction: an endpoint saved and then
// read back before the commit is there, which a read pinned to a replica — or to
// the primary outside the transaction — would report as absent.
func (s *SQLStore) GetEndpoint(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, endpointID string) (*Endpoint, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(endpointIDKey, endpointID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading webhook endpoint %q", endpointID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading webhook endpoint %q", endpointID)
	}

	row, err := s.q.GetEndpoint(ctx, q, webhooksdb.GetEndpointParams{
		ID:    endpointID,
		Scope: scope,
	})
	if err != nil {
		return nil, op.Error(err, "reading webhook endpoint %q", endpointID)
	}

	columns := endpointFromGet(&row)

	endpoint, err := columns.endpoint()
	if err != nil {
		return nil, op.Error(err, "reading webhook endpoint %q", endpointID)
	}

	if endpoint.Subscriptions, err = s.subscriptionsFor(ctx, q, endpointID); err != nil {
		return nil, op.Error(err, "reading webhook endpoint %q subscriptions", endpointID)
	}

	return endpoint, nil
}

// ListEndpoints pages one scope's registry, in the direction the filter names.
//
// The direction is a choice between two generated statements rather than an
// argument either of them binds — see sortedRows — so what this method does with
// filter.SortBy is pick the one whose ORDER BY and cursor comparison agree with
// it.
func (s *SQLStore) ListEndpoints(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Endpoint], error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing webhook endpoints")
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing webhook endpoints")
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]webhooksdb.ListEndpointsRow, error) {
			return s.q.ListEndpoints(ctx, q, listEndpointsParams(scope, filter))
		},
		func() ([]webhooksdb.ListEndpointsDescendingRow, error) {
			return s.q.ListEndpointsDescending(ctx, q,
				webhooksdb.ListEndpointsDescendingParams(listEndpointsParams(scope, filter)))
		},
		func(r webhooksdb.ListEndpointsDescendingRow) webhooksdb.ListEndpointsRow {
			return webhooksdb.ListEndpointsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing webhook endpoints")
	}

	rows := make([]pageRow[Endpoint], 0, len(listRows))

	for i := range listRows {
		columns := endpointFromListRow(&listRows[i])

		endpoint, convErr := columns.endpoint()
		if convErr != nil {
			return nil, op.Error(convErr, "listing webhook endpoints")
		}

		// Subscriptions are read per endpoint rather than through a join, so
		// that one endpoint with thirty event types does not multiply every
		// other row in the page by its subscription count.
		if endpoint.Subscriptions, err = s.subscriptionsFor(ctx, q, endpoint.ID); err != nil {
			return nil, op.Error(err, "listing webhook endpoints")
		}

		rows = append(rows, pageRow[Endpoint]{
			value:    endpoint,
			filtered: listRows[i].FilteredCount,
			total:    listRows[i].TotalCount,
		})
	}

	op.SpanOnly(endpointCountKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(e *Endpoint) string { return e.ID }, filter), nil
}

// ArchiveEndpoint retires one of the scope's endpoints, through the caller's
// transaction. An endpoint in another scope is not touched.
func (s *SQLStore) ArchiveEndpoint(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(endpointIDKey, endpointID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving webhook endpoint %q", endpointID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving webhook endpoint %q", endpointID)
	}

	// The count is deliberately dropped. An archive that names nothing and an
	// archive of something already archived are both "this endpoint is not
	// live", which is the state the caller asked for; the distinction between
	// them is a read, and GetEndpoint is that read.
	if _, err := s.q.ArchiveEndpoint(ctx, tx, webhooksdb.ArchiveEndpointParams{
		ID:    endpointID,
		Scope: scope,
	}); err != nil {
		return op.Error(err, "archiving webhook endpoint %q", endpointID)
	}

	return nil
}

// AddSubscription subscribes one of the scope's endpoints to eventType.
//
// It is an upsert and a read rather than an insert, because a subscription is
// identified by the (endpoint, event type) pair: subscribing to something the
// endpoint already subscribes to is not an error, and re-subscribing to
// something it archived revives that row rather than minting a second one for
// the same pair. Both cases return the row that is now live, which is why the
// write is followed by a read — a revived row keeps the ID it already had.
//
// All three statements run in the caller's transaction, so the row read back is
// the row written and not one a concurrent archive has since retired — and the
// subscription commits with whatever else that transaction did.
func (s *SQLStore) AddSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string, eventType EventType) (*Subscription, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(endpointIDKey, endpointID),
		observability.WithValue(eventTypeKey, eventType.String()),
	)
	defer op.End()

	if tx == nil {
		return nil, op.Error(ErrNilExecutor, "subscribing webhook endpoint %q", endpointID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "subscribing webhook endpoint %q", endpointID)
	}

	if eventType == "" {
		return nil, op.Error(ErrEmptyEventType, "subscribing webhook endpoint %q", endpointID)
	}

	// The endpoint is read within the scope first, so that subscribing to an
	// endpoint belonging to somebody else is a not-found rather than a write:
	// the subscriptions table has no scope of its own to filter on, and the
	// foreign key alone would happily accept the row.
	if _, err := s.q.GetEndpoint(ctx, tx, webhooksdb.GetEndpointParams{
		ID:    endpointID,
		Scope: scope,
	}); err != nil {
		return nil, op.Error(
			platformerrors.Wrapf(err, "reading webhook endpoint %q", endpointID),
			"subscribing webhook endpoint %q to %q", endpointID, eventType,
		)
	}

	if err := s.q.UpsertSubscription(ctx, tx, webhooksdb.UpsertSubscriptionParams{
		ID:         identifiers.New(),
		EndpointID: endpointID,
		EventType:  eventType.String(),
	}); err != nil {
		return nil, op.Error(err, "upserting webhook subscription")
	}

	row, err := s.q.GetSubscriptionByPair(ctx, tx, webhooksdb.GetSubscriptionByPairParams{
		EndpointID: endpointID,
		EventType:  eventType.String(),
	})
	if err != nil {
		return nil, op.Error(err, "reading webhook subscription")
	}

	columns := subscriptionFromPair(&row)
	subscription := columns.subscription()

	op.Set(subscriptionIDKey, subscription.ID)

	return &subscription, nil
}

// GetSubscription reads one of the scope's subscriptions on the caller's
// executor, archived ones included. A subscription under another scope's
// endpoint reads as absent.
func (s *SQLStore) GetSubscription(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subscriptionID string) (*Subscription, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subscriptionIDKey, subscriptionID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading webhook subscription %q", subscriptionID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading webhook subscription %q", subscriptionID)
	}

	row, err := s.q.GetSubscription(ctx, q, webhooksdb.GetSubscriptionParams{
		ID:    subscriptionID,
		Scope: scope,
	})
	if err != nil {
		return nil, op.Error(err, "reading webhook subscription %q", subscriptionID)
	}

	columns := subscriptionFromGet(&row)
	subscription := columns.subscription()

	return &subscription, nil
}

// ListSubscriptions pages the live subscriptions of one of the scope's
// endpoints, on the caller's executor and in the direction the filter names.
func (s *SQLStore) ListSubscriptions(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, endpointID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Subscription], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(endpointIDKey, endpointID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing webhook subscriptions for endpoint %q", endpointID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing webhook subscriptions for endpoint %q", endpointID)
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]webhooksdb.ListSubscriptionsRow, error) {
			return s.q.ListSubscriptions(ctx, q, listSubscriptionsParams(scope, endpointID, filter))
		},
		func() ([]webhooksdb.ListSubscriptionsDescendingRow, error) {
			return s.q.ListSubscriptionsDescending(ctx, q,
				webhooksdb.ListSubscriptionsDescendingParams(listSubscriptionsParams(scope, endpointID, filter)))
		},
		func(r webhooksdb.ListSubscriptionsDescendingRow) webhooksdb.ListSubscriptionsRow {
			return webhooksdb.ListSubscriptionsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing webhook subscriptions for endpoint %q", endpointID)
	}

	rows := make([]pageRow[Subscription], 0, len(listRows))

	for i := range listRows {
		columns := subscriptionFromListRow(&listRows[i])
		subscription := columns.subscription()

		rows = append(rows, pageRow[Subscription]{
			value:    &subscription,
			filtered: listRows[i].FilteredCount,
			total:    listRows[i].TotalCount,
		})
	}

	op.SpanOnly(subscriptionCountKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(sub *Subscription) string { return sub.ID }, filter), nil
}

// ArchiveSubscription retires one of the scope's subscriptions, through the
// caller's transaction. A subscription under another scope's endpoint is not
// touched.
//
// Like ArchiveEndpoint it does not report whether it matched a row. An archive
// that names nothing and an archive of something already archived are both
// "this subscription is not live", which is the state the caller asked for; the
// distinction between them is a read, and GetSubscription is that read.
func (s *SQLStore) ArchiveSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscriptionID string) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(subscriptionIDKey, subscriptionID),
	)
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "archiving webhook subscription %q", subscriptionID)
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "archiving webhook subscription %q", subscriptionID)
	}

	if _, err := s.q.ArchiveSubscription(ctx, tx, webhooksdb.ArchiveSubscriptionParams{
		ID:    subscriptionID,
		Scope: scope,
	}); err != nil {
		return op.Error(err, "archiving webhook subscription %q", subscriptionID)
	}

	return nil
}

// EndpointsForEvent resolves the fan-out set within one scope, using the
// caller's executor so it sees the same snapshot as the transaction that is
// dispatching.
func (s *SQLStore) EndpointsForEvent(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, eventType EventType) ([]*Endpoint, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(eventTypeKey, eventType.String()),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "reading webhook endpoints for event %q", eventType)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "reading webhook endpoints for event %q", eventType)
	}

	// Disabled endpoints are excluded here rather than at delivery, so no
	// dispatch row is ever created for them. The flag is bound rather than
	// written into the statement as a literal, which is what makes it a value
	// this call decides rather than one the corpus assumes.
	rows, err := s.q.ListEndpointsForEvent(ctx, q, webhooksdb.ListEndpointsForEventParams{
		Scope:     scope,
		Disabled:  false,
		EventType: eventType.String(),
	})
	if err != nil {
		return nil, op.Error(err, "reading webhook endpoints for event %q", eventType)
	}

	endpoints := make([]*Endpoint, 0, len(rows))

	for i := range rows {
		columns := endpointFromEventRow(&rows[i])

		endpoint, convErr := columns.endpoint()
		if convErr != nil {
			return nil, op.Error(convErr, "reading webhook endpoints for event %q", eventType)
		}

		endpoints = append(endpoints, endpoint)
	}

	op.SpanOnly(endpointCountKey, len(endpoints))

	return endpoints, nil
}

// Enqueue writes the delivery and its dispatches through the caller's transaction,
// so they commit with whatever else that transaction did.
//
// Every dispatch in one call carries the instant the caller named, as both its
// creation time and its first next_attempt: a new dispatch is eligible
// immediately, and the claim's per-key ordering predicate compares
// (created_at, id) across the rows one fan-out produced.
func (s *SQLStore) Enqueue(ctx context.Context, tx database.Tx, delivery *Delivery, endpointIDs []string, now time.Time) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(fanoutKey, len(endpointIDs)))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "enqueuing webhook delivery")
	}

	if delivery == nil {
		return op.Error(ErrNilDelivery, "enqueuing webhook delivery")
	}

	if err := delivery.Scope.Validate(); err != nil {
		return op.Error(err, "enqueuing webhook delivery %q", delivery.ID)
	}

	op.Set(deliveryIDKey, delivery.ID).
		Set(scopeKey, delivery.Scope.String()).
		Set(eventTypeKey, delivery.EventType.String())

	if len(endpointIDs) == 0 {
		return nil
	}

	if err := s.q.InsertDelivery(ctx, tx, webhooksdb.InsertDeliveryParams{
		ID:          delivery.ID,
		Scope:       delivery.Scope,
		EventType:   delivery.EventType.String(),
		Payload:     delivery.Payload,
		OrderingKey: delivery.OrderingKey,
	}); err != nil {
		return op.Error(err, "inserting webhook delivery")
	}

	for _, endpointID := range endpointIDs {
		if err := s.q.InsertDispatch(ctx, tx, webhooksdb.InsertDispatchParams{
			ID:          identifiers.New(),
			DeliveryID:  delivery.ID,
			EndpointID:  endpointID,
			OrderingKey: delivery.OrderingKey,
			CreatedAt:   now.UTC(),
		}); err != nil {
			return op.Error(err, "inserting webhook dispatch")
		}
	}

	return nil
}

// Claim selects a batch, leases it, and reads it back — all in one transaction,
// so two workers cannot lease the same rows.
func (s *SQLStore) Claim(ctx context.Context, now time.Time, limit int, leaseUntil time.Time) ([]ClaimedDispatch, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	var claimed []ClaimedDispatch

	err := s.client.WithTransaction(ctx, func(q database.Tx) error {
		// One clock read, bound twice. The two comparisons are the same moment;
		// they are two arguments because next_attempt is NOT NULL and
		// claimed_until is not, and no analyzer gives one argument two
		// nullabilities — see webhooks/internal/queries.
		at := now.UTC()

		selected, err := s.q.SelectClaimableDispatches(ctx, q, webhooksdb.SelectClaimableDispatchesParams{
			Now:            at,
			LeaseExpiredBy: &at,
			ResultLimit:    int64(limit),
		})
		if err != nil {
			return platformerrors.Wrap(err, "selecting claimable webhook dispatches")
		}

		if len(selected) == 0 {
			return nil
		}

		ids := make([]string, 0, len(selected))
		for i := range selected {
			ids = append(ids, selected[i].ID)
		}

		if _, err = s.q.ClaimDispatches(ctx, q, webhooksdb.ClaimDispatchesParams{
			ClaimedUntil: timeOrNil(leaseUntil),
			IDs:          ids,
		}); err != nil {
			return platformerrors.Wrap(err, "claiming webhook dispatches")
		}

		// A dispatch whose endpoint was disabled or archived between fan-out and
		// claim is filtered out by the fetch join, so it is claimed here and
		// simply not returned. Its lease expires and it is reclaimed, which is a
		// slow no-op rather than a delivery — the alternative, delivering to an
		// endpoint an operator has just disabled, is worse.
		rows, err := s.q.FetchClaimedDispatches(ctx, q, webhooksdb.FetchClaimedDispatchesParams{IDs: ids})
		if err != nil {
			return platformerrors.Wrap(err, "reading claimed webhook dispatches")
		}

		claimed = make([]ClaimedDispatch, 0, len(rows))

		for i := range rows {
			dispatch, convErr := claimedFromRow(&rows[i])
			if convErr != nil {
				return convErr
			}

			claimed = append(claimed, dispatch)
		}

		return nil
	})
	if err != nil {
		return nil, op.Error(err, "claiming webhook dispatches")
	}

	op.SpanOnly(claimedKey, len(claimed))

	return claimed, nil
}

// MarkDelivered retires an accepted dispatch.
func (s *SQLStore) MarkDelivered(ctx context.Context, dispatchID string, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(dispatchIDKey, dispatchID))
	defer op.End()

	// The lease and the last failure are cleared, and the row is kept rather
	// than deleted so the delivery log has something to point at; the reaper
	// removes it once it ages out.
	if _, err := s.q.MarkDispatchDelivered(ctx, s.client.Writer(), webhooksdb.MarkDispatchDeliveredParams{
		DeliveredAt:  timeOrNil(at),
		ClaimedUntil: nil,
		LastError:    nil,
		ID:           dispatchID,
	}); err != nil {
		return op.Error(err, "marking webhook dispatch %q delivered", dispatchID)
	}

	return nil
}

// RecordFailure schedules the retry, or marks the dispatch dead.
func (s *SQLStore) RecordFailure(ctx context.Context, dispatchID string, attempts int, nextAttempt time.Time, lastErr string, dead bool) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(dispatchIDKey, dispatchID),
		observability.WithValue(deadKey, dead),
	)
	defer op.End()

	if attempts < 0 {
		attempts = 0
	}

	op.SpanOnly(attemptsKey, attempts)

	// attempts is written rather than left as the claim incremented it, because
	// not every failure should cost an attempt: a delivery skipped by an open
	// circuit never reached the subscriber, and the worker hands back the count
	// it had before this claim.
	if _, err := s.q.RecordDispatchFailure(ctx, s.client.Writer(), webhooksdb.RecordDispatchFailureParams{
		ClaimedUntil: nil,
		Attempts:     int64(attempts),
		NextAttempt:  nextAttempt.UTC(),
		LastError:    textOrNil(lastErr),
		Dead:         dead,
		ID:           dispatchID,
	}); err != nil {
		return op.Error(err, "recording webhook dispatch %q failure", dispatchID)
	}

	return nil
}

// RecordAttempt appends to the delivery log.
func (s *SQLStore) RecordAttempt(ctx context.Context, attempt *Attempt) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if attempt == nil {
		return op.Error(platformerrors.ErrNilInputParameter, "nil webhook attempt")
	}

	if attempt.ID == "" {
		attempt.ID = identifiers.New()
	}

	op.Set(deliveryIDKey, attempt.DeliveryID).
		Set(endpointIDKey, attempt.EndpointID).
		SpanOnly(statusCodeKey, attempt.StatusCode)

	if err := s.q.InsertAttempt(ctx, s.client.Writer(), webhooksdb.InsertAttemptParams{
		ID:           attempt.ID,
		DeliveryID:   attempt.DeliveryID,
		EndpointID:   attempt.EndpointID,
		AttemptCount: int64(attempt.AttemptCount),
		StatusCode:   int64(attempt.StatusCode),
		Error:        textOrNil(attempt.Error),
		DurationMs:   attempt.Duration.Milliseconds(),
		CreatedAt:    attempt.CreatedAt.UTC(),
	}); err != nil {
		return op.Error(err, "recording webhook delivery attempt")
	}

	return nil
}

// ListAttempts pages one of the scope's deliveries' logs, on the caller's
// executor and in the direction the filter names. A delivery in another scope
// reads as one with no attempts.
func (s *SQLStore) ListAttempts(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, deliveryID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Attempt], error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(deliveryIDKey, deliveryID),
	)
	defer op.End()

	if q == nil {
		return nil, op.Error(ErrNilExecutor, "listing webhook attempts for delivery %q", deliveryID)
	}

	if err := scope.Validate(); err != nil {
		return nil, op.Error(err, "listing webhook attempts for delivery %q", deliveryID)
	}

	filter = pageFilter(filter)

	listRows, err := sortedRows(filter,
		func() ([]webhooksdb.ListAttemptsRow, error) {
			return s.q.ListAttempts(ctx, q, listAttemptsParams(scope, deliveryID, filter))
		},
		func() ([]webhooksdb.ListAttemptsDescendingRow, error) {
			return s.q.ListAttemptsDescending(ctx, q,
				webhooksdb.ListAttemptsDescendingParams(listAttemptsParams(scope, deliveryID, filter)))
		},
		func(r webhooksdb.ListAttemptsDescendingRow) webhooksdb.ListAttemptsRow {
			return webhooksdb.ListAttemptsRow(r)
		})
	if err != nil {
		return nil, op.Error(err, "listing webhook attempts for delivery %q", deliveryID)
	}

	rows := make([]pageRow[Attempt], 0, len(listRows))
	for i := range listRows {
		rows = append(rows, pageRow[Attempt]{
			value:    attemptFromRow(&listRows[i]),
			filtered: listRows[i].FilteredCount,
			total:    listRows[i].TotalCount,
		})
	}

	op.SpanOnly(attemptCountKey, len(rows))

	return filtering.Drain(rows, pageValue, pageCounts,
		func(a *Attempt) string { return a.ID }, filter), nil
}

// Requeue re-drives one delivery to one endpoint.
func (s *SQLStore) Requeue(ctx context.Context, deliveryID, endpointID string, at time.Time) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(deliveryIDKey, deliveryID),
		observability.WithValue(endpointIDKey, endpointID),
	)
	defer op.End()

	// The attempt count is reset rather than continued, which is the whole point
	// of a replay — a dead dispatch is one that has already exhausted its
	// budget, and requeuing it without a reset would have it die again on the
	// next attempt. last_error is kept: the attempts log records what happened,
	// and clearing the reason a replay was needed makes the replay harder to
	// explain afterwards.
	affected, err := s.q.RequeueDispatch(ctx, s.client.Writer(), webhooksdb.RequeueDispatchParams{
		NextAttempt:  at.UTC(),
		ClaimedUntil: nil,
		DeliveredAt:  nil,
		Dead:         false,
		Attempts:     0,
		DeliveryID:   deliveryID,
		EndpointID:   endpointID,
	})
	if err != nil {
		return op.Error(err, "requeuing webhook delivery %q to endpoint %q", deliveryID, endpointID)
	}

	if affected == 0 {
		return op.Error(ErrDeliveryNotFound, "delivery %q to endpoint %q", deliveryID, endpointID)
	}

	return nil
}

// Backlog reads how many dispatches are waiting and how old the oldest is.
//
// An empty queue is no row rather than a row of zeroes — see
// webhooks/internal/queries on why the statement is grouped — so a driver's
// empty result is the answer here rather than a failure: nothing is waiting,
// and there is no oldest row to date.
func (s *SQLStore) Backlog(ctx context.Context) (depth int64, oldest time.Time, err error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	row, err := s.q.DispatchBacklog(ctx, s.client.Reader())

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, time.Time{}, nil
	case err != nil:
		return 0, time.Time{}, op.Error(err, "reading webhook backlog")
	}

	op.SpanOnly(backlogDepthKey, row.Depth)

	return row.Depth, row.Oldest.UTC(), nil
}

// Reap deletes delivered dispatches past the retention window, then the log
// rows and deliveries left without one.
//
// The three DELETEs run in one transaction so a crash between them cannot leave
// a delivery whose dispatches are gone but whose payload lingers forever —
// nothing would ever revisit it.
func (s *SQLStore) Reap(ctx context.Context, before time.Time, limit int) (int64, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	var reaped int64

	err := s.client.WithTransaction(ctx, func(q database.Tx) error {
		affected, err := s.q.ReapDispatches(ctx, q, webhooksdb.ReapDispatchesParams{
			Before:      timeOrNil(before),
			ResultLimit: int64(limit),
		})
		if err != nil {
			return platformerrors.Wrap(err, "reaping webhook dispatches")
		}

		// Nothing aged out, so there is nothing orphaned to collect either.
		if affected == 0 {
			return nil
		}

		reaped = affected

		if _, err = s.q.ReapAttempts(ctx, q, webhooksdb.ReapAttemptsParams{ResultLimit: int64(limit)}); err != nil {
			return platformerrors.Wrap(err, "reaping webhook attempts")
		}

		if _, err = s.q.ReapDeliveries(ctx, q, webhooksdb.ReapDeliveriesParams{ResultLimit: int64(limit)}); err != nil {
			return platformerrors.Wrap(err, "reaping webhook deliveries")
		}

		return nil
	})
	if err != nil {
		return 0, op.Error(err, "reaping webhook dispatches")
	}

	op.SpanOnly(reapedKey, reaped)

	return reaped, nil
}

// checkEndpointScope refuses a save whose ID is already registered to somebody
// else.
//
// It runs inside SaveEndpoint's transaction and against its executor, so the row
// it read cannot be re-scoped between the check and the upsert. Without it the
// upsert's conflict branch would rewrite another scope's URL, headers, and
// signing secret — the endpoint would keep its owner and stop being theirs. The
// conflict target cannot be the thing that stops it: Postgres matches one
// against a unique index the table actually has, and this schema's is the
// primary key.
//
// An ID that exists nowhere is fine: that is the common case, an insert.
func (s *SQLStore) checkEndpointScope(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, endpointID string) error {
	row, err := s.q.GetEndpointScope(ctx, q, webhooksdb.GetEndpointScopeParams{ID: endpointID})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return platformerrors.Wrapf(err, "reading scope of webhook endpoint %q", endpointID)
	case row.Scope != scope:
		return platformerrors.Wrapf(ErrEndpointOutOfScope, "endpoint %q", endpointID)
	default:
		return nil
	}
}

// subscriptionsFor reads one endpoint's live subscription rows, ordered by
// event type so a rendered endpoint lists them the same way twice.
//
// It takes no scope, and it is one of the four statements in this package's
// corpus that reaches a subscription without one. Its caller has already read the endpoint within one —
// this is the second half of a read that was scoped, not a read of its own —
// and the endpoint ID it is given came out of that first query rather than off
// the wire.
func (s *SQLStore) subscriptionsFor(ctx context.Context, q database.SQLQueryExecutor, endpointID string) ([]Subscription, error) {
	rows, err := s.q.ListSubscriptionsForEndpoint(ctx, q,
		webhooksdb.ListSubscriptionsForEndpointParams{EndpointID: endpointID})
	if err != nil {
		return nil, platformerrors.Wrapf(err, "reading subscriptions for webhook endpoint %q", endpointID)
	}

	subscriptions := make([]Subscription, 0, len(rows))
	for i := range rows {
		columns := subscriptionFromEndpointRow(&rows[i])
		subscriptions = append(subscriptions, columns.subscription())
	}

	return subscriptions, nil
}

// The three paged reads' params, each built from one reading of the filter.
//
// They are separate functions returning nominal types rather than one returning
// a shared struct, because sqlc's params types are nominal per statement — see
// webhooks/rows.go. The descending half of each pair is a conversion of the
// ascending one, which is the assertion that the two statements take the same
// arguments.

func listEndpointsParams(scope tenancy.Scope, filter *filtering.QueryFilter) webhooksdb.ListEndpointsParams {
	w := windowFrom(filter)

	return webhooksdb.ListEndpointsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listSubscriptionsParams(scope tenancy.Scope, endpointID string, filter *filtering.QueryFilter) webhooksdb.ListSubscriptionsParams {
	w := windowFrom(filter)

	return webhooksdb.ListSubscriptionsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		EndpointID:      endpointID,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}

func listAttemptsParams(scope tenancy.Scope, deliveryID string, filter *filtering.QueryFilter) webhooksdb.ListAttemptsParams {
	w := windowFrom(filter)

	return webhooksdb.ListAttemptsParams{
		CreatedAfter:    w.createdAfter,
		CreatedBefore:   w.createdBefore,
		UpdatedAfter:    w.updatedAfter,
		UpdatedBefore:   w.updatedBefore,
		IncludeArchived: w.includeArchived,
		DeliveryID:      deliveryID,
		Scope:           scope,
		PageCursor:      w.pageCursor,
		ResultLimit:     w.resultLimit,
	}
}
