package webhooks

import (
	"context"
	"database/sql"
	"testing"

	"github.com/primandproper/platform-go/v15/internal/txcount"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHook is a hook refusing the write it was called from.
var errHook = platformerrors.New("the hook failed")

// hookCall is one hook invocation, as the recording hooks saw it.
type hookCall struct {
	before       any
	endpoint     *Endpoint
	subscription *Subscription
	name         string
	endpointID   string
	scope        tenancy.Scope
}

// recordingHooks remembers every call, and fails the one named by failOn.
//
// It implements Hooks outright rather than embedding NoopHooks, so that a method
// added to Hooks later fails this file's build until it is recorded here too,
// rather than arriving as a no-op the suite never asserts on.
type recordingHooks struct {
	failOn string
	calls  []hookCall
}

var _ Hooks = (*recordingHooks)(nil)

func (h *recordingHooks) record(call *hookCall) error {
	h.calls = append(h.calls, *call)

	if call.name == h.failOn {
		return errHook
	}

	return nil
}

func (h *recordingHooks) AfterSaveEndpoint(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Endpoint) error {
	return h.record(&hookCall{name: "AfterSaveEndpoint", scope: scope, endpoint: after, before: before})
}

func (h *recordingHooks) AfterArchiveEndpoint(_ context.Context, _ database.Tx, scope tenancy.Scope, endpoint *Endpoint) error {
	return h.record(&hookCall{name: "AfterArchiveEndpoint", scope: scope, endpoint: endpoint})
}

func (h *recordingHooks) AfterRotateSecret(_ context.Context, _ database.Tx, scope tenancy.Scope, endpointID string) error {
	return h.record(&hookCall{name: "AfterRotateSecret", scope: scope, endpointID: endpointID})
}

func (h *recordingHooks) AfterAddSubscription(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Subscription) error {
	return h.record(&hookCall{name: "AfterAddSubscription", scope: scope, subscription: after, before: before})
}

func (h *recordingHooks) AfterArchiveSubscription(_ context.Context, _ database.Tx, scope tenancy.Scope, subscription *Subscription) error {
	return h.record(&hookCall{name: "AfterArchiveSubscription", scope: scope, subscription: subscription})
}

// last is the most recent call, failing the test when there was none.
func (h *recordingHooks) last(tb testing.TB) hookCall {
	tb.Helper()

	must.SliceNotEmpty(tb, h.calls)

	return h.calls[len(h.calls)-1]
}

// runHooksSuite is every assertion about the hooks a write runs: that each write
// calls its own, with the row it answers with, that a write overwriting a row
// hands over the row it overwrote, and that a hook's refusal is the write's.
func runHooksSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("every write calls its hook with the row it answers with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		saved := registerEndpoint(t, store, "endpoint-1", orderCreated)
		call := hooks.last(t)
		test.EqOp(t, "AfterSaveEndpoint", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, saved, call.endpoint)
		test.True(t, call.endpoint.Created)
		created, ok := call.before.(*Endpoint)
		must.True(t, ok)
		test.Nil(t, created)

		added, err := addSubscription(t, store, testScope, "endpoint-1", orderUpdated)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterAddSubscription", call.name)
		test.EqOp(t, added, call.subscription)
		test.False(t, call.subscription.Archived())

		must.NoError(t, rotateSecret(t, store, testScope, "endpoint-1", []byte("next")))
		call = hooks.last(t)
		test.EqOp(t, "AfterRotateSecret", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, "endpoint-1", call.endpointID)

		archivedSubscription := mustArchiveSubscription(t, store, testScope, added.ID)
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveSubscription", call.name)
		test.EqOp(t, archivedSubscription, call.subscription)
		test.True(t, call.subscription.Archived())

		archivedEndpoint := mustArchiveEndpoint(t, store, testScope, "endpoint-1")
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveEndpoint", call.name)
		test.EqOp(t, archivedEndpoint, call.endpoint)
		test.True(t, call.endpoint.Archived())

		test.SliceLen(t, 5, hooks.calls)
	})

	t.Run("a re-registration's hook is handed the endpoint it overwrote", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		first := registerEndpoint(t, store, "endpoint-1", orderCreated)

		resaved := mustSaveEndpoint(t, store, testScope, &Endpoint{
			ID:            "endpoint-1",
			Name:          "renamed",
			URL:           "https://93.184.216.34/hooks/moved",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("secret-endpoint-1")},
			Subscriptions: SubscribeTo(orderUpdated),
		})

		call := hooks.last(t)
		test.EqOp(t, "AfterSaveEndpoint", call.name)
		test.EqOp(t, resaved, call.endpoint)
		test.False(t, call.endpoint.Created)
		test.EqOp(t, "https://93.184.216.34/hooks/moved", call.endpoint.URL)

		before, ok := call.before.(*Endpoint)
		must.True(t, ok)
		must.NotNil(t, before)
		test.EqOp(t, first.URL, before.URL)
		test.EqOp(t, "", before.Name)
		// The before row carries the subscriptions that were live, so a hook can
		// say which event types the save retired as well as which fields moved.
		test.Eq(t, []EventType{orderCreated}, before.EventTypes())
		test.Eq(t, []EventType{orderUpdated}, call.endpoint.EventTypes())
	})

	t.Run("a revived subscription's hook is handed the archived row it revived", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		saved := registerEndpoint(t, store, "endpoint-1", orderCreated, orderUpdated)
		retired := subscriptionFor(t, saved, orderCreated)
		mustArchiveSubscription(t, store, testScope, retired.ID)

		revived, err := addSubscription(t, store, testScope, "endpoint-1", orderCreated)
		must.NoError(t, err)

		call := hooks.last(t)
		test.EqOp(t, "AfterAddSubscription", call.name)
		test.EqOp(t, revived, call.subscription)
		test.False(t, call.subscription.Archived())

		before, ok := call.before.(*Subscription)
		must.True(t, ok)
		must.NotNil(t, before)
		test.EqOp(t, retired.ID, before.ID)
		test.True(t, before.Archived())
	})

	t.Run("a new subscription's hook is handed no before row", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		registerEndpoint(t, store, "endpoint-1", orderCreated)

		_, err := addSubscription(t, store, testScope, "endpoint-1", orderUpdated)
		must.NoError(t, err)

		call := hooks.last(t)
		test.EqOp(t, "AfterAddSubscription", call.name)

		before, ok := call.before.(*Subscription)
		must.True(t, ok)
		test.Nil(t, before)
	})

	t.Run("an archive that names nothing calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		archivedEndpoint, err := archiveEndpoint(t, store, testScope, "never-registered")
		must.NoError(t, err)
		test.Nil(t, archivedEndpoint)

		archivedSubscription, err := archiveSubscription(t, store, testScope, "never-subscribed")
		must.NoError(t, err)
		test.Nil(t, archivedSubscription)

		test.SliceEmpty(t, hooks.calls)
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		registerScopedEndpoint(t, store, otherScope, "endpoint-1", orderCreated)
		before := len(hooks.calls)

		_, err := saveEndpoint(t, store, testScope, &Endpoint{
			ID:            "endpoint-1",
			URL:           "https://93.184.216.34/hooks/endpoint-1",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("secret")},
			Subscriptions: SubscribeTo(orderCreated),
		})
		must.Error(t, err)

		_, err = addSubscription(t, store, testScope, "endpoint-1", orderUpdated)
		must.ErrorIs(t, err, sql.ErrNoRows)

		err = rotateSecret(t, store, testScope, "endpoint-1", []byte("next"))
		must.ErrorIs(t, err, sql.ErrNoRows)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the write's, and the write rolls back with it", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterSaveEndpoint"}
		store := env.newStore(t, WithHooks(hooks))

		saved, err := saveEndpoint(t, store, testScope, &Endpoint{
			ID:            "endpoint-1",
			URL:           "https://93.184.216.34/hooks/endpoint-1",
			ContentType:   DefaultContentType,
			Secret:        Secret{Current: []byte("secret")},
			Subscriptions: SubscribeTo(orderCreated),
		})
		must.ErrorIs(t, err, errHook)
		test.Nil(t, saved)

		_, err = store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("an archive's hook error is the archive's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterArchiveSubscription"}
		store := env.newStore(t, WithHooks(hooks))

		saved := registerEndpoint(t, store, "endpoint-1", orderCreated)
		subscription := subscriptionFor(t, saved, orderCreated)

		archived, err := archiveSubscription(t, store, testScope, subscription.ID)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, archived)

		read, err := store.GetSubscription(ctxFor(t), readerOf(t, store), testScope, subscription.ID)
		must.NoError(t, err)
		test.False(t, read.Archived())
	})

	t.Run("a rotation's hook error is the rotation's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterRotateSecret"}
		store := env.newStore(t, WithHooks(hooks))

		registerEndpoint(t, store, "endpoint-1", orderCreated)

		err := rotateSecret(t, store, testScope, "endpoint-1", []byte("next"))
		must.ErrorIs(t, err, errHook)

		read, err := store.GetEndpoint(ctxFor(t), readerOf(t, store), testScope, "endpoint-1")
		must.NoError(t, err)
		test.Eq(t, []byte("secret-endpoint-1"), read.Secret.Current)
	})

	t.Run("nil hooks are no hooks", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t, WithHooks(nil))

		registerEndpoint(t, store, "endpoint-1", orderCreated)
	})

	t.Run("a write reads its before row only for hooks", func(t *testing.T) {
		t.Parallel()

		// Each runs one write on a fresh store and reports how many statements
		// it sent. NoopHooks is installed hooks as far as the store can tell, so
		// the difference is the before read and nothing else.
		resave := func(t *testing.T, opts ...SQLStoreOption) int64 {
			t.Helper()

			store := env.newStore(t, opts...)
			registerEndpoint(t, store, "endpoint-1", orderCreated)

			return countStatements(t, store, func(tx database.Tx) error {
				_, err := store.SaveEndpoint(t.Context(), tx, testScope, &Endpoint{
					ID:            "endpoint-1",
					URL:           "https://93.184.216.34/hooks/moved",
					ContentType:   DefaultContentType,
					Secret:        Secret{Current: []byte("secret-endpoint-1")},
					Subscriptions: SubscribeTo(orderCreated),
				})
				return err
			})
		}

		subscribe := func(t *testing.T, opts ...SQLStoreOption) int64 {
			t.Helper()

			store := env.newStore(t, opts...)
			registerEndpoint(t, store, "endpoint-1", orderCreated)

			return countStatements(t, store, func(tx database.Tx) error {
				_, err := store.AddSubscription(t.Context(), tx, testScope, "endpoint-1", orderUpdated)
				return err
			})
		}

		test.Less(t, resave(t, WithHooks(NoopHooks{})), resave(t), test.Sprint("SaveEndpoint"))
		test.Less(t, subscribe(t, WithHooks(NoopHooks{})), subscribe(t), test.Sprint("AddSubscription"))
	})
}

// countStatements runs fn in a transaction of its own and reports how many
// statements it sent through it.
func countStatements(t *testing.T, store Store, fn func(tx database.Tx) error) int64 {
	t.Helper()

	var counted *txcount.Tx

	must.NoError(t, inTx(t, store, func(tx database.Tx) error {
		counted = txcount.Wrap(tx)

		return fn(counted)
	}))

	return counted.Statements()
}
