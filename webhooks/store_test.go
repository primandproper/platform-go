package webhooks

import (
	"database/sql"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// bogusDialectClient reports a dialect this package cannot emit SQL for.
//
// The unsupported-dialect branch is otherwise unreachable: the dialect comes
// from the client rather than the caller, and every client this module ships
// reports one of the three supported dialects. Only the embedded Dialect is
// consulted before the constructor gives up, so the embedded Client is never
// called.
type bogusDialectClient struct {
	database.Client
}

func (bogusDialectClient) Dialect() dialect.Dialect { return "oracle" }

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("unsupported dialect", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(bogusDialectClient{env.client})
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	// The prefix is interpolated into query text, not bound.
	T.Run("rejects a prefix that is not an identifier", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(env.client, WithTablePrefix("webhook; DROP TABLE users"))
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})
}

// TestSQLStore_SQLite runs the behavioral suite against SQLite. The same suite
// runs against real Postgres and MySQL in containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is the behavioral contract every dialect owes.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("saves and reads back an endpoint", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := &Endpoint{
			ID:            "endpoint-1",
			Scope:         testScope,
			URL:           "https://93.184.216.34/hooks",
			ContentType:   "application/cloudevents+json",
			Secret:        Secret{Current: []byte("current"), Previous: []byte("previous")},
			Headers:       map[string]string{"X-Tenant": "acme"},
			Subscriptions: SubscribeTo(orderCreated, orderUpdated),
		}
		must.NoError(t, saveEndpoint(t, store, testScope, saved))

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		test.EqOp(t, saved.URL, got.URL)
		test.EqOp(t, saved.ContentType, got.ContentType)
		test.Eq(t, []byte("current"), got.Secret.Current)
		test.Eq(t, []byte("previous"), got.Secret.Previous)
		test.Eq(t, map[string]string{"X-Tenant": "acme"}, got.Headers)
		test.Eq(t, []EventType{orderCreated, orderUpdated}, got.EventTypes())
		test.False(t, got.Disabled)
	})

	// "Not rotating" and "rotating to an empty key" must not be confusable in
	// the column, so an absent previous secret round-trips as absent.
	t.Run("an absent previous secret stays absent", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		test.SliceEmpty(t, got.Secret.Previous)
	})

	t.Run("re-saving reconciles the subscription set", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerEndpoint(t, store, "endpoint-1", "order.created", "order.updated")
		registerEndpoint(t, store, "endpoint-1", "order.updated")

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		test.Eq(t, []EventType{orderUpdated}, got.EventTypes())

		// And the endpoint is no longer resolved for the event it dropped.
		test.SliceEmpty(t, endpointsFor(t, store, "order.created"))
		test.SliceLen(t, 1, endpointsFor(t, store, "order.updated"))
	})

	// The reconciliation's other half: a subscription the save keeps is the row
	// it already was, rather than a fresh one wearing the same event type. An ID
	// handed out once has to still name something after the endpoint is edited.
	t.Run("re-saving preserves the identity of a kept subscription", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := registerEndpoint(t, store, "endpoint-1", "order.created", "order.updated")
		must.SliceLen(t, 2, first.Subscriptions)

		kept := subscriptionFor(t, first, orderUpdated)

		registerEndpoint(t, store, "endpoint-1", "order.updated", "order.deleted")

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		test.EqOp(t, kept.ID, subscriptionFor(t, got, orderUpdated).ID)
		test.EqOp(t, kept.CreatedAt, subscriptionFor(t, got, orderUpdated).CreatedAt)
	})

	// A dropped subscription is archived rather than deleted, so re-adding it
	// revives the row an operator may already be holding a link to.
	t.Run("re-saving revives a subscription it had archived", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := registerEndpoint(t, store, "endpoint-1", "order.created")
		original := subscriptionFor(t, first, orderCreated)

		registerEndpoint(t, store, "endpoint-1", "order.updated")
		registerEndpoint(t, store, "endpoint-1", "order.created")

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		revived := subscriptionFor(t, got, orderCreated)
		test.EqOp(t, original.ID, revived.ID)
		test.Nil(t, revived.ArchivedAt)
		test.NotNil(t, revived.LastUpdatedAt)
	})

	t.Run("saving fills in the subscriptions that were written", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := registerEndpoint(t, store, "endpoint-1", "order.created", "order.updated")

		must.SliceLen(t, 2, saved.Subscriptions)

		for i := range saved.Subscriptions {
			test.NotEqOp(t, "", saved.Subscriptions[i].ID)
			test.EqOp(t, "endpoint-1", saved.Subscriptions[i].EndpointID)
			test.False(t, saved.Subscriptions[i].CreatedAt.IsZero())
		}
	})

	// The operation a flat event list has no form for.
	t.Run("archives one subscription without touching the others", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := registerEndpoint(t, store, "endpoint-1", "order.created", "order.updated")
		retired := subscriptionFor(t, saved, orderCreated)

		must.NoError(t, archiveSubscription(t, store, testScope, retired.ID))

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		test.Eq(t, []EventType{orderUpdated}, got.EventTypes())
		test.SliceEmpty(t, endpointsFor(t, store, "order.created"))
		test.SliceLen(t, 1, endpointsFor(t, store, "order.updated"))
	})

	// Archived rather than deleted, so the delivery log still has something to be
	// read against and "when did they stop receiving this" has an answer.
	t.Run("an archived subscription is still readable", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := registerEndpoint(t, store, "endpoint-1", "order.created")
		retired := subscriptionFor(t, saved, orderCreated)

		must.NoError(t, archiveSubscription(t, store, testScope, retired.ID))

		got, err := store.GetSubscription(ctxFor(t), readerOf(t, store), testScope, retired.ID)
		must.NoError(t, err)

		test.EqOp(t, retired.ID, got.ID)
		test.EqOp(t, orderCreated, got.EventType)
		test.True(t, got.Archived())
	})

	t.Run("adds a subscription to an existing endpoint", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerEndpoint(t, store, "endpoint-1", "order.created")

		added, err := addSubscription(t, store, testScope, "endpoint-1", orderUpdated)
		must.NoError(t, err)
		test.NotEqOp(t, "", added.ID)
		test.EqOp(t, orderUpdated, added.EventType)

		test.SliceLen(t, 1, endpointsFor(t, store, "order.updated"))
	})

	// Idempotent on the pair, because the pair is what identifies a subscription.
	t.Run("adding a subscription twice returns the same row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := registerEndpoint(t, store, "endpoint-1", "order.created")

		again, err := addSubscription(t, store, testScope, "endpoint-1", orderCreated)
		must.NoError(t, err)

		test.EqOp(t, subscriptionFor(t, saved, orderCreated).ID, again.ID)

		got, readErr := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, readErr)
		test.SliceLen(t, 1, got.Subscriptions)
	})

	t.Run("re-adding an archived subscription revives it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := registerEndpoint(t, store, "endpoint-1", "order.created", "order.updated")
		retired := subscriptionFor(t, saved, orderCreated)

		must.NoError(t, archiveSubscription(t, store, testScope, retired.ID))

		revived, err := addSubscription(t, store, testScope, "endpoint-1", orderCreated)
		must.NoError(t, err)

		test.EqOp(t, retired.ID, revived.ID)
		test.Nil(t, revived.ArchivedAt)
		test.SliceLen(t, 1, endpointsFor(t, store, "order.created"))
	})

	t.Run("lists live subscriptions", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := registerEndpoint(t, store, "endpoint-1", "order.created", "order.updated")
		must.NoError(t, archiveSubscription(t, store, testScope, subscriptionFor(t, saved, orderCreated).ID))

		listed, err := store.ListSubscriptions(ctxFor(t), readerOf(t, store), testScope, "endpoint-1", filtering.DefaultQueryFilter())
		must.NoError(t, err)

		must.SliceLen(t, 1, listed.Data)
		test.EqOp(t, orderUpdated, listed.Data[0].EventType)
		test.EqOp(t, uint64(1), listed.TotalCount)
	})

	// Every subscription read is reached through its endpoint, so a neighboring
	// tenant's subscription is not readable, listable, or archivable from here.
	t.Run("does not reach another scope's subscriptions", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		theirs := registerScopedEndpoint(t, store, otherScope, "endpoint-1", "order.created")
		subscription := subscriptionFor(t, theirs, orderCreated)

		_, err := store.GetSubscription(ctxFor(t), readerOf(t, store), testScope, subscription.ID)
		test.ErrorIs(t, err, sql.ErrNoRows)

		listed, listErr := store.ListSubscriptions(ctxFor(t), readerOf(t, store), testScope, "endpoint-1", nil)
		must.NoError(t, listErr)
		test.SliceEmpty(t, listed.Data)

		_, addErr := addSubscription(t, store, testScope, "endpoint-1", orderUpdated)
		test.ErrorIs(t, addErr, sql.ErrNoRows)

		// The archive matches nothing, and the owner's row is untouched.
		must.NoError(t, archiveSubscription(t, store, testScope, subscription.ID))

		still, readErr := store.GetSubscription(ctxFor(t), readerOf(t, store), otherScope, subscription.ID)
		must.NoError(t, readErr)
		test.False(t, still.Archived())
	})

	// Every subscription entry point refuses the zero scope rather than widening.
	t.Run("subscription reads refuse a scope that names nobody", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := addSubscription(t, store, tenancy.Scope{}, "endpoint-1", orderCreated)
		test.ErrorIs(t, err, ErrNoScope)

		_, err = store.GetSubscription(ctxFor(t), readerOf(t, store), tenancy.Scope{}, "subscription-1")
		test.ErrorIs(t, err, ErrNoScope)

		_, err = store.ListSubscriptions(ctxFor(t), readerOf(t, store), tenancy.Scope{}, "endpoint-1", nil)
		test.ErrorIs(t, err, ErrNoScope)

		test.ErrorIs(t, archiveSubscription(t, store, tenancy.Scope{}, "subscription-1"), ErrNoScope)
	})

	t.Run("refuses to subscribe to an empty event type", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerEndpoint(t, store, "endpoint-1", "order.created")

		_, err := addSubscription(t, store, testScope, "endpoint-1", "")
		test.ErrorIs(t, err, ErrEmptyEventType)
	})

	// The endpoint metadata the consumer's own resource model carried because
	// Endpoint had nowhere to put it.
	t.Run("round-trips an endpoint's name, creator, and timestamps", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := &Endpoint{
			ID:            "endpoint-1",
			Scope:         testScope,
			CreatedBy:     tenancy.Of("user_7"),
			Name:          "billing exports",
			URL:           "https://93.184.216.34/hooks",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("current")},
			Subscriptions: SubscribeTo(orderCreated),
		}
		must.NoError(t, saveEndpoint(t, store, testScope, saved))

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		test.EqOp(t, "billing exports", got.Name)
		test.EqOp(t, tenancy.Of("user_7"), got.CreatedBy)
		test.False(t, got.CreatedAt.IsZero())
		test.Nil(t, got.LastUpdatedAt)
		test.Nil(t, got.ArchivedAt)
		test.False(t, got.Archived())

		// Archival is representable rather than merely happening in the column.
		must.NoError(t, archiveEndpoint(t, store, testScope, "endpoint-1"))

		archived, readErr := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, readErr)
		test.True(t, archived.Archived())
	})

	// An application that does not attribute endpoints to a person leaves
	// CreatedBy unset, and the column has to tell that apart from the global
	// scope — whose stored identifier is the empty string.
	t.Run("an unset creator reads back unset rather than global", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerEndpoint(t, store, "endpoint-1", "order.created")

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		test.ErrorIs(t, got.CreatedBy.Validate(), ErrNoScope)
		test.False(t, got.CreatedBy.IsGlobal())
	})

	t.Run("the global scope is a creator like any other", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := &Endpoint{
			ID:            "endpoint-1",
			Scope:         testScope,
			CreatedBy:     tenancy.Global(),
			URL:           "https://93.184.216.34/hooks",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("current")},
			Subscriptions: SubscribeTo(orderCreated),
		}
		must.NoError(t, saveEndpoint(t, store, testScope, saved))

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)

		test.True(t, got.CreatedBy.IsGlobal())
	})

	// A claim reads the endpoint through the same projection, so the metadata has
	// to survive the join too.
	t.Run("a claimed dispatch carries the endpoint's metadata", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		saved := &Endpoint{
			ID:            "endpoint-1",
			Scope:         testScope,
			CreatedBy:     tenancy.Of("user_7"),
			Name:          "billing exports",
			URL:           "https://93.184.216.34/hooks",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("current")},
			Subscriptions: SubscribeTo(orderCreated),
		}
		must.NoError(t, saveEndpoint(t, store, testScope, saved))

		dispatchTo(t, store, &Delivery{EventType: orderCreated, Payload: testBody}, baseTime, "endpoint-1")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)

		test.EqOp(t, "billing exports", claimed[0].Endpoint.Name)
		test.EqOp(t, tenancy.Of("user_7"), claimed[0].Endpoint.CreatedBy)
		test.False(t, claimed[0].Endpoint.CreatedAt.IsZero())
	})

	t.Run("resolves the fan-out set for an event", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerEndpoint(t, store, "endpoint-1", "order.created")
		registerEndpoint(t, store, "endpoint-2", "order.created", "order.updated")
		registerEndpoint(t, store, "endpoint-3", "order.updated")

		test.Eq(t, []string{"endpoint-1", "endpoint-2"}, idsOf(endpointsFor(t, store, "order.created")))
		test.Eq(t, []string{"endpoint-2", "endpoint-3"}, idsOf(endpointsFor(t, store, "order.updated")))
		test.SliceEmpty(t, endpointsFor(t, store, "order.deleted"))
	})

	// Excluded at fan-out rather than at delivery: a dispatch row for a disabled
	// endpoint would sit permanently undeliverable in the backlog.
	t.Run("excludes disabled and archived endpoints from fan-out", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerEndpoint(t, store, "live", "order.created")

		disabled := registerEndpoint(t, store, "disabled", "order.created")
		disabled.Disabled = true
		must.NoError(t, saveEndpoint(t, store, testScope, disabled))

		registerEndpoint(t, store, "archived", "order.created")
		must.NoError(t, archiveEndpoint(t, store, testScope, "archived"))

		test.Eq(t, []string{"live"}, idsOf(endpointsFor(t, store, "order.created")))
	})

	t.Run("lists live endpoints", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerEndpoint(t, store, "endpoint-1", "order.created")
		registerEndpoint(t, store, "endpoint-2", "order.created")
		registerEndpoint(t, store, "endpoint-3", "order.created")
		must.NoError(t, archiveEndpoint(t, store, testScope, "endpoint-2"))

		listed, err := store.ListEndpoints(ctxFor(t), readerOf(t, store), testScope, filtering.DefaultQueryFilter())
		must.NoError(t, err)

		test.SliceLen(t, 2, listed.Data)
		test.EqOp(t, uint64(2), listed.TotalCount)

		// Subscriptions come back with each row.
		for _, endpoint := range listed.Data {
			test.Eq(t, []EventType{orderCreated}, endpoint.EventTypes())
		}
	})

	// The failure this dimension exists to prevent: two accounts subscribed to
	// the same event type, and neither one's endpoint in the other's fan-out set.
	t.Run("resolves the fan-out set within one scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerScopedEndpoint(t, store, testScope, "endpoint-1", "order.created")
		registerScopedEndpoint(t, store, otherScope, "endpoint-2", "order.created")
		registerScopedEndpoint(t, store, tenancy.Global(), "endpoint-3", "order.created")

		test.Eq(t, []string{"endpoint-1"},
			idsOf(scopedEndpointsFor(t, store, testScope, "order.created")))
		test.Eq(t, []string{"endpoint-2"},
			idsOf(scopedEndpointsFor(t, store, otherScope, "order.created")))

		// The global scope is a scope like any other: it matches only itself, in
		// both directions.
		test.Eq(t, []string{"endpoint-3"},
			idsOf(scopedEndpointsFor(t, store, tenancy.Global(), "order.created")))
	})

	t.Run("reads an endpoint in another scope as absent", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerScopedEndpoint(t, store, otherScope, "endpoint-1", "order.created")

		// Absent rather than forbidden: it is not in this registry, and saying
		// so keeps the read from being an oracle for another scope's IDs.
		_, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		test.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("lists and counts only its own scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerScopedEndpoint(t, store, testScope, "endpoint-1", "order.created")
		registerScopedEndpoint(t, store, otherScope, "endpoint-2", "order.created")
		registerScopedEndpoint(t, store, otherScope, "endpoint-3", "order.created")

		listed, err := store.ListEndpoints(ctxFor(t), readerOf(t, store), testScope, filtering.DefaultQueryFilter())
		must.NoError(t, err)

		must.SliceLen(t, 1, listed.Data)
		test.EqOp(t, "endpoint-1", listed.Data[0].ID)

		// The total is the scope's, not the table's — otherwise a page of one
		// reports itself as one of three.
		test.EqOp(t, uint64(1), listed.TotalCount)
	})

	t.Run("an endpoint reads back the scope it was saved in", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerScopedEndpoint(t, store, otherScope, "endpoint-1", "order.created")

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), otherScope, "endpoint-1")
		must.NoError(t, err)
		test.EqOp(t, otherScope, got.Scope)
	})

	t.Run("does not archive an endpoint in another scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerScopedEndpoint(t, store, otherScope, "endpoint-1", "order.created")

		must.NoError(t, archiveEndpoint(t, store, testScope, "endpoint-1"))

		// Still live, and still delivering, for the scope that owns it.
		test.SliceLen(t, 1, scopedEndpointsFor(t, store, otherScope, "order.created"))
	})

	// The cross-tenant write: without the scope check, an upsert on a known ID
	// would rewrite another subscriber's URL and signing secret.
	t.Run("refuses to save over an endpoint in another scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerScopedEndpoint(t, store, otherScope, "endpoint-1", "order.created")

		err := saveEndpoint(t, store, testScope, &Endpoint{
			ID:            "endpoint-1",
			URL:           "https://93.184.216.34/attacker",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("attacker")},
			Subscriptions: SubscribeTo(orderCreated),
		})
		test.ErrorIs(t, err, ErrEndpointOutOfScope)

		// And the owner's row is untouched.
		got, readErr := store.GetEndpoint(ctxFor(t), readerOf(t, store), otherScope, "endpoint-1")
		must.NoError(t, readErr)
		test.EqOp(t, "https://93.184.216.34/hooks/endpoint-1", got.URL)
		test.Eq(t, []byte("secret-endpoint-1"), got.Secret.Current)
	})

	t.Run("reads another scope's delivery log as empty", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerScopedEndpoint(t, store, otherScope, "endpoint-1", "order.created")

		delivery := dispatchTo(t, store,
			&Delivery{Scope: otherScope, EventType: "order.created", Payload: testBody},
			baseTime, "endpoint-1")

		must.NoError(t, store.RecordAttempt(ctxFor(t), &Attempt{
			DeliveryID: delivery.ID, EndpointID: "endpoint-1",
			AttemptCount: 1, StatusCode: 200, CreatedAt: baseTime,
		}))

		listed, err := store.ListAttempts(ctxFor(t), readerOf(t, store), testScope, delivery.ID, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		test.SliceEmpty(t, listed.Data)
		test.EqOp(t, uint64(0), listed.TotalCount)

		// The scope that owns it sees the whole log.
		owned, err := store.ListAttempts(ctxFor(t), readerOf(t, store), otherScope, delivery.ID, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		test.SliceLen(t, 1, owned.Data)
	})

	// The worker claims across every scope — one worker drains one queue — and
	// what it claims says which scope it came from, so a failure is attributable.
	t.Run("a claim carries the delivery's scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerScopedEndpoint(t, store, testScope, "endpoint-1", "order.created")
		registerScopedEndpoint(t, store, otherScope, "endpoint-2", "order.created")

		dispatchTo(t, store,
			&Delivery{Scope: testScope, EventType: "order.created", Payload: testBody},
			baseTime, "endpoint-1")
		dispatchTo(t, store,
			&Delivery{Scope: otherScope, EventType: "order.created", Payload: testBody},
			baseTime, "endpoint-2")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 2, claimed)

		byEndpoint := map[string]tenancy.Scope{}
		for i := range claimed {
			byEndpoint[claimed[i].EndpointID] = claimed[i].Scope
			test.EqOp(t, claimed[i].Scope, claimed[i].Endpoint.Scope)
		}

		test.EqOp(t, testScope, byEndpoint["endpoint-1"])
		test.EqOp(t, otherScope, byEndpoint["endpoint-2"])
	})

	// A scope that names nobody is refused rather than read as "every scope".
	// The zero value reaching a query is the mistake this whole dimension is
	// against, so every entry point that takes one says so.
	t.Run("refuses a scope that names nobody", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		var unset tenancy.Scope

		_, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), unset, "endpoint-1")
		test.ErrorIs(t, err, ErrNoScope)

		_, err = store.ListEndpoints(ctxFor(t), readerOf(t, store), unset, filtering.DefaultQueryFilter())
		test.ErrorIs(t, err, ErrNoScope)

		_, err = store.ListAttempts(ctxFor(t), readerOf(t, store), unset, "delivery-1", filtering.DefaultQueryFilter())
		test.ErrorIs(t, err, ErrNoScope)

		test.ErrorIs(t, archiveEndpoint(t, store, unset, "endpoint-1"), ErrNoScope)
		test.ErrorIs(t, saveEndpoint(t, store, unset, &Endpoint{ID: "endpoint-2"}), ErrNoScope)

		must.NoError(t, env.client.WithTransaction(ctxFor(t), func(q database.Tx) error {
			_, forEventErr := store.EndpointsForEvent(ctxFor(t), q, unset, "order.created")
			test.ErrorIs(t, forEventErr, ErrNoScope)

			test.ErrorIs(t,
				store.Enqueue(ctxFor(t), q, &Delivery{ID: "d", EventType: "order.created", Payload: testBody},
					[]string{"endpoint-1"}, baseTime),
				ErrNoScope,
			)

			return nil
		}))

		// And the endpoint that was registered is still reachable in its own
		// scope, so none of the above half-applied.
		test.SliceLen(t, 1, endpointsFor(t, store, "order.created"))
	})

	t.Run("fans a delivery out into one dispatch per endpoint", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		registerEndpoint(t, store, "endpoint-1", "order.created")
		registerEndpoint(t, store, "endpoint-2", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody},
			baseTime, "endpoint-1", "endpoint-2")

		claimed := claimAll(t, store, baseTime)

		must.SliceLen(t, 2, claimed)
		test.Eq(t, []string{"endpoint-1", "endpoint-2"}, endpointIDsOf(claimed))

		// The payload and the endpoint's secrets arrive with the claim, so the
		// worker makes one round trip per batch rather than one per dispatch.
		for i := range claimed {
			test.Eq(t, testBody, claimed[i].Payload)
			test.EqOp(t, "order.created", claimed[i].EventType)
			must.NotNil(t, claimed[i].Endpoint)
			test.SliceNotEmpty(t, claimed[i].Endpoint.Secret.Current)
			test.EqOp(t, 1, claimed[i].Attempts)
		}
	})

	t.Run("enqueuing to nobody writes nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, env.client.WithTransaction(ctxFor(t), func(q database.Tx) error {
			return store.Enqueue(ctxFor(t), q, &Delivery{ID: "d", Scope: testScope, EventType: "order.created", Payload: testBody}, nil, baseTime)
		}))

		test.SliceEmpty(t, claimAll(t, store, baseTime))
	})

	// The ordering guarantee. Two deliveries sharing a key reach an endpoint
	// oldest-first, and the second is not claimable until the first lands.
	t.Run("holds back a keyed dispatch behind an earlier one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		first := dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody, OrderingKey: "order-7"},
			baseTime, "endpoint-1")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody, OrderingKey: "order-7"},
			baseTime.Add(time.Second), "endpoint-1")

		claimed := claimAll(t, store, baseTime.Add(time.Minute))
		must.SliceLen(t, 1, claimed)
		test.EqOp(t, first.ID, claimed[0].DeliveryID)

		// Still blocked while the first is in flight.
		test.SliceEmpty(t, claimAll(t, store, baseTime.Add(time.Minute)))

		must.NoError(t, store.MarkDelivered(ctxFor(t), claimed[0].ID, baseTime.Add(time.Minute)))

		next := claimAll(t, store, baseTime.Add(2*time.Minute))
		must.SliceLen(t, 1, next)
		test.NotEqOp(t, first.ID, next[0].DeliveryID)
	})

	// Ordering is per (endpoint, key), not per key. A subscriber that is stuck
	// must delay only its own queue — otherwise a dead endpoint stalls healthy
	// ones, which is the failure circuit breaking exists to prevent.
	t.Run("one endpoint's backlog does not block another's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "slow", "order.created")
		registerEndpoint(t, store, "fast", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody, OrderingKey: "order-7"},
			baseTime, "slow", "fast")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody, OrderingKey: "order-7"},
			baseTime.Add(time.Second), "slow", "fast")

		first := claimAll(t, store, baseTime.Add(time.Minute))
		must.SliceLen(t, 2, first)

		// Only "fast" completes its first dispatch.
		for i := range first {
			if first[i].EndpointID == "fast" {
				must.NoError(t, store.MarkDelivered(ctxFor(t), first[i].ID, baseTime.Add(time.Minute)))
			}
		}

		// "fast" advances to its second delivery; "slow" is still held.
		//
		// Claimed inside "slow"'s lease (claimAll leases for a minute from the
		// claim instant), so what holds its second dispatch back is the ordering
		// predicate rather than an expired lease letting the first be reclaimed.
		second := claimAll(t, store, baseTime.Add(90*time.Second))
		must.SliceLen(t, 1, second)
		test.EqOp(t, "fast", second[0].EndpointID)
	})

	t.Run("unkeyed dispatches claim freely", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		for i := range 3 {
			dispatchTo(t, store,
				&Delivery{EventType: "order.created", Payload: testBody},
				baseTime.Add(time.Duration(i)*time.Second), "endpoint-1")
		}

		test.SliceLen(t, 3, claimAll(t, store, baseTime.Add(time.Minute)))
	})

	t.Run("a lease keeps a second claim away until it expires", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		claimed, err := store.Claim(ctxFor(t), baseTime, 10, baseTime.Add(30*time.Second))
		must.NoError(t, err)
		must.SliceLen(t, 1, claimed)

		// Inside the lease.
		again, err := store.Claim(ctxFor(t), baseTime.Add(10*time.Second), 10, baseTime.Add(time.Minute))
		must.NoError(t, err)
		test.SliceEmpty(t, again)

		// Past it.
		reclaimed, err := store.Claim(ctxFor(t), baseTime.Add(31*time.Second), 10, baseTime.Add(2*time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 1, reclaimed)

		// The attempt count survived the reclaim, so a dispatch that reliably
		// kills its worker eventually dies rather than looping forever.
		test.EqOp(t, 2, reclaimed[0].Attempts)
	})

	t.Run("respects the batch limit", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		for i := range 5 {
			dispatchTo(t, store,
				&Delivery{EventType: "order.created", Payload: testBody},
				baseTime.Add(time.Duration(i)*time.Second), "endpoint-1")
		}

		claimed, err := store.Claim(ctxFor(t), baseTime.Add(time.Minute), 2, baseTime.Add(2*time.Minute))
		must.NoError(t, err)
		test.SliceLen(t, 2, claimed)
	})

	t.Run("schedules a retry and then goes dead", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)

		// Retry scheduled into the future: not claimable yet, claimable after.
		must.NoError(t, store.RecordFailure(ctxFor(t), claimed[0].ID, claimed[0].Attempts, baseTime.Add(5*time.Minute), "boom", false))

		test.SliceEmpty(t, claimAll(t, store, baseTime.Add(time.Minute)))
		test.SliceLen(t, 1, claimAll(t, store, baseTime.Add(6*time.Minute)))

		// Dead is terminal. Native boolean handling differs per dialect; this is
		// the assertion that catches a TINYINT(1) mismatch.
		must.NoError(t, store.RecordFailure(ctxFor(t), claimed[0].ID, claimed[0].Attempts, baseTime, "boom", true))
		test.SliceEmpty(t, claimAll(t, store, baseTime.Add(time.Hour)))
	})

	// RecordFailure writes the attempt count it is given rather than leaving the
	// one Claim incremented, so a caller can decline to charge an attempt for a
	// failure the subscriber never saw — an open circuit being the case that
	// matters. Without this an endpoint down for an hour silently drains the
	// budget of everything queued behind it.
	t.Run("persists the attempt count it is given", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)
		must.EqOp(t, 1, claimed[0].Attempts)

		// Hand back the count from before this claim incremented it.
		must.NoError(t, store.RecordFailure(ctxFor(t), claimed[0].ID, 0, baseTime, "circuit open", false))

		next := claimAll(t, store, baseTime.Add(time.Minute))
		must.SliceLen(t, 1, next)

		// Back to 1 rather than 2: the skipped delivery cost nothing.
		test.EqOp(t, 1, next[0].Attempts)
	})

	// A dead dispatch must not block the ordering key behind it forever.
	t.Run("a dead dispatch releases its ordering key", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody, OrderingKey: "order-7"},
			baseTime, "endpoint-1")
		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody, OrderingKey: "order-7"},
			baseTime.Add(time.Second), "endpoint-1")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)

		must.NoError(t, store.RecordFailure(ctxFor(t), claimed[0].ID, claimed[0].Attempts, baseTime, "poison", true))

		next := claimAll(t, store, baseTime.Add(time.Minute))
		test.SliceLen(t, 1, next)
	})

	t.Run("records and lists attempts", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		delivery := dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		must.NoError(t, store.RecordAttempt(ctxFor(t), &Attempt{
			DeliveryID: delivery.ID, EndpointID: "endpoint-1",
			AttemptCount: 1, StatusCode: 500, Error: "boom",
			Duration: 250 * time.Millisecond, CreatedAt: baseTime,
		}))
		must.NoError(t, store.RecordAttempt(ctxFor(t), &Attempt{
			DeliveryID: delivery.ID, EndpointID: "endpoint-1",
			AttemptCount: 2, StatusCode: 200,
			Duration: 120 * time.Millisecond, CreatedAt: baseTime.Add(time.Minute),
		}))

		listed, err := store.ListAttempts(ctxFor(t), readerOf(t, store), testScope, delivery.ID, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		must.SliceLen(t, 2, listed.Data)
		test.EqOp(t, uint64(2), listed.TotalCount)

		first, second := listed.Data[0], listed.Data[1]

		test.EqOp(t, 1, first.AttemptCount)
		test.EqOp(t, 500, first.StatusCode)
		test.EqOp(t, "boom", first.Error)
		test.EqOp(t, 250*time.Millisecond, first.Duration)
		test.False(t, first.Succeeded())
		test.EqOp(t, baseTime.Unix(), first.CreatedAt.Unix())

		test.EqOp(t, 2, second.AttemptCount)
		test.True(t, second.Succeeded())

		// An ID is generated when the caller does not supply one.
		test.NotEqOp(t, "", first.ID)
	})

	t.Run("replays a dead dispatch with a fresh budget", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		delivery := dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)
		must.NoError(t, store.RecordFailure(ctxFor(t), claimed[0].ID, claimed[0].Attempts, baseTime, "gave up", true))
		must.SliceEmpty(t, claimAll(t, store, baseTime.Add(time.Hour)))

		must.NoError(t, store.Requeue(ctxFor(t), delivery.ID, "endpoint-1", baseTime.Add(time.Hour)))

		replayed := claimAll(t, store, baseTime.Add(2*time.Hour))
		must.SliceLen(t, 1, replayed)

		// Reset, not continued — a dead dispatch replayed without a reset would
		// die again on its next attempt.
		test.EqOp(t, 1, replayed[0].Attempts)
	})

	t.Run("replays a delivered dispatch", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		delivery := dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)
		must.NoError(t, store.MarkDelivered(ctxFor(t), claimed[0].ID, baseTime))

		must.NoError(t, store.Requeue(ctxFor(t), delivery.ID, "endpoint-1", baseTime.Add(time.Hour)))
		test.SliceLen(t, 1, claimAll(t, store, baseTime.Add(2*time.Hour)))
	})

	t.Run("replaying an unknown pair reports it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		err := store.Requeue(ctxFor(t), "no-such-delivery", "endpoint-1", baseTime)
		test.ErrorIs(t, err, ErrDeliveryNotFound)
	})

	t.Run("reports the backlog", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		depth, oldest, err := store.Backlog(ctxFor(t))
		must.NoError(t, err)
		test.EqOp(t, int64(0), depth)
		test.True(t, oldest.IsZero())

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")
		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime.Add(time.Hour), "endpoint-1")

		depth, oldest, err = store.Backlog(ctxFor(t))
		must.NoError(t, err)
		test.EqOp(t, int64(2), depth)
		test.EqOp(t, baseTime.Unix(), oldest.Unix())
	})

	// A permanently broken subscriber must not read as a permanently growing
	// backlog.
	t.Run("the backlog excludes dead dispatches", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)
		must.NoError(t, store.RecordFailure(ctxFor(t), claimed[0].ID, claimed[0].Attempts, baseTime, "poison", true))

		depth, _, err := store.Backlog(ctxFor(t))
		must.NoError(t, err)
		test.EqOp(t, int64(0), depth)
	})

	t.Run("reaps delivered dispatches and their history", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		delivery := dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)

		must.NoError(t, store.RecordAttempt(ctxFor(t), &Attempt{
			DeliveryID: delivery.ID, EndpointID: "endpoint-1",
			AttemptCount: 1, StatusCode: 200, CreatedAt: baseTime,
		}))
		must.NoError(t, store.MarkDelivered(ctxFor(t), claimed[0].ID, baseTime))

		// Inside the retention window, nothing goes.
		reaped, err := store.Reap(ctxFor(t), baseTime.Add(-time.Hour), 100)
		must.NoError(t, err)
		test.EqOp(t, int64(0), reaped)

		reaped, err = store.Reap(ctxFor(t), baseTime.Add(time.Hour), 100)
		must.NoError(t, err)
		test.EqOp(t, int64(1), reaped)

		// The log goes with it, so a reaped delivery leaves nothing behind.
		listed, err := store.ListAttempts(ctxFor(t), readerOf(t, store), testScope, delivery.ID, filtering.DefaultQueryFilter())
		must.NoError(t, err)
		test.SliceEmpty(t, listed.Data)
	})

	t.Run("the reaper leaves undelivered dispatches alone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		reaped, err := store.Reap(ctxFor(t), baseTime.Add(time.Hour), 100)
		must.NoError(t, err)
		test.EqOp(t, int64(0), reaped)

		test.SliceLen(t, 1, claimAll(t, store, baseTime.Add(time.Hour)))
	})

	// The endpoint is read at claim time, not captured at dispatch time, so a
	// secret rotated in between signs under the current key.
	t.Run("a claim sees the endpoint's current secret", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		endpoint := registerEndpoint(t, store, "endpoint-1", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		endpoint.Secret = Secret{Current: []byte("rotated"), Previous: []byte("secret-endpoint-1")}
		must.NoError(t, saveEndpoint(t, store, testScope, endpoint))

		claimed := claimAll(t, store, baseTime)
		must.SliceLen(t, 1, claimed)
		test.Eq(t, []byte("rotated"), claimed[0].Endpoint.Secret.Current)
		test.Eq(t, []byte("secret-endpoint-1"), claimed[0].Endpoint.Secret.Previous)
	})

	// An endpoint disabled between fan-out and claim is not delivered to.
	t.Run("a claim skips a since-disabled endpoint", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		endpoint := registerEndpoint(t, store, "endpoint-1", "order.created")

		dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		endpoint.Disabled = true
		must.NoError(t, saveEndpoint(t, store, testScope, endpoint))

		test.SliceEmpty(t, claimAll(t, store, baseTime))
	})

	// Cursor pagination, for both paged reads. The cursor branch is what a
	// second page actually exercises, and a first-page-only test never renders
	// it. It owes every dialect: it is the one query shape whose placeholder
	// index is computed from the argument count rather than written literally,
	// and SQLite and MySQL both use positional '?', where a numbering mistake
	// is invisible. Only Postgres's numbered $N can catch it.
	t.Run("pages endpoints with a cursor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for _, id := range []string{"endpoint-1", "endpoint-2", "endpoint-3"} {
			registerEndpoint(t, store, id, "order.created")
		}

		filter := filtering.DefaultQueryFilter()
		filter.MaxResponseSize = new(uint16(2))

		first, err := store.ListEndpoints(ctxFor(t), readerOf(t, store), testScope, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, first.Data)
		test.EqOp(t, "endpoint-2", first.Cursor)

		filter.Cursor = &first.Cursor

		second, err := store.ListEndpoints(ctxFor(t), readerOf(t, store), testScope, filter)
		must.NoError(t, err)
		must.SliceLen(t, 1, second.Data)
		test.EqOp(t, "endpoint-3", second.Data[0].ID)

		// The total is the whole set, not the page.
		test.EqOp(t, uint64(3), second.TotalCount)
	})

	t.Run("pages attempts with a cursor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		delivery := dispatchTo(t, store,
			&Delivery{EventType: "order.created", Payload: testBody}, baseTime, "endpoint-1")

		for i := range 3 {
			must.NoError(t, store.RecordAttempt(ctxFor(t), &Attempt{
				DeliveryID: delivery.ID, EndpointID: "endpoint-1",
				AttemptCount: i + 1, StatusCode: 500,
				CreatedAt: baseTime.Add(time.Duration(i) * time.Minute),
			}))
		}

		filter := filtering.DefaultQueryFilter()
		filter.MaxResponseSize = new(uint16(2))

		first, err := store.ListAttempts(ctxFor(t), readerOf(t, store), testScope, delivery.ID, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, first.Data)

		filter.Cursor = &first.Cursor

		second, err := store.ListAttempts(ctxFor(t), readerOf(t, store), testScope, delivery.ID, filter)
		must.NoError(t, err)
		test.SliceLen(t, 1, second.Data)
		test.EqOp(t, uint64(3), second.TotalCount)
	})

	// A nil filter is the common call and must not page to zero rows.
	t.Run("a nil filter uses the defaults", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		listed, err := store.ListEndpoints(ctxFor(t), readerOf(t, store), testScope, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, listed.Data)
	})

	// Every consumer method runs on an executor its caller supplies, so there is
	// none of them that can fall back to a handle of the store's own. The
	// machinery is deliberately absent from this list: it takes no executor to be
	// nil.
	t.Run("guards against a nil executor", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		filter := filtering.DefaultQueryFilter()

		_, err := store.EndpointsForEvent(ctxFor(t), nil, testScope, "order.created")
		test.ErrorIs(t, err, ErrNilExecutor)

		test.ErrorIs(t,
			store.Enqueue(ctxFor(t), nil, &Delivery{ID: "d", Scope: testScope}, []string{"e"}, baseTime),
			ErrNilExecutor,
		)

		test.ErrorIs(t, store.SaveEndpoint(ctxFor(t), nil, testScope, &Endpoint{ID: "e"}), ErrNilExecutor)
		test.ErrorIs(t, store.ArchiveEndpoint(ctxFor(t), nil, testScope, "e"), ErrNilExecutor)
		test.ErrorIs(t, store.ArchiveSubscription(ctxFor(t), nil, testScope, "s"), ErrNilExecutor)

		_, err = store.AddSubscription(ctxFor(t), nil, testScope, "e", orderCreated)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetEndpoint(ctxFor(t), nil, testScope, "e")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListEndpoints(ctxFor(t), nil, testScope, filter)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetSubscription(ctxFor(t), nil, testScope, "s")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListSubscriptions(ctxFor(t), nil, testScope, "e", filter)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListAttempts(ctxFor(t), nil, testScope, "d", filter)
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	// The property the read shape exists for, and the reason a read takes the
	// wider database.SQLQueryExecutor rather than a reader: a caller inside a
	// transaction reads that transaction. Pinned on all four reads at once,
	// because a read narrowed back to Reader() would pass every other case in
	// this suite.
	t.Run("a read on the caller's transaction sees that transaction's writes", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, inTx(t, store, func(tx database.Tx) error {
			endpoint := &Endpoint{
				ID:            "endpoint-1",
				URL:           "https://93.184.216.34/hooks",
				ContentType:   DefaultContentType,
				Secret:        Secret{Current: []byte("secret")},
				Subscriptions: SubscribeTo(orderCreated),
			}
			if err := store.SaveEndpoint(ctxFor(t), tx, testScope, endpoint); err != nil {
				return err
			}

			got, err := store.GetEndpoint(ctxFor(t), tx, testScope, "endpoint-1")
			if err != nil {
				return err
			}

			test.EqOp(t, "endpoint-1", got.ID)

			listed, err := store.ListEndpoints(ctxFor(t), tx, testScope, filtering.DefaultQueryFilter())
			if err != nil {
				return err
			}

			test.SliceLen(t, 1, listed.Data)

			subscription := subscriptionFor(t, endpoint, orderCreated)

			read, err := store.GetSubscription(ctxFor(t), tx, testScope, subscription.ID)
			if err != nil {
				return err
			}

			test.EqOp(t, subscription.ID, read.ID)

			subscriptions, err := store.ListSubscriptions(ctxFor(t), tx, testScope, "endpoint-1",
				filtering.DefaultQueryFilter())
			if err != nil {
				return err
			}

			test.SliceLen(t, 1, subscriptions.Data)

			return nil
		}))

		// And it is still there once the transaction committed, which is what
		// distinguishes the read above from a read of nothing.
		_, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		test.NoError(t, err)
	})

	// The other half, and the whole case for the port: a write handed the
	// caller's transaction unwinds with it. A store that opened its own would
	// have committed the endpoint while the audit entry beside it was refused.
	t.Run("a write the caller unwinds leaves nothing behind", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		registerEndpoint(t, store, "endpoint-1", "order.created")

		refused := platformerrors.New("the caller changed its mind")

		err := inTx(t, store, func(tx database.Tx) error {
			if saveErr := store.SaveEndpoint(ctxFor(t), tx, testScope, &Endpoint{
				ID:            "endpoint-2",
				URL:           "https://93.184.216.34/hooks/2",
				ContentType:   DefaultContentType,
				Secret:        Secret{Current: []byte("secret")},
				Subscriptions: SubscribeTo(orderCreated),
			}); saveErr != nil {
				return saveErr
			}

			if _, addErr := store.AddSubscription(ctxFor(t), tx, testScope, "endpoint-1", orderUpdated); addErr != nil {
				return addErr
			}

			if archiveErr := store.ArchiveEndpoint(ctxFor(t), tx, testScope, "endpoint-1"); archiveErr != nil {
				return archiveErr
			}

			return refused
		})
		test.ErrorIs(t, err, refused)

		_, readErr := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-2")
		test.Error(t, readErr)

		survivor, readErr := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, readErr)
		test.False(t, survivor.Archived())
		test.Eq(t, []EventType{orderCreated}, survivor.EventTypes())
	})

	// The scope is the argument, and an endpoint carrying one that disagrees is a
	// caller holding somebody else's row. Refused rather than corrected, and
	// before any statement runs.
	t.Run("refuses an endpoint naming a different scope than the write", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		err := saveEndpoint(t, store, testScope, &Endpoint{
			ID:            "endpoint-1",
			Scope:         otherScope,
			URL:           "https://93.184.216.34/hooks",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("secret")},
			Subscriptions: SubscribeTo(orderCreated),
		})
		test.ErrorIs(t, err, ErrScopeMismatch)

		_, readErr := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		test.Error(t, readErr)
	})

	// The ordinary case: the caller names the scope and the endpoint takes it,
	// so what the row holds is what the statement bound.
	t.Run("an endpoint naming no scope adopts the write's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		endpoint := &Endpoint{
			ID:            "endpoint-1",
			URL:           "https://93.184.216.34/hooks",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("secret")},
			Subscriptions: SubscribeTo(orderCreated),
		}
		must.NoError(t, saveEndpoint(t, store, otherScope, endpoint))

		test.EqOp(t, otherScope, endpoint.Scope)

		got, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), otherScope, "endpoint-1")
		must.NoError(t, err)
		test.EqOp(t, otherScope, got.Scope)
	})

	t.Run("guards against nil inputs", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t, saveEndpoint(t, store, testScope, nil), ErrNilEndpoint)
		test.ErrorIs(t, store.RecordAttempt(ctxFor(t), nil), platformerrors.ErrNilInputParameter)

		must.NoError(t, env.client.WithTransaction(ctxFor(t), func(q database.Tx) error {
			test.ErrorIs(t, store.Enqueue(ctxFor(t), q, nil, []string{"e"}, baseTime), ErrNilDelivery)

			return nil
		}))
	})
}

// idsOf projects endpoint IDs.
func idsOf(endpoints []*Endpoint) []string {
	ids := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		ids = append(ids, endpoint.ID)
	}

	return ids
}

// The store holds no clock. Every timestamp it writes is the database server's,
// which is database/querygen's rule rather than a preference taken here: a row's
// created_at and the filter window compared against it have to come from one
// clock, or two application instances a second apart write rows a window
// excludes at random.
//
// So this is the test that used to assert an injected clock was consulted
// twice. What it asserts now is the property that replaced it — that the stamps
// are real and current without anything having supplied them.
func TestSQLStore_StampsFromTheDatabaseClock(T *testing.T) {
	T.Parallel()

	T.Run("endpoint timestamps come from the server", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		client, prefix := env.database(t)

		store, err := NewSQLStore(client, WithTablePrefix(prefix))
		must.NoError(t, err)

		before := time.Now().UTC().Add(-time.Minute)

		must.NoError(t, saveEndpoint(t, store, testScope, &Endpoint{
			ID:            "endpoint",
			URL:           "https://example.com/hook",
			Secret:        Secret{Current: []byte("s3cr3t")},
			Subscriptions: SubscribeTo("user.created"),
		}))

		saved, err := store.GetEndpoint(t.Context(), readerOf(t, store), testScope, "endpoint")
		must.NoError(t, err)

		test.True(t, saved.CreatedAt.After(before),
			test.Sprintf("created_at %s is not after %s", saved.CreatedAt, before))
		test.True(t, saved.CreatedAt.Before(time.Now().UTC().Add(time.Minute)))
		must.SliceLen(t, 1, saved.Subscriptions)
		test.True(t, saved.Subscriptions[0].CreatedAt.After(before))

		must.NoError(t, archiveEndpoint(t, store, testScope, "endpoint"))

		archived, err := store.GetEndpoint(t.Context(), readerOf(t, store), testScope, "endpoint")
		must.NoError(t, err)
		must.NotNil(t, archived.ArchivedAt)
		test.True(t, archived.ArchivedAt.After(before),
			test.Sprintf("archived_at %s is not after %s", archived.ArchivedAt, before))
	})
}
