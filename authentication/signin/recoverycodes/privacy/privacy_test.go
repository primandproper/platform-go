package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes"
	recoverycodesmock "github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/mock"
	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/privacy"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two scopes a subject's recovery codes might be in. Two, because the whole of
// what a ScopeResolver decides is which of them a request reaches.
var (
	firstScope  = tenancy.Of("acct_1")
	secondScope = tenancy.Of("acct_2")

	// testScope is the confinement a request arrives with, which the fulfiller
	// hands the collector and the eraser beside the subject.
	testScope = firstScope
)

// subject is the person these tests are about. Their id is the principal the
// table keys on, which is the whole of the mapping this package makes.
var subject = dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}

// errStoreUnavailable stands in for a store that cannot answer.
var errStoreUnavailable = platformerrors.New("the store is unavailable")

// codeIn is one stored code, in a scope, spent where spent is true.
func codeIn(scope tenancy.Scope, spent bool) *recoverycodes.RecoveryCode {
	code := &recoverycodes.RecoveryCode{
		Scope:    scope,
		UserID:   subject.ID,
		IssuedAt: time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC),
	}

	if spent {
		usedAt := time.Date(2026, time.September, 20, 9, 0, 0, 0, time.UTC)
		code.UsedAt = &usedAt
	}

	return code
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
		// every recovery code the subject ever held.
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

func TestNewCollector(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewCollector(nil, &testReader{}, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilStore)

		// The executor is a constructor argument because Collect has nowhere to
		// put one, so an absent one has to be refused here or every collection
		// fails at the store instead.
		_, err = privacy.NewCollector(&recoverycodesmock.StoreMock{}, nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewCollector(&recoverycodesmock.StoreMock{}, &testReader{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("collects every scope the resolver names", func(t *testing.T) {
		t.Parallel()

		var reader database.SQLQueryExecutor = &testReader{}

		store := &recoverycodesmock.StoreMock{
			ListForUserFunc: func(
				_ context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, userID string,
			) ([]*recoverycodes.RecoveryCode, error) {
				// The executor is the one the collector was built with, which is
				// the whole of what the constructor argument buys.
				test.EqOp(t, reader, q)
				test.EqOp(t, subject.ID, userID)

				return []*recoverycodes.RecoveryCode{codeIn(scope, scope == secondScope)}, nil
			},
		}

		collector, err := privacy.NewCollector(store, reader, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []recoverycodes.RecoveryCode
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)
		test.EqOp(t, firstScope, collected[0].Scope)
		test.EqOp(t, secondScope, collected[1].Scope)

		// What a subject is entitled to: that they held a code, when it was
		// issued, and whether and when they spent it.
		test.False(t, collected[0].IssuedAt.IsZero())
		test.Nil(t, collected[0].UsedAt)
		test.NotNil(t, collected[1].UsedAt)
	})

	T.Run("carries nothing that could stand in for a second factor", func(t *testing.T) {
		t.Parallel()

		// The store's projection excludes the hash, so there is no field on
		// RecoveryCode for a code or a digest to arrive in. This asserts it
		// against the bytes the artifact would carry.
		store := &recoverycodesmock.StoreMock{
			ListForUserFunc: func(
				_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, _ string,
			) ([]*recoverycodes.RecoveryCode, error) {
				return []*recoverycodes.RecoveryCode{codeIn(scope, false)}, nil
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		test.StrNotContains(t, string(fragment), "hash")
		test.StrNotContains(t, string(fragment), "digest")
		test.StrNotContains(t, string(fragment), "code\"")
	})

	T.Run("skips a nil row rather than panicking on it", func(t *testing.T) {
		t.Parallel()

		// The store's own read never produces one, but the seam is an interface
		// and an implementation that is not this module's might.
		store := &recoverycodesmock.StoreMock{
			ListForUserFunc: func(
				_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope, _ string,
			) ([]*recoverycodes.RecoveryCode, error) {
				return []*recoverycodes.RecoveryCode{nil, codeIn(scope, false), nil}, nil
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []recoverycodes.RecoveryCode
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 1, collected)
		test.EqOp(t, subject.ID, collected[0].UserID)
	})

	T.Run("a subject who never held a set is a domain holding nothing", func(t *testing.T) {
		t.Parallel()

		// nil, nil is how a collector says "no data here", and the section is
		// then omitted from the artifact rather than written as an empty list an
		// export reads as a form.
		store := &recoverycodesmock.StoreMock{
			ListForUserFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
			) ([]*recoverycodes.RecoveryCode, error) {
				return nil, nil
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
	})

	T.Run("no scopes is no reads", func(t *testing.T) {
		t.Parallel()

		store := &recoverycodesmock.StoreMock{}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes())
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
		test.SliceEmpty(t, store.ListForUserCalls())
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		collector, err := privacy.NewCollector(&recoverycodesmock.StoreMock{}, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		// The confinement is the argument now, so the request that named none
		// is the zero Scope rather than a subject with an empty field.
		_, err = collector.Collect(t.Context(), tenancy.Scope{}, subject)
		must.ErrorIs(t, err, privacy.ErrUnscopedRequest)
	})

	T.Run("reports a read that failed rather than a short export", func(t *testing.T) {
		t.Parallel()

		// A collector must not return partially-collected data alongside an
		// error: the fragment is used or the error is recorded, and a truncated
		// subject access request looks exactly like a correct one.
		store := &recoverycodesmock.StoreMock{
			ListForUserFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
			) ([]*recoverycodes.RecoveryCode, error) {
				return nil, errStoreUnavailable
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.ErrorIs(t, err, errStoreUnavailable)
		test.Nil(t, fragment)
	})
}

func TestNewEraser(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewEraser(nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilStore)

		_, err = privacy.NewEraser(&recoverycodesmock.StoreMock{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestEraser_Erase(T *testing.T) {
	T.Parallel()

	T.Run("sums what it destroyed across every scope", func(t *testing.T) {
		t.Parallel()

		store := &recoverycodesmock.StoreMock{
			DeleteForUserFunc: func(
				_ context.Context, q database.Tx, scope tenancy.Scope, userID string,
			) (int64, error) {
				// The executor is the request's, so the codes and the rest of
				// the subject's footprint commit or roll back together.
				test.NotNil(t, q)
				test.EqOp(t, subject.ID, userID)

				if scope == firstScope {
					return 2, nil
				}

				return 3, nil
			},
		}

		eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
			test.EqOp(t, int64(5), outcome.Deleted)
			test.EqOp(t, int64(0), outcome.Anonymized)
			test.MapEmpty(t, outcome.Retained)
		})
	})

	T.Run("a subject who never held a set is not a failure", func(t *testing.T) {
		t.Parallel()

		store := &recoverycodesmock.StoreMock{
			DeleteForUserFunc: func(
				context.Context, database.Tx, tenancy.Scope, string,
			) (int64, error) {
				return 0, nil
			},
		}

		eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
			test.EqOp(t, int64(0), outcome.Deleted)
		})
	})

	T.Run("refuses to run outside a transaction", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewEraser(&recoverycodesmock.StoreMock{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, testScope, subject)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewEraser(&recoverycodesmock.StoreMock{}, privacy.RequestScope)
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, tenancy.Scope{}, subject)
			must.ErrorIs(t, eraseErr, privacy.ErrUnscopedRequest)
		})
	})

	T.Run("reports a delete that failed rather than a partial total", func(t *testing.T) {
		t.Parallel()

		store := &recoverycodesmock.StoreMock{
			DeleteForUserFunc: func(
				_ context.Context, _ database.Tx, scope tenancy.Scope, _ string,
			) (int64, error) {
				if scope == firstScope {
					return 2, nil
				}

				return 0, errStoreUnavailable
			},
		}

		eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.ErrorIs(t, eraseErr, errStoreUnavailable)
			test.EqOp(t, int64(0), outcome.Deleted)
		})
	})
}

func TestRegistersUnderTheDefaultKey(t *testing.T) {
	t.Parallel()

	// The key names the section an export's artifact carries these in, and a
	// registry refuses one it does not like — so the constant is exercised
	// against the thing that validates it rather than merely declared.
	registry := dataprivacy.NewRegistry()

	collector, err := privacy.NewCollector(&recoverycodesmock.StoreMock{}, &testReader{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterCollector(privacy.DefaultKey, collector))

	eraser, err := privacy.NewEraser(&recoverycodesmock.StoreMock{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterEraser(privacy.DefaultKey, eraser))

	test.Eq(t, []string{privacy.DefaultKey}, registry.CollectorKeys())
	test.Eq(t, []string{privacy.DefaultKey}, registry.EraserKeys())
}
