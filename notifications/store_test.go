package notifications

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore_SQLite runs the behavioral suite against SQLite, which is the
// engine every developer has. TestSQLStore_RealServers runs the identical suite
// against Postgres and MySQL — see containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is everything this store promises, run against whichever
// database it is handed.
//
// It is one function rather than a file of top-level tests because the three
// engines have to be held to the same behavior: the conflict clause, the
// placeholder rendering and the archived predicates are spelled three ways, and
// a suite that ran only against SQLite would prove the one spelling SQLite
// accepts.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("inbox", func(t *testing.T) {
		t.Parallel()

		runInboxSuite(t, env)
	})

	t.Run("registry", func(t *testing.T) {
		t.Parallel()

		runRegistrySuite(t, env)
	})

	t.Run("transactions", func(t *testing.T) {
		t.Parallel()

		runTransactionSuite(t, env)
	})
}

func runInboxSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a created notification comes back with what the database assigned", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		n := newNotification(testPrincipal, "order.shipped", "Your order shipped")
		must.NoError(t, env.create(t, store, testScope, n))

		// The id is minted here and the creation time is the database's, read
		// back rather than left as a zero time a caller would serialize as a
		// date in the year one.
		test.NotEqOp(t, "", n.ID)
		test.False(t, n.CreatedAt.IsZero())
		test.Nil(t, n.ReadAt)

		read, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		must.NoError(t, err)

		test.EqOp(t, n.ID, read.ID)
		test.EqOp(t, "order.shipped", read.Topic)
		test.EqOp(t, "Your order shipped", read.Title)
		test.EqOp(t, "the body", read.Body)
		test.EqOp(t, "/orders/1", read.Link)
		test.EqOp(t, testScope, read.Scope)
		test.False(t, read.Read())
	})

	t.Run("an id the caller supplied is the id that is stored", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		n := newNotification(testPrincipal, "invite.received", "You were invited")
		n.ID = "notif_supplied"
		must.NoError(t, env.create(t, store, testScope, n))

		read, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, "notif_supplied")
		must.NoError(t, err)
		test.EqOp(t, "notif_supplied", read.ID)
	})

	t.Run("another principal's notification reads as absent", func(t *testing.T) {
		t.Parallel()

		// The failure this exists for: a get keyed on the scope alone would hand
		// one member of an account another member's inbox row by id.
		store := env.newStore(t)

		n := newNotification(otherPrincipal, "order.shipped", "Their order shipped")
		must.NoError(t, env.create(t, store, testScope, n))

		_, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		test.ErrorIs(t, err, ErrNotificationNotFound)
	})

	t.Run("another scope's notification reads as absent", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		n := newNotification(testPrincipal, "order.shipped", "Somebody else's tenant")
		n.Scope = otherScope
		must.NoError(t, env.create(t, store, otherScope, n))

		_, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		test.ErrorIs(t, err, ErrNotificationNotFound)
	})

	t.Run("the inbox lists only this principal's notifications, in both directions", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := make([]string, 0, 3)
		for _, title := range []string{"first", "second", "third"} {
			n := newNotification(testPrincipal, "order.shipped", title)
			must.NoError(t, env.create(t, store, testScope, n))
			mine = append(mine, n.ID)
		}

		theirs := newNotification(otherPrincipal, "order.shipped", "not yours")
		must.NoError(t, env.create(t, store, testScope, theirs))

		ascending, err := store.ListNotifications(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		must.SliceLen(t, 3, ascending.Data)
		test.EqOp(t, uint64(3), ascending.FilteredCount)

		for i, id := range mine {
			test.EqOp(t, id, ascending.Data[i].ID)
		}

		descending, err := store.ListNotifications(t.Context(), env.reader(), testScope, testPrincipal,
			&filtering.QueryFilter{SortBy: filtering.SortDescending})
		must.NoError(t, err)
		must.SliceLen(t, 3, descending.Data)

		// The same page walked the other way, which is a second statement rather
		// than a bound argument — so this is what proves the store picked it.
		for i, id := range mine {
			test.EqOp(t, id, descending.Data[len(mine)-1-i].ID)
		}
	})

	t.Run("the unread list carries the badge count", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		ids := make([]string, 0, 3)
		for _, title := range []string{"first", "second", "third"} {
			n := newNotification(testPrincipal, "order.shipped", title)
			must.NoError(t, env.create(t, store, testScope, n))
			ids = append(ids, n.ID)
		}

		must.NoError(t, env.markRead(t, store, testScope, testPrincipal, ids[0]))

		// One page of one, and the count is still of everything unread rather
		// than of what came back — which is the whole reason this schema needs no
		// COUNT statement.
		unread, err := store.ListUnreadNotifications(t.Context(), env.reader(), testScope, testPrincipal,
			&filtering.QueryFilter{MaxResponseSize: pointer.To(uint16(1))})
		must.NoError(t, err)

		must.SliceLen(t, 1, unread.Data)
		test.EqOp(t, ids[1], unread.Data[0].ID)
		test.EqOp(t, uint64(2), unread.FilteredCount)
	})

	t.Run("marking read stamps once and stays stamped", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		store := env.newStore(t, WithClock(c))

		n := newNotification(testPrincipal, "order.shipped", "Your order shipped")
		must.NoError(t, env.create(t, store, testScope, n))

		must.NoError(t, env.markRead(t, store, testScope, testPrincipal, n.ID))

		read, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		must.NotNil(t, read.ReadAt)
		test.EqOp(t, baseTime, read.ReadAt.UTC())

		// A second mark is a success that changes nothing. Moving the stamp is
		// what turns "read last Tuesday" into "read on every list refresh", and
		// it is the reason the statement guards on the column being absent.
		c.advance(time.Hour)
		must.NoError(t, env.markRead(t, store, testScope, testPrincipal, n.ID))

		reread, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		must.NotNil(t, reread.ReadAt)
		test.EqOp(t, baseTime, reread.ReadAt.UTC())
	})

	t.Run("marking a notification that is not in the inbox reports it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Zero rows means two things — already read, or not there — and this is
		// the half that must not be reported as success.
		err := env.markRead(t, store, testScope, testPrincipal, "notif_nonexistent")
		test.ErrorIs(t, err, ErrNotificationNotFound)

		theirs := newNotification(otherPrincipal, "order.shipped", "not yours")
		must.NoError(t, env.create(t, store, testScope, theirs))

		err = env.markRead(t, store, testScope, testPrincipal, theirs.ID)
		test.ErrorIs(t, err, ErrNotificationNotFound)
	})

	t.Run("marking everything read counts what was unread", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for _, title := range []string{"first", "second", "third"} {
			n := newNotification(testPrincipal, "order.shipped", title)
			must.NoError(t, env.create(t, store, testScope, n))
		}

		theirs := newNotification(otherPrincipal, "order.shipped", "not yours")
		must.NoError(t, env.create(t, store, testScope, theirs))

		count, err := env.markAllRead(t, store, testScope, testPrincipal)
		must.NoError(t, err)
		test.EqOp(t, int64(3), count)

		// Idempotent, and the count says so.
		count, err = env.markAllRead(t, store, testScope, testPrincipal)
		must.NoError(t, err)
		test.EqOp(t, int64(0), count)

		// The other principal's notification is untouched, which is what the
		// principal predicate on a statement with no id is there for.
		other, err := store.GetNotification(t.Context(), env.reader(), testScope, otherPrincipal, theirs.ID)
		must.NoError(t, err)
		test.False(t, other.Read())
	})

	t.Run("archiving takes it out of the inbox and keeps the row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		n := newNotification(testPrincipal, "order.shipped", "Your order shipped")
		must.NoError(t, env.create(t, store, testScope, n))

		must.NoError(t, env.archive(t, store, testScope, testPrincipal, n.ID))

		_, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		test.ErrorIs(t, err, ErrNotificationNotFound)

		live, err := store.ListNotifications(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, live.Data)

		// The row is still there for whoever asks later what somebody was told.
		archived, err := store.ListNotifications(t.Context(), env.reader(), testScope, testPrincipal,
			&filtering.QueryFilter{IncludeArchived: pointer.To(true)})
		must.NoError(t, err)
		must.SliceLen(t, 1, archived.Data)
		test.NotNil(t, archived.Data[0].ArchivedAt)

		// Archiving it again finds nothing, because an archived notification is
		// not in the inbox and this addresses the inbox.
		test.ErrorIs(t,
			env.archive(t, store, testScope, testPrincipal, n.ID),
			ErrNotificationNotFound)
	})

	t.Run("refuses a notification nobody could read", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t, env.create(t, store, testScope, nil), ErrNilNotification)

		// The scope is the write's rather than the row's, so an unset one is an
		// unset argument. A notification carrying none adopts what the write
		// names, which is the reading that lets a caller pass the value they were
		// handed without restating it.
		test.ErrorIs(t,
			env.create(t, store, tenancy.Scope{},
				newNotification(testPrincipal, "order.shipped", "Your order shipped")),
			tenancy.ErrNoScope)

		adopting := newNotification(testPrincipal, "order.shipped", "Your order shipped")
		adopting.Scope = tenancy.Scope{}
		must.NoError(t, env.create(t, store, testScope, adopting))
		test.EqOp(t, testScope, adopting.Scope)

		// One that names a different one is refused rather than corrected: a
		// caller holding one tenant's notification and filing it into another is
		// a mix-up, not a thing to guess at.
		elsewhere := newNotification(testPrincipal, "order.shipped", "Your order shipped")
		elsewhere.Scope = otherScope
		test.ErrorIs(t, env.create(t, store, testScope, elsewhere), ErrScopeMismatch)

		unaddressed := newNotification("", "order.shipped", "Your order shipped")
		test.ErrorIs(t, env.create(t, store, testScope, unaddressed), ErrEmptyPrincipal)

		untopiced := newNotification(testPrincipal, "", "Your order shipped")
		test.ErrorIs(t, env.create(t, store, testScope, untopiced), ErrEmptyTopic)

		untitled := newNotification(testPrincipal, "order.shipped", "")
		test.Error(t, env.create(t, store, testScope, untitled))
	})

	t.Run("refuses a read that names no scope or no principal", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.GetNotification(t.Context(), env.reader(), tenancy.Scope{}, testPrincipal, "notif_1")
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.ListNotifications(t.Context(), env.reader(), tenancy.Scope{}, testPrincipal, nil)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.ListUnreadNotifications(t.Context(), env.reader(), testScope, "", nil)
		test.ErrorIs(t, err, ErrEmptyPrincipal)

		_, err = env.markAllRead(t, store, testScope, "")
		test.ErrorIs(t, err, ErrEmptyPrincipal)

		test.ErrorIs(t,
			env.markRead(t, store, testScope, "", "notif_1"), ErrEmptyPrincipal)
		test.ErrorIs(t,
			env.archive(t, store, testScope, "", "notif_1"), ErrEmptyPrincipal)
	})
}

func runRegistrySuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a registered device comes back with what the database assigned", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		store := env.newStore(t, WithClock(c))

		d := newDevice(testPrincipal, PlatformIOS, "token-a")
		must.NoError(t, env.register(t, store, testScope, d))

		test.NotEqOp(t, "", d.ID)
		test.False(t, d.CreatedAt.IsZero())
		test.EqOp(t, baseTime, d.LastSeenAt.UTC())
		test.EqOp(t, PlatformIOS, d.Platform)
	})

	t.Run("re-registering the same handset keeps its identity", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		store := env.newStore(t, WithClock(c))

		first := newDevice(testPrincipal, PlatformIOS, "token-a")
		must.NoError(t, env.register(t, store, testScope, first))

		c.advance(time.Hour)

		// A fresh value, as a client that has forgotten its registration id would
		// send: same token, new id. The row it converges on keeps the id the
		// first registration minted, and the caller is told so — otherwise they
		// would hold an id no row has and revoke nothing on sign-out.
		again := newDevice(testPrincipal, PlatformIOS, "token-a")
		must.NoError(t, env.register(t, store, testScope, again))

		test.EqOp(t, first.ID, again.ID)
		test.EqOp(t, first.CreatedAt, again.CreatedAt)
		test.EqOp(t, baseTime.Add(time.Hour), again.LastSeenAt.UTC())

		devices, err := store.ListDevices(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, devices.Data)
	})

	t.Run("a handset that changes hands moves rather than fanning out", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := newDevice(testPrincipal, PlatformIOS, "token-a")
		must.NoError(t, env.register(t, store, testScope, first))

		// Somebody else signs in on the same phone. Two rows here would deliver
		// the previous owner's notifications to the new one.
		second := newDevice(otherPrincipal, PlatformIOS, "token-a")
		must.NoError(t, env.register(t, store, testScope, second))

		gone, err := store.ListDevices(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, gone.Data)

		moved, err := store.ListDevices(t.Context(), env.reader(), testScope, otherPrincipal, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, moved.Data)
		test.EqOp(t, first.ID, moved.Data[0].ID)
	})

	t.Run("the same token on two platforms is two devices", func(t *testing.T) {
		t.Parallel()

		// The two providers mint their tokens independently, so the platform is
		// half the key rather than a label on it.
		store := env.newStore(t)

		must.NoError(t, env.register(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a")))
		must.NoError(t, env.register(t, store, testScope, newDevice(testPrincipal, PlatformAndroid, "token-a")))

		devices, err := store.ListDevices(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, devices.Data)
	})

	t.Run("the batched read answers a fan-out in one query", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, env.register(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a")))
		must.NoError(t, env.register(t, store, testScope, newDevice(testPrincipal, PlatformAndroid, "token-b")))
		must.NoError(t, env.register(t, store, testScope, newDevice(otherPrincipal, PlatformIOS, "token-c")))

		elsewhere := newDevice(testPrincipal, PlatformIOS, "token-d")
		elsewhere.Scope = otherScope
		must.NoError(t, env.register(t, store, otherScope, elsewhere))

		devices, err := store.ListDevicesByPrincipals(t.Context(), env.reader(), testScope,
			[]string{testPrincipal, otherPrincipal})
		must.NoError(t, err)
		test.SliceLen(t, 3, devices)

		// An empty batch is an empty answer and no query — the statement has no
		// rendering of an empty set.
		none, err := store.ListDevicesByPrincipals(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, none)
	})

	t.Run("revoking removes the row and reports a registration that was not there", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		d := newDevice(testPrincipal, PlatformIOS, "token-a")
		must.NoError(t, env.register(t, store, testScope, d))

		must.NoError(t, env.revoke(t, store, testScope, testPrincipal, d.ID))

		devices, err := store.ListDevices(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, devices.Data)

		test.ErrorIs(t,
			env.revoke(t, store, testScope, testPrincipal, d.ID), ErrDeviceNotFound)
	})

	t.Run("another principal cannot revoke this one's device", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		d := newDevice(testPrincipal, PlatformIOS, "token-a")
		must.NoError(t, env.register(t, store, testScope, d))

		test.ErrorIs(t,
			env.revoke(t, store, testScope, otherPrincipal, d.ID), ErrDeviceNotFound)
		test.ErrorIs(t,
			env.revoke(t, store, otherScope, testPrincipal, d.ID), ErrDeviceNotFound)
	})

	t.Run("the provider hook prunes across every scope and is idempotent", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Registered in a scope the hook is never told about, because a provider
		// answering a push names a token and nothing else.
		elsewhere := newDevice(testPrincipal, PlatformIOS, "token-dead")
		elsewhere.Scope = otherScope
		must.NoError(t, env.register(t, store, otherScope, elsewhere))

		must.NoError(t, store.InvalidateDeviceToken(t.Context(), "ios", "token-dead"))

		devices, err := store.ListDevices(t.Context(), env.reader(), otherScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, devices.Data)

		// A token already gone is the state the caller asked for. Two workers
		// pruning the same dead token must not turn a push into a second failure.
		must.NoError(t, store.InvalidateDeviceToken(t.Context(), "ios", "token-dead"))
	})

	t.Run("the provider hook normalizes the platform it is handed", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, env.register(t, store, testScope, newDevice(testPrincipal, PlatformIOS, "token-a")))

		// A mobile client's spelling, which is what reaches a sender.
		must.NoError(t, store.InvalidateDeviceToken(t.Context(), " iOS ", "token-a"))

		devices, err := store.ListDevices(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, devices.Data)
	})

	t.Run("the provider hook refuses what it cannot act on", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// A platform nothing routes to would delete nothing and report success,
		// which is exactly the silence this hook exists to end.
		test.ErrorIs(t,
			store.InvalidateDeviceToken(t.Context(), "blackberry", "token-a"), ErrUnknownPlatform)
		test.ErrorIs(t,
			store.InvalidateDeviceToken(t.Context(), "ios", ""), ErrEmptyToken)
	})

	t.Run("refuses a registration nothing could ever push to", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t, env.register(t, store, testScope, nil), ErrNilDevice)

		test.ErrorIs(t,
			env.register(t, store, tenancy.Scope{},
				newDevice(testPrincipal, PlatformIOS, "token-a")),
			tenancy.ErrNoScope)

		adopting := newDevice(testPrincipal, PlatformIOS, "token-adopting")
		adopting.Scope = tenancy.Scope{}
		must.NoError(t, env.register(t, store, testScope, adopting))
		test.EqOp(t, testScope, adopting.Scope)

		mismatched := newDevice(testPrincipal, PlatformIOS, "token-mismatched")
		mismatched.Scope = otherScope
		test.ErrorIs(t, env.register(t, store, testScope, mismatched), ErrScopeMismatch)

		unaddressed := newDevice("", PlatformIOS, "token-a")
		test.ErrorIs(t, env.register(t, store, testScope, unaddressed), ErrEmptyPrincipal)

		unroutable := newDevice(testPrincipal, Platform("blackberry"), "token-a")
		test.ErrorIs(t, env.register(t, store, testScope, unroutable), ErrUnknownPlatform)

		tokenless := newDevice(testPrincipal, PlatformIOS, "")
		test.ErrorIs(t, env.register(t, store, testScope, tokenless), ErrEmptyToken)
	})

	t.Run("refuses a read that names no scope or no principal", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.ListDevices(t.Context(), env.reader(), tenancy.Scope{}, testPrincipal, nil)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.ListDevices(t.Context(), env.reader(), testScope, "", nil)
		test.ErrorIs(t, err, ErrEmptyPrincipal)

		_, err = store.ListDevicesByPrincipals(t.Context(), env.reader(), tenancy.Scope{}, []string{testPrincipal})
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		test.ErrorIs(t,
			env.revoke(t, store, testScope, "", "device_1"), ErrEmptyPrincipal)
	})
}

func runTransactionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a write and a read inside one transaction observe each other", func(t *testing.T) {
		t.Parallel()

		// The property the reads were widened for, and the one no auto-committing
		// write could express: inside the transaction the notification is there,
		// and from outside it is not there yet. A read narrowed to the client's
		// reader would have been reading a database that does not hold the row its
		// own caller just wrote.
		store := env.newStore(t)

		n := newNotification(testPrincipal, "order.shipped", "written and read on one executor")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if err := store.CreateNotification(t.Context(), tx, testScope, n); err != nil {
				return err
			}

			read, err := store.GetNotification(t.Context(), tx, testScope, testPrincipal, n.ID)
			if err != nil {
				return err
			}

			test.EqOp(t, "written and read on one executor", read.Title)

			unread, err := store.ListUnreadNotifications(t.Context(), tx, testScope, testPrincipal, nil)
			if err != nil {
				return err
			}

			must.SliceLen(t, 1, unread.Data)
			test.EqOp(t, n.ID, unread.Data[0].ID)

			// And the same read, on the client, cannot see it: the transaction
			// has not committed, so this is the other half of the same fact
			// rather than a second one.
			outside, err := store.ListUnreadNotifications(t.Context(), env.reader(), testScope, testPrincipal, nil)
			if err != nil {
				return err
			}

			test.SliceEmpty(t, outside.Data)

			return nil
		}))

		// After the commit both executors agree, which is what makes the reading
		// above about visibility rather than about two different rows.
		read, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		must.NoError(t, err)
		test.EqOp(t, n.ID, read.ID)
	})

	t.Run("a notification created in a rolled-back transaction is never visible", func(t *testing.T) {
		t.Parallel()

		// The failure the auto form allowed, and the reason this port exists. A
		// notification is about something else that was just written; filed in a
		// transaction of the store's own, it survives that operation unwinding —
		// so a refused order still tells somebody their order was placed, and the
		// failure runs in the direction the user can see.
		store := env.newStore(t)

		n := newNotification(testPrincipal, "order.shipped", "an order that never happened")

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.CreateNotification(t.Context(), tx, testScope, n); txErr != nil {
				return txErr
			}

			// Standing in for the write a consumer makes beside it: the order
			// row, the audit entry, the outbox event.
			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The id was minted onto the caller's value on the way through. Nothing
		// undoes that, and nothing should: what rolled back is the row.
		test.NotEqOp(t, "", n.ID)

		_, err = store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, n.ID)
		must.ErrorIs(t, err, ErrNotificationNotFound)

		// And the badge stays at nothing, which is what the person would have
		// seen: an inbox that never mentioned it.
		unread, err := store.ListUnreadNotifications(t.Context(), env.reader(), testScope, testPrincipal, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, unread.Data)
		test.EqOp(t, uint64(0), unread.FilteredCount)
	})

	t.Run("the inbox writes commit with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		created := newNotification(testPrincipal, "order.shipped", "written inside")

		marked := newNotification(testPrincipal, "invite.received", "to be read")
		must.NoError(t, env.create(t, store, testScope, marked))

		doomed := newNotification(testPrincipal, "order.shipped", "on the way out")
		must.NoError(t, env.create(t, store, testScope, doomed))

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if err := store.CreateNotification(t.Context(), tx, testScope, created); err != nil {
				return err
			}

			if err := store.MarkNotificationRead(t.Context(), tx, testScope, testPrincipal, marked.ID); err != nil {
				return err
			}

			return store.ArchiveNotification(t.Context(), tx, testScope, testPrincipal, doomed.ID)
		}))

		// The create reads its creation time back through the caller's executor,
		// so the value the caller is handed is the row this transaction wrote
		// rather than a zero time waiting on a commit.
		test.NotEqOp(t, "", created.ID)
		test.False(t, created.CreatedAt.IsZero())

		read, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, created.ID)
		must.NoError(t, err)
		test.EqOp(t, "written inside", read.Title)

		read, err = store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, marked.ID)
		must.NoError(t, err)
		test.True(t, read.Read())

		_, err = store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, doomed.ID)
		must.ErrorIs(t, err, ErrNotificationNotFound)
	})

	t.Run("a rolled back transaction takes every inbox write with it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		marked := newNotification(testPrincipal, "invite.received", "still unread afterwards")
		must.NoError(t, env.create(t, store, testScope, marked))

		survivor := newNotification(testPrincipal, "order.shipped", "still in the inbox")
		must.NoError(t, env.create(t, store, testScope, survivor))

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.MarkNotificationRead(t.Context(), tx, testScope, testPrincipal, marked.ID); txErr != nil {
				return txErr
			}

			if txErr := store.ArchiveNotification(t.Context(), tx, testScope, testPrincipal, survivor.ID); txErr != nil {
				return txErr
			}

			// The count MarkAllNotificationsRead reports describes a state that
			// is about to stop having happened, which is why its doc says the
			// number is this transaction's.
			if _, txErr := store.MarkAllNotificationsRead(t.Context(), tx, testScope, testPrincipal); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		read, err := store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, marked.ID)
		must.NoError(t, err)
		test.False(t, read.Read())

		read, err = store.GetNotification(t.Context(), env.reader(), testScope, testPrincipal, survivor.ID)
		must.NoError(t, err)
		test.Nil(t, read.ArchivedAt)
	})

	t.Run("the registry writes commit and roll back with the caller", func(t *testing.T) {
		t.Parallel()

		// A sign-out is several writes — the session ends, the refresh token is
		// revoked, the handset stops being addressable — and this is the write
		// that joins them.
		store := env.newStore(t)

		revoked := newDevice(testPrincipal, PlatformIOS, "token-signed-out")
		must.NoError(t, env.register(t, store, testScope, revoked))

		registered := newDevice(testPrincipal, PlatformAndroid, "token-never-committed")

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.RegisterDevice(t.Context(), tx, testScope, registered); txErr != nil {
				return txErr
			}

			// The fan-out read, on the caller's executor: a handset registered a
			// moment ago in this transaction is one this push is addressed to.
			devices, txErr := store.ListDevicesByPrincipals(t.Context(), tx, testScope, []string{testPrincipal})
			if txErr != nil {
				return txErr
			}

			test.SliceLen(t, 2, devices)

			if txErr = store.RevokeDevice(t.Context(), tx, testScope, testPrincipal, revoked.ID); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// Neither half survived: the new handset was never registered, and the
		// signed-out one is still addressable because the sign-out unwound.
		devices, err := store.ListDevicesByPrincipals(t.Context(), env.reader(), testScope, []string{testPrincipal})
		must.NoError(t, err)
		must.SliceLen(t, 1, devices)
		test.EqOp(t, revoked.ID, devices[0].ID)
	})

	t.Run("every method a consumer calls refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// Every one of the eleven, not a representative one. There is no
		// connection of the store's own for a consumer-facing method to fall back
		// to, so one that did anything but refuse would be reaching for something
		// that is not there.
		store := env.newStore(t)

		must.ErrorIs(t,
			store.CreateNotification(t.Context(), nil, testScope,
				newNotification(testPrincipal, "order.shipped", "words")),
			ErrNilExecutor)
		must.ErrorIs(t,
			store.MarkNotificationRead(t.Context(), nil, testScope, testPrincipal, "notif_1"),
			ErrNilExecutor)
		must.ErrorIs(t,
			store.ArchiveNotification(t.Context(), nil, testScope, testPrincipal, "notif_1"),
			ErrNilExecutor)
		must.ErrorIs(t,
			store.RegisterDevice(t.Context(), nil, testScope, newDevice(testPrincipal, PlatformIOS, "token-a")),
			ErrNilExecutor)
		must.ErrorIs(t,
			store.RevokeDevice(t.Context(), nil, testScope, testPrincipal, "device_1"),
			ErrNilExecutor)

		_, err := store.MarkAllNotificationsRead(t.Context(), nil, testScope, testPrincipal)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetNotification(t.Context(), nil, testScope, testPrincipal, "notif_1")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListNotifications(t.Context(), nil, testScope, testPrincipal, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListUnreadNotifications(t.Context(), nil, testScope, testPrincipal, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListDevices(t.Context(), nil, testScope, testPrincipal, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListDevicesByPrincipals(t.Context(), nil, testScope, []string{testPrincipal})
		must.ErrorIs(t, err, ErrNilExecutor)
	})
}

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a prefix that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(env.client, WithTablePrefix("no-hyphens-allowed"))
		test.Error(t, err)
	})

	T.Run("reports the prefix it was built with", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, WithTablePrefix("ntf"))
		must.NoError(t, err)
		test.EqOp(t, "ntf", store.TablePrefix())

		unprefixed, err := NewSQLStore(env.client)
		must.NoError(t, err)
		test.EqOp(t, DefaultTablePrefix, unprefixed.TablePrefix())
	})

	T.Run("ignores a nil option and a nil clock", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, nil, WithClock(nil))
		must.NoError(t, err)
		must.NotNil(t, store)
		test.False(t, store.now().IsZero())
	})
}

func TestNotificationsdbDialect(T *testing.T) {
	T.Parallel()

	T.Run("maps every dialect this module supports", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			mapped, err := notificationsdbDialect(d)
			must.NoError(t, err, must.Sprintf("dialect %s", d))
			test.NotEqOp(t, "", string(mapped))
		}
	})

	T.Run("names a dialect the querier was not generated for", func(t *testing.T) {
		t.Parallel()

		_, err := notificationsdbDialect(dialect.Dialect("cassandra"))
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}
