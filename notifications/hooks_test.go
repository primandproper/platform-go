package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/internal/txcount"

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
	notification *Notification
	device       *Device
	name         string
	principal    string
	scope        tenancy.Scope
	count        int64
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

func (h *recordingHooks) AfterCreateNotification(_ context.Context, _ database.Tx, scope tenancy.Scope, n *Notification) error {
	return h.record(&hookCall{name: "AfterCreateNotification", scope: scope, notification: n})
}

func (h *recordingHooks) AfterMarkNotificationRead(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Notification) error {
	return h.record(&hookCall{name: "AfterMarkNotificationRead", scope: scope, notification: after, before: before})
}

func (h *recordingHooks) AfterMarkAllNotificationsRead(_ context.Context, _ database.Tx, scope tenancy.Scope, principal string, marked int64) error {
	return h.record(&hookCall{name: "AfterMarkAllNotificationsRead", scope: scope, principal: principal, count: marked})
}

func (h *recordingHooks) AfterArchiveNotification(_ context.Context, _ database.Tx, scope tenancy.Scope, n *Notification) error {
	return h.record(&hookCall{name: "AfterArchiveNotification", scope: scope, notification: n})
}

func (h *recordingHooks) AfterDeleteNotificationsForPrincipal(_ context.Context, _ database.Tx, scope tenancy.Scope, principal string, deleted int64) error {
	return h.record(&hookCall{name: "AfterDeleteNotificationsForPrincipal", scope: scope, principal: principal, count: deleted})
}

func (h *recordingHooks) AfterRegisterDevice(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Device) error {
	return h.record(&hookCall{name: "AfterRegisterDevice", scope: scope, device: after, before: before})
}

func (h *recordingHooks) AfterRevokeDevice(_ context.Context, _ database.Tx, scope tenancy.Scope, d *Device) error {
	return h.record(&hookCall{name: "AfterRevokeDevice", scope: scope, device: d})
}

func (h *recordingHooks) AfterDeleteDevicesForPrincipal(_ context.Context, _ database.Tx, scope tenancy.Scope, principal string, deleted int64) error {
	return h.record(&hookCall{name: "AfterDeleteDevicesForPrincipal", scope: scope, principal: principal, count: deleted})
}

// last is the most recent call, failing the test when there was none.
func (h *recordingHooks) last(tb testing.TB) hookCall {
	tb.Helper()

	must.SliceNotEmpty(tb, h.calls)

	return h.calls[len(h.calls)-1]
}

// runHooksSuite is every assertion about the hooks a write runs: that each write
// calls its own, with the row it answers with, that an update is handed the row
// from before it, and that a hook's refusal is the write's.
func runHooksSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("every inbox write calls its hook with the row it answers with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		c := newStubClock()
		store := env.newStore(t, WithClock(c), WithHooks(hooks))

		n := env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))
		call := hooks.last(t)
		test.EqOp(t, "AfterCreateNotification", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, n, call.notification)
		test.False(t, call.notification.CreatedAt.IsZero())

		marked, err := env.markRead(t, store, testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterMarkNotificationRead", call.name)
		test.EqOp(t, marked, call.notification)
		must.NotNil(t, call.notification.ReadAt)
		before, ok := call.before.(*Notification)
		must.True(t, ok)
		test.EqOp(t, n.ID, before.ID)
		test.Nil(t, before.ReadAt)

		// The mark is idempotent, and the pair says so: a second mark is handed a
		// before that was already read, carrying the same stamp as the after.
		c.advance(time.Hour)

		_, err = env.markRead(t, store, testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterMarkNotificationRead", call.name)
		before, ok = call.before.(*Notification)
		must.True(t, ok)
		must.NotNil(t, before.ReadAt)
		test.EqOp(t, before.ReadAt.UTC(), call.notification.ReadAt.UTC())

		env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.delivered", "Your order arrived"))

		count, err := env.markAllRead(t, store, testScope, testPrincipal)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterMarkAllNotificationsRead", call.name)
		test.EqOp(t, testPrincipal, call.principal)
		test.EqOp(t, int64(1), call.count)
		test.EqOp(t, count, call.count)

		archived, err := env.archive(t, store, testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveNotification", call.name)
		test.EqOp(t, archived, call.notification)
		test.NotNil(t, call.notification.ArchivedAt)

		// The erasure reaches the archived row too, and is handed the count.
		for _, want := range []int64{2, 0} {
			erased, eraseErr := env.eraseNotifications(t, store, testScope, testPrincipal)
			must.NoError(t, eraseErr)
			call = hooks.last(t)
			test.EqOp(t, "AfterDeleteNotificationsForPrincipal", call.name)
			test.EqOp(t, testPrincipal, call.principal)
			test.EqOp(t, want, call.count)
			test.EqOp(t, erased, call.count)
		}

		test.SliceLen(t, 8, hooks.calls)
	})

	t.Run("every registry write calls its hook with the row it answers with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		first := env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))
		call := hooks.last(t)
		test.EqOp(t, "AfterRegisterDevice", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, first, call.device)
		beforeDevice, ok := call.before.(*Device)
		must.True(t, ok)
		test.Nil(t, beforeDevice)

		// The handset changes hands, and the pair is what says so.
		moved := env.mustRegister(t, store, testScope, newDevice(otherPrincipal, PlatformIOS, "token-a"))
		call = hooks.last(t)
		test.EqOp(t, "AfterRegisterDevice", call.name)
		test.EqOp(t, moved, call.device)
		test.EqOp(t, otherPrincipal, call.device.Principal)
		beforeDevice, ok = call.before.(*Device)
		must.True(t, ok)
		must.NotNil(t, beforeDevice)
		test.EqOp(t, first.ID, beforeDevice.ID)
		test.EqOp(t, testPrincipal, beforeDevice.Principal)

		revoked, err := env.revoke(t, store, testScope, otherPrincipal, moved.ID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterRevokeDevice", call.name)
		test.EqOp(t, revoked, call.device)
		test.EqOp(t, "token-a", call.device.Token)

		env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformAndroid, "token-b"))

		for _, want := range []int64{1, 0} {
			erased, eraseErr := env.eraseDevices(t, store, testScope, testPrincipal)
			must.NoError(t, eraseErr)
			call = hooks.last(t)
			test.EqOp(t, "AfterDeleteDevicesForPrincipal", call.name)
			test.EqOp(t, testPrincipal, call.principal)
			test.EqOp(t, want, call.count)
			test.EqOp(t, erased, call.count)
		}

		test.SliceLen(t, 6, hooks.calls)
	})

	t.Run("a token arriving from another scope has no before", func(t *testing.T) {
		t.Parallel()

		// The before read is scoped like every read a consumer reaches, so what the
		// handset was in another tenant is not handed to this one.
		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		elsewhere := newDevice(testPrincipal, PlatformIOS, "token-a")
		elsewhere.Scope = otherScope
		env.mustRegister(t, store, otherScope, elsewhere)

		env.mustRegister(t, store, testScope, newDevice(otherPrincipal, PlatformIOS, "token-a"))

		call := hooks.last(t)
		test.EqOp(t, "AfterRegisterDevice", call.name)
		beforeDevice, ok := call.before.(*Device)
		must.True(t, ok)
		test.Nil(t, beforeDevice)
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		n := env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))
		before := len(hooks.calls)

		_, err := env.markRead(t, store, testScope, otherPrincipal, n.ID)
		must.ErrorIs(t, err, ErrNotificationNotFound)

		_, err = env.archive(t, store, testScope, otherPrincipal, n.ID)
		must.ErrorIs(t, err, ErrNotificationNotFound)

		_, err = env.revoke(t, store, testScope, testPrincipal, "no-such-device")
		must.ErrorIs(t, err, ErrDeviceNotFound)

		_, err = env.create(t, store, testScope, newNotification("", "order.shipped", "nobody"))
		must.ErrorIs(t, err, ErrEmptyPrincipal)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the write's, and the write rolls back with it", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterCreateNotification"}
		store := env.newStore(t, WithHooks(hooks))

		filed, err := env.create(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))
		must.ErrorIs(t, err, errHook)
		test.Nil(t, filed)

		inbox, err := store.ListNotifications(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, inbox.Data)
	})

	t.Run("an update's hook error is the update's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterMarkNotificationRead"}
		store := env.newStore(t, WithHooks(hooks))

		n := env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))

		marked, err := env.markRead(t, store, testScope, testPrincipal, n.ID)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, marked)

		read, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		test.Nil(t, read.ReadAt)
	})

	t.Run("a revocation's hook error leaves the handset registered", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterRevokeDevice"}
		store := env.newStore(t, WithHooks(hooks))

		d := env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))

		revoked, err := env.revoke(t, store, testScope, testPrincipal, d.ID)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, revoked)

		devices, err := store.ListDevices(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, devices.Data)
	})

	t.Run("an erasure's hook error is the erasure's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterDeleteNotificationsForPrincipal"}
		store := env.newStore(t, WithHooks(hooks))

		env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))

		count, err := env.eraseNotifications(t, store, testScope, testPrincipal)
		must.ErrorIs(t, err, errHook)
		test.EqOp(t, int64(0), count)

		inbox, err := store.ListNotifications(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, inbox.Data)
	})

	t.Run("nil hooks are no hooks", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t, WithHooks(nil))

		env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))
		env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))
	})

	t.Run("an update reads its before row only for hooks", func(t *testing.T) {
		t.Parallel()

		// Each runs one write on a fresh store and reports how many statements
		// it sent. NoopHooks is installed hooks as far as the store can tell, so
		// the difference is the before read and nothing else.
		markRead := func(t *testing.T, opts ...SQLStoreOption) int64 {
			t.Helper()

			store := env.newStore(t, opts...)
			n := env.mustCreate(t, store, testScope, newNotification(testPrincipal, "order.shipped", "Your order shipped"))

			return countStatements(t, env, func(tx database.Tx) error {
				_, err := store.MarkNotificationRead(t.Context(), tx, testScope, testPrincipal, n.ID)
				return err
			})
		}

		// A re-registration, so the before row the hooked store reads is a real
		// one rather than an absence.
		registerDevice := func(t *testing.T, opts ...SQLStoreOption) int64 {
			t.Helper()

			store := env.newStore(t, opts...)
			env.mustRegister(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))

			return countStatements(t, env, func(tx database.Tx) error {
				_, err := store.RegisterDevice(t.Context(), tx, testScope, newDevice(testPrincipal, PlatformIOS, "token-a"))
				return err
			})
		}

		test.Less(t, markRead(t, WithHooks(NoopHooks{})), markRead(t), test.Sprint("MarkNotificationRead"))
		test.Less(t, registerDevice(t, WithHooks(NoopHooks{})), registerDevice(t), test.Sprint("RegisterDevice"))
	})
}

// countStatements runs fn in a transaction of its own and reports how many
// statements it sent through it.
func countStatements(tb testing.TB, env *storeEnv, fn func(tx database.Tx) error) int64 {
	tb.Helper()

	var counted *txcount.Tx

	must.NoError(tb, env.inTx(tb, func(tx database.Tx) error {
		counted = txcount.Wrap(tx)

		return fn(counted)
	}))

	return counted.Statements()
}
