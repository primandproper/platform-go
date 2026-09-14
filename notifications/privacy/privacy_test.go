package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationsmock "github.com/primandproper/platform-go/v14/notifications/mock"
	"github.com/primandproper/platform-go/v14/notifications/privacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two directories a subject's notifications might be in. Two, because the
// whole of what a ScopeResolver decides is which of them a request reaches.
var (
	firstScope  = tenancy.Of("acct_1")
	secondScope = tenancy.Of("acct_2")

	// testScope is the confinement a request arrives with, which the fulfiller
	// hands a collector and an eraser beside the subject.
	testScope = firstScope
)

// subject is the person these tests are about. Their id is the principal both
// tables key on, which is the whole of the mapping this package makes.
var subject = dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}

// errStoreUnavailable stands in for a store that cannot answer.
var errStoreUnavailable = platformerrors.New("the store is unavailable")

// inboxPageOf returns one full page of notifications, as an inbox would.
func inboxPageOf(told ...*notifications.Notification) *filtering.QueryFilteredResult[notifications.Notification] {
	return filtering.NewQueryFilteredResult(told, uint64(len(told)), uint64(len(told)),
		func(n *notifications.Notification) string { return n.ID }, filtering.DefaultQueryFilter())
}

// devicePageOf returns one full page of registrations, as a registry would.
func devicePageOf(held ...*notifications.Device) *filtering.QueryFilteredResult[notifications.Device] {
	return filtering.NewQueryFilteredResult(held, uint64(len(held)), uint64(len(held)),
		func(d *notifications.Device) string { return d.ID }, filtering.DefaultQueryFilter())
}

// notificationIn is one stored notification, in a scope.
func notificationIn(scope tenancy.Scope, id string) *notifications.Notification {
	return &notifications.Notification{
		ID:        id,
		Scope:     scope,
		Principal: subject.ID,
		Topic:     "order.shipped",
		Title:     "Your order shipped",
		Body:      "it is on its way",
		Link:      "/orders/1",
	}
}

// deviceIn is one stored registration, in a scope.
func deviceIn(scope tenancy.Scope, id string) *notifications.Device {
	return &notifications.Device{
		ID:        id,
		Scope:     scope,
		Principal: subject.ID,
		Platform:  notifications.PlatformIOS,
		Token:     "token-for-" + id,
	}
}

// testReader is the executor a collector is built over.
//
// Nothing executes through it: the store beneath the collector is a mock, and
// what these tests assert is that the executor the collector was built with is
// the one it passes down. database.SQLQueryExecutor is an interface with no
// unexported methods, so unlike database.Tx a test can stand in for it.
type testReader struct{}

var _ database.SQLQueryExecutor = (*testReader)(nil)

func (*testReader) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("the collector's store is a mock; nothing runs on this")
}

func (*testReader) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("the collector's store is a mock; nothing runs on this")
}

// reader is the executor every collector below is built over, named once so a
// test can assert that it is the one the collector passed down.
var reader = &testReader{}

// withTx runs fn inside a real transaction.
//
// database.Tx carries an unexported method, so it is producible only by the
// database package — which is the point of the type, and which means a test that
// wants one opens a database rather than standing in for it. Nothing here
// executes a statement through it: the store beneath the eraser is a mock, and
// what is being asserted is that the executor the eraser was handed is the one
// it passes down.
func withTx(t *testing.T, fn func(q database.Tx)) {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "privacy.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
		fn(q)

		return nil
	}))
}

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func TestRequestScope(T *testing.T) {
	T.Parallel()

	T.Run("resolves the scope the request names", func(t *testing.T) {
		t.Parallel()

		scopes, err := privacy.RequestScope(t.Context(), tenancy.Of("acct_1"), subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Of("acct_1")}, scopes)
	})

	T.Run("refuses a request that names none", func(t *testing.T) {
		t.Parallel()

		// Not the global scope. An export that quietly covered only the global
		// scope would be well-formed, would have a section, and would be missing
		// every notification the subject was ever sent.
		_, err := privacy.RequestScope(t.Context(), tenancy.Scope{}, subject)
		must.ErrorIs(t, err, privacy.ErrUnscopedRequest)
	})
}

func TestFixedScopes(T *testing.T) {
	T.Parallel()

	T.Run("resolves every subject to the same scopes", func(t *testing.T) {
		t.Parallel()

		scopes, err := privacy.FixedScopes(tenancy.Global())(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{tenancy.Global()}, scopes)
	})

	T.Run("keeps its own copy of what it was given", func(t *testing.T) {
		t.Parallel()

		// The caller's slice is the caller's. A resolver that aliased it would
		// have the scopes it erases decided by whoever mutated that slice next.
		given := []tenancy.Scope{firstScope, secondScope}
		resolve := privacy.FixedScopes(given...)
		given[0] = tenancy.Of("somebody_else")

		scopes, err := resolve(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Eq(t, []tenancy.Scope{firstScope, secondScope}, scopes)
	})
}

func TestNewInboxCollector(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewInboxCollector(nil, reader, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilInbox)

		// The executor is required for the same reason the eraser's is: the
		// store keeps no connection of its own, so a collector without one has
		// nothing to read on.
		_, err = privacy.NewInboxCollector(&notificationsmock.InboxMock{}, nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewInboxCollector(&notificationsmock.InboxMock{}, reader, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestInboxCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("collects every scope the resolver names, dismissed notifications included", func(t *testing.T) {
		t.Parallel()

		inbox := &notificationsmock.InboxMock{
			ListNotificationsFunc: func(
				_ context.Context,
				q database.SQLQueryExecutor,
				scope tenancy.Scope,
				principal string,
				filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[notifications.Notification], error) {
				// The executor is the one the collector was built with, since an
				// export is a read with no transaction to be part of.
				test.EqOp(t, database.SQLQueryExecutor(reader), q)

				// The subject's id is the principal, which is the whole of the
				// mapping: notifications does not own the directory.
				test.EqOp(t, subject.ID, principal)

				// A dismissed notification still holds the title, the body and
				// the link somebody was sent, and an export that showed only
				// what they had not yet cleared would answer a different
				// question than the right of access asks.
				must.NotNil(t, filter)
				must.NotNil(t, filter.IncludeArchived)
				test.True(t, *filter.IncludeArchived)

				return inboxPageOf(notificationIn(scope, "notif_in_"+scope.String())), nil
			},
		}

		collector, err := privacy.NewInboxCollector(inbox, reader, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []notifications.Notification
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)
		test.EqOp(t, "notif_in_acct_1", collected[0].ID)
		test.EqOp(t, "notif_in_acct_2", collected[1].ID)

		// The title and the body are in the artifact, which is what makes the
		// erasure a delete: they are the part that identifies people.
		test.EqOp(t, "Your order shipped", collected[0].Title)
		test.EqOp(t, "it is on its way", collected[0].Body)
	})

	T.Run("asks on a copy of the filter it was handed", func(t *testing.T) {
		t.Parallel()

		// dataprivacy.CollectAll owns the filter it pages with — the cursor it
		// advances between pages is on it — so a collector that wrote
		// IncludeArchived through the pointer would be editing somebody else's
		// value to ask its own question.
		var handed *filtering.QueryFilter

		inbox := &notificationsmock.InboxMock{
			ListNotificationsFunc: func(
				_ context.Context, _ database.SQLQueryExecutor, _ tenancy.Scope, _ string,
				filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[notifications.Notification], error) {
				handed = filter

				return inboxPageOf(), nil
			},
		}

		collector, err := privacy.NewInboxCollector(inbox, reader, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		must.NotNil(t, handed)
		must.NotNil(t, handed.IncludeArchived)
		test.True(t, *handed.IncludeArchived)
	})

	T.Run("a subject who was never told anything is a domain holding nothing", func(t *testing.T) {
		t.Parallel()

		// nil, nil is how a collector says "no data here", and the section is
		// then omitted from the artifact rather than written as an empty list an
		// export reads as a form.
		inbox := &notificationsmock.InboxMock{
			ListNotificationsFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[notifications.Notification], error) {
				return inboxPageOf(), nil
			},
		}

		collector, err := privacy.NewInboxCollector(inbox, reader, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
	})

	T.Run("no scopes is no reads", func(t *testing.T) {
		t.Parallel()

		inbox := &notificationsmock.InboxMock{}

		collector, err := privacy.NewInboxCollector(inbox, reader, privacy.FixedScopes())
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
		test.SliceEmpty(t, inbox.ListNotificationsCalls())
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		collector, err := privacy.NewInboxCollector(&notificationsmock.InboxMock{}, reader, privacy.RequestScope)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.ErrorIs(t, err, privacy.ErrUnscopedRequest)
	})

	T.Run("reports a read that failed rather than a short export", func(t *testing.T) {
		t.Parallel()

		// A collector must not return partially-collected data alongside an
		// error: the fragment is used or the error is recorded, and a truncated
		// subject access request looks exactly like a correct one.
		inbox := &notificationsmock.InboxMock{
			ListNotificationsFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[notifications.Notification], error) {
				return nil, errStoreUnavailable
			},
		}

		collector, err := privacy.NewInboxCollector(inbox, reader, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.ErrorIs(t, err, errStoreUnavailable)
		test.Nil(t, fragment)
	})
}

func TestNewInboxEraser(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewInboxEraser(nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilInbox)

		_, err = privacy.NewInboxEraser(&notificationsmock.InboxMock{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestInboxEraser_Erase(T *testing.T) {
	T.Parallel()

	T.Run("sums what it destroyed across every scope", func(t *testing.T) {
		t.Parallel()

		inbox := &notificationsmock.InboxMock{
			DeleteNotificationsForPrincipalFunc: func(
				_ context.Context, tx database.Tx, scope tenancy.Scope, principal string,
			) (int64, error) {
				// The executor is the request's, so the notifications and the
				// rest of the subject's footprint commit or roll back together.
				test.NotNil(t, tx)
				test.EqOp(t, subject.ID, principal)

				if scope == firstScope {
					return 2, nil
				}

				return 3, nil
			},
		}

		eraser, err := privacy.NewInboxEraser(inbox, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
			test.EqOp(t, int64(5), outcome.Deleted)
			test.EqOp(t, int64(0), outcome.Anonymized)

			// Nothing is retained, so nothing is reported as retained. There is
			// no anonymization to fall back to: stripping the principal off a
			// notification leaves the title, the body and the link, which is the
			// part that identifies people.
			test.MapEmpty(t, outcome.Retained)
		})
	})

	T.Run("it destroys rather than archiving", func(t *testing.T) {
		t.Parallel()

		// The archive is the write somebody reaches for when they mean "get rid
		// of it", and it leaves everything a notification says about a person.
		// This asserts the eraser calls neither it nor the single-row write that
		// would have needed an id it does not have.
		inbox := &notificationsmock.InboxMock{
			DeleteNotificationsForPrincipalFunc: func(
				context.Context, database.Tx, tenancy.Scope, string,
			) (int64, error) {
				return 1, nil
			},
		}

		eraser, err := privacy.NewInboxEraser(inbox, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
		})

		test.SliceLen(t, 1, inbox.DeleteNotificationsForPrincipalCalls())
		test.SliceEmpty(t, inbox.ArchiveNotificationCalls())
	})

	T.Run("a subject who was never told anything is not a failure", func(t *testing.T) {
		t.Parallel()

		inbox := &notificationsmock.InboxMock{
			DeleteNotificationsForPrincipalFunc: func(
				context.Context, database.Tx, tenancy.Scope, string,
			) (int64, error) {
				return 0, nil
			},
		}

		eraser, err := privacy.NewInboxEraser(inbox, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
			test.EqOp(t, int64(0), outcome.Deleted)
		})
	})

	T.Run("refuses to run outside a transaction", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewInboxEraser(&notificationsmock.InboxMock{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, testScope, subject)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewInboxEraser(&notificationsmock.InboxMock{}, privacy.RequestScope)
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, tenancy.Scope{}, subject)
			must.ErrorIs(t, eraseErr, privacy.ErrUnscopedRequest)
		})
	})

	T.Run("reports a delete that failed rather than a partial total", func(t *testing.T) {
		t.Parallel()

		inbox := &notificationsmock.InboxMock{
			DeleteNotificationsForPrincipalFunc: func(
				_ context.Context, _ database.Tx, scope tenancy.Scope, _ string,
			) (int64, error) {
				if scope == firstScope {
					return 2, nil
				}

				return 0, errStoreUnavailable
			},
		}

		eraser, err := privacy.NewInboxEraser(inbox, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.ErrorIs(t, eraseErr, errStoreUnavailable)
			test.EqOp(t, int64(0), outcome.Deleted)
		})
	})
}

func TestNewDeviceCollector(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewDeviceCollector(nil, reader, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilRegistry)

		_, err = privacy.NewDeviceCollector(&notificationsmock.RegistryMock{}, nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewDeviceCollector(&notificationsmock.RegistryMock{}, reader, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestDeviceCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("collects every scope's registrations, tokens included", func(t *testing.T) {
		t.Parallel()

		registry := &notificationsmock.RegistryMock{
			ListDevicesFunc: func(
				_ context.Context,
				q database.SQLQueryExecutor,
				scope tenancy.Scope,
				principal string,
				filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[notifications.Device], error) {
				test.EqOp(t, database.SQLQueryExecutor(reader), q)
				test.EqOp(t, subject.ID, principal)

				// No IncludeArchived: this table has no archived_at, because a
				// token is revoked or invalidated rather than stamped, so every
				// row it holds is a live one.
				must.NotNil(t, filter)
				test.Nil(t, filter.IncludeArchived)

				return devicePageOf(deviceIn(scope, "device_in_"+scope.String())), nil
			},
		}

		collector, err := privacy.NewDeviceCollector(registry, reader, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []notifications.Device
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)
		test.EqOp(t, "device_in_acct_1", collected[0].ID)
		test.EqOp(t, "device_in_acct_2", collected[1].ID)

		// The token is in the artifact rather than redacted out of it. The right
		// of access is a right to what the table holds about the person asking,
		// and the token is the row's most consequential fact about them — see the
		// package documentation, which is where a deployment that disagrees is
		// told to register no device collector.
		test.EqOp(t, "token-for-device_in_acct_1", collected[0].Token)
	})

	T.Run("a subject with no handsets is a domain holding nothing", func(t *testing.T) {
		t.Parallel()

		registry := &notificationsmock.RegistryMock{
			ListDevicesFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[notifications.Device], error) {
				return devicePageOf(), nil
			},
		}

		collector, err := privacy.NewDeviceCollector(registry, reader, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
	})

	T.Run("no scopes is no reads", func(t *testing.T) {
		t.Parallel()

		registry := &notificationsmock.RegistryMock{}

		collector, err := privacy.NewDeviceCollector(registry, reader, privacy.FixedScopes())
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
		test.SliceEmpty(t, registry.ListDevicesCalls())
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		collector, err := privacy.NewDeviceCollector(&notificationsmock.RegistryMock{}, reader, privacy.RequestScope)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.ErrorIs(t, err, privacy.ErrUnscopedRequest)
	})

	T.Run("reports a read that failed rather than a short export", func(t *testing.T) {
		t.Parallel()

		registry := &notificationsmock.RegistryMock{
			ListDevicesFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[notifications.Device], error) {
				return nil, errStoreUnavailable
			},
		}

		collector, err := privacy.NewDeviceCollector(registry, reader, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.ErrorIs(t, err, errStoreUnavailable)
		test.Nil(t, fragment)
	})
}

func TestNewDeviceEraser(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewDeviceEraser(nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilRegistry)

		_, err = privacy.NewDeviceEraser(&notificationsmock.RegistryMock{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestDeviceEraser_Erase(T *testing.T) {
	T.Parallel()

	T.Run("sums what it removed across every scope", func(t *testing.T) {
		t.Parallel()

		registry := &notificationsmock.RegistryMock{
			DeleteDevicesForPrincipalFunc: func(
				_ context.Context, tx database.Tx, scope tenancy.Scope, principal string,
			) (int64, error) {
				test.NotNil(t, tx)
				test.EqOp(t, subject.ID, principal)

				if scope == firstScope {
					return 1, nil
				}

				return 2, nil
			},
		}

		eraser, err := privacy.NewDeviceEraser(registry, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
			test.EqOp(t, int64(3), outcome.Deleted)
			test.EqOp(t, int64(0), outcome.Anonymized)
			test.MapEmpty(t, outcome.Retained)
		})
	})

	T.Run("it tells no provider, because deleting the row is what stops the push", func(t *testing.T) {
		t.Parallel()

		// The provider hook runs the other way round: APNs and FCM learn a token
		// is dead by being pushed to and rejecting it. An eraser that called it
		// would be unscoped machinery running inside a scoped request, on rows it
		// has just deleted.
		registry := &notificationsmock.RegistryMock{
			DeleteDevicesForPrincipalFunc: func(
				context.Context, database.Tx, tenancy.Scope, string,
			) (int64, error) {
				return 1, nil
			},
		}

		eraser, err := privacy.NewDeviceEraser(registry, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
		})

		test.SliceLen(t, 1, registry.DeleteDevicesForPrincipalCalls())
		test.SliceEmpty(t, registry.InvalidateDeviceTokenCalls())
		test.SliceEmpty(t, registry.RevokeDeviceCalls())
	})

	T.Run("a subject with no handsets is not a failure", func(t *testing.T) {
		t.Parallel()

		registry := &notificationsmock.RegistryMock{
			DeleteDevicesForPrincipalFunc: func(
				context.Context, database.Tx, tenancy.Scope, string,
			) (int64, error) {
				return 0, nil
			},
		}

		eraser, err := privacy.NewDeviceEraser(registry, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
			test.EqOp(t, int64(0), outcome.Deleted)
		})
	})

	T.Run("refuses to run outside a transaction", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewDeviceEraser(&notificationsmock.RegistryMock{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, testScope, subject)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewDeviceEraser(&notificationsmock.RegistryMock{}, privacy.RequestScope)
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, tenancy.Scope{}, subject)
			must.ErrorIs(t, eraseErr, privacy.ErrUnscopedRequest)
		})
	})

	T.Run("reports a delete that failed rather than a partial total", func(t *testing.T) {
		t.Parallel()

		registry := &notificationsmock.RegistryMock{
			DeleteDevicesForPrincipalFunc: func(
				_ context.Context, _ database.Tx, scope tenancy.Scope, _ string,
			) (int64, error) {
				if scope == firstScope {
					return 1, nil
				}

				return 0, errStoreUnavailable
			},
		}

		eraser, err := privacy.NewDeviceEraser(registry, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.ErrorIs(t, eraseErr, errStoreUnavailable)
			test.EqOp(t, int64(0), outcome.Deleted)
		})
	})
}

// TestRegistersUnderTheDefaultKeys exercises both constants against the thing
// that validates them, and pins the shape of this package that is a decision:
// two keys rather than one.
//
// A registry refuses a key it does not like, and these two carry a dot — which
// dataprivacy's own key rule allows as a segment separator — so declaring them is
// not the same as knowing they register. It also asserts what the split buys: a
// deployment with no mobile app registers the inbox pair and nothing else, and
// the registry is content with an eraser key that has no collector beside it.
func TestRegistersUnderTheDefaultKeys(T *testing.T) {
	T.Parallel()

	T.Run("all four, under two keys", func(t *testing.T) {
		t.Parallel()

		registry := dataprivacy.NewRegistry()

		inboxCollector, err := privacy.NewInboxCollector(&notificationsmock.InboxMock{}, reader, privacy.RequestScope)
		must.NoError(t, err)
		must.NoError(t, registry.RegisterCollector(privacy.DefaultInboxKey, inboxCollector))

		inboxEraser, err := privacy.NewInboxEraser(&notificationsmock.InboxMock{}, privacy.RequestScope)
		must.NoError(t, err)
		must.NoError(t, registry.RegisterEraser(privacy.DefaultInboxKey, inboxEraser))

		deviceCollector, err := privacy.NewDeviceCollector(
			&notificationsmock.RegistryMock{}, reader, privacy.RequestScope)
		must.NoError(t, err)
		must.NoError(t, registry.RegisterCollector(privacy.DefaultDeviceKey, deviceCollector))

		deviceEraser, err := privacy.NewDeviceEraser(&notificationsmock.RegistryMock{}, privacy.RequestScope)
		must.NoError(t, err)
		must.NoError(t, registry.RegisterEraser(privacy.DefaultDeviceKey, deviceEraser))

		// Sorted, so the devices key comes first.
		want := []string{privacy.DefaultDeviceKey, privacy.DefaultInboxKey}
		test.Eq(t, want, registry.CollectorKeys())
		test.Eq(t, want, registry.EraserKeys())
	})

	T.Run("the inbox pair alone, for a deployment with no handsets", func(t *testing.T) {
		t.Parallel()

		// This is the whole reason there are two pairs. A service with a bell
		// icon and no mobile app implements notifications.Inbox and nothing else,
		// and one adapter over notifications.Store would have made it hold a
		// registry it does not have.
		registry := dataprivacy.NewRegistry()

		collector, err := privacy.NewInboxCollector(&notificationsmock.InboxMock{}, reader, privacy.RequestScope)
		must.NoError(t, err)
		must.NoError(t, registry.RegisterCollector(privacy.DefaultInboxKey, collector))

		eraser, err := privacy.NewInboxEraser(&notificationsmock.InboxMock{}, privacy.RequestScope)
		must.NoError(t, err)
		must.NoError(t, registry.RegisterEraser(privacy.DefaultInboxKey, eraser))

		test.Eq(t, []string{privacy.DefaultInboxKey}, registry.CollectorKeys())
		test.Eq(t, []string{privacy.DefaultInboxKey}, registry.EraserKeys())
	})

	T.Run("an erasure with no export, which is the asymmetry the registry allows", func(t *testing.T) {
		t.Parallel()

		// A deployment that considers a push token too sensitive to put in a file
		// that leaves the building registers the device eraser and no device
		// collector. The two key namespaces are independent, so that is a
		// configuration rather than a hole.
		registry := dataprivacy.NewRegistry()

		eraser, err := privacy.NewDeviceEraser(&notificationsmock.RegistryMock{}, privacy.RequestScope)
		must.NoError(t, err)
		must.NoError(t, registry.RegisterEraser(privacy.DefaultDeviceKey, eraser))

		test.SliceEmpty(t, registry.CollectorKeys())
		test.Eq(t, []string{privacy.DefaultDeviceKey}, registry.EraserKeys())
	})
}
