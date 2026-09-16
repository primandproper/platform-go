package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientsmock "github.com/primandproper/platform-go/v14/authentication/oauth2clients/mock"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/privacy"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two registries a subject's clients might be in. Two, because the whole of
// what a ScopeResolver decides is which of them a request reaches.
var (
	firstScope  = tenancy.Of("acct_1")
	secondScope = tenancy.Of("acct_2")

	// testScope is the confinement a request arrives with, which the fulfiller
	// hands the collector and the eraser beside the subject.
	testScope = firstScope
)

// subject is the person these tests are about. Their id is the owner the
// registry keys on, which is the whole of the mapping this package makes.
var subject = dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}

// errStoreUnavailable stands in for a store that cannot answer.
var errStoreUnavailable = platformerrors.New("the store is unavailable")

// pageOf returns one full page of registrations, as a store would.
func pageOf(registered ...*oauth2clients.Client) *filtering.QueryFilteredResult[oauth2clients.Client] {
	return filtering.NewQueryFilteredResult(registered, uint64(len(registered)), uint64(len(registered)),
		func(c *oauth2clients.Client) string { return c.ID }, filtering.DefaultQueryFilter())
}

// clientIn is one registration, in a registry, carrying a secret digest so the
// redaction has something to clear.
func clientIn(scope tenancy.Scope, id string) *oauth2clients.Client {
	return &oauth2clients.Client{
		ID:            id,
		Scope:         scope,
		BelongsToUser: subject.ID,
		ClientID:      "cid_" + id,
		SecretHash:    "deadbeefdeadbeefdeadbeefdeadbeef",
		Name:          "Ada's recipe importer",
		Description:   "imports recipes from elsewhere",
		RedirectURIs:  []string{"https://example.test/callback"},
		Scopes:        []string{"recipes:read"},
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
		// registry would be well-formed, would have a section, and would be
		// missing every application the subject registered.
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
		// have the registries it erases decided by whoever mutated that slice
		// next.
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
		_, err = privacy.NewCollector(&oauth2clientsmock.StoreMock{}, nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewCollector(&oauth2clientsmock.StoreMock{}, &testReader{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("collects every registry the resolver names", func(t *testing.T) {
		t.Parallel()

		var reader database.SQLQueryExecutor = &testReader{}

		store := &oauth2clientsmock.StoreMock{
			ListClientsForOwnerFunc: func(
				_ context.Context, q database.SQLQueryExecutor, scope tenancy.Scope,
				userID string, filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
				// The executor is the one the collector was built with, which is
				// the whole of what the constructor argument buys.
				test.EqOp(t, reader, q)
				test.EqOp(t, subject.ID, userID)

				// Withdrawn registrations are asked for: the row is still there,
				// and it is still something the subject registered.
				must.NotNil(t, filter.IncludeArchived)
				test.True(t, *filter.IncludeArchived)

				return pageOf(clientIn(scope, "client_in_"+scope.String())), nil
			},
		}

		collector, err := privacy.NewCollector(store, reader, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []oauth2clients.Client
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)
		test.EqOp(t, "client_in_acct_1", collected[0].ID)
		test.EqOp(t, "client_in_acct_2", collected[1].ID)

		// The public identifier goes, because it is a public identifier its
		// owner already has and sends on every authorization request.
		test.EqOp(t, "cid_client_in_acct_1", collected[0].ClientID)
		test.Eq(t, []string{"recipes:read"}, collected[0].Scopes)
	})

	T.Run("the digest is not in the artifact", func(t *testing.T) {
		t.Parallel()

		// The one column that would make a subject access request a credential
		// dump. It is cleared by Redacted and carries json:"-" as well, so this
		// asserts against the bytes rather than against the struct.
		store := &oauth2clientsmock.StoreMock{
			ListClientsForOwnerFunc: func(
				_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope,
				_ string, _ *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
				return pageOf(clientIn(scope, "client_1")), nil
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		test.StrNotContains(t, string(fragment), "deadbeef")
		test.StrNotContains(t, string(fragment), "secretHash")
		test.StrNotContains(t, string(fragment), "SecretHash")

		var collected []oauth2clients.Client
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 1, collected)
		test.EqOp(t, "", collected[0].SecretHash)
	})

	T.Run("does not clear the digest on the row the store handed back", func(t *testing.T) {
		t.Parallel()

		// Redacted copies. A collector that blanked the field in place would be
		// reaching into a value its caller may still be holding — and if a store
		// ever cached its rows, into the cache.
		held := clientIn(firstScope, "client_1")

		store := &oauth2clientsmock.StoreMock{
			ListClientsForOwnerFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
				return pageOf(held), nil
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		test.EqOp(t, "deadbeefdeadbeefdeadbeefdeadbeef", held.SecretHash)
	})

	T.Run("a subject who registered nothing is a domain holding nothing", func(t *testing.T) {
		t.Parallel()

		// nil, nil is how a collector says "no data here", and the section is
		// then omitted from the artifact rather than written as an empty list an
		// export reads as a form.
		store := &oauth2clientsmock.StoreMock{
			ListClientsForOwnerFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
				return pageOf(), nil
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

		store := &oauth2clientsmock.StoreMock{}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes())
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
		test.SliceEmpty(t, store.ListClientsForOwnerCalls())
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		collector, err := privacy.NewCollector(&oauth2clientsmock.StoreMock{}, &testReader{}, privacy.RequestScope)
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
		store := &oauth2clientsmock.StoreMock{
			ListClientsForOwnerFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[oauth2clients.Client], error) {
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

		_, err = privacy.NewEraser(&oauth2clientsmock.StoreMock{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestEraser_Erase(T *testing.T) {
	T.Parallel()

	T.Run("sums what it destroyed across every registry", func(t *testing.T) {
		t.Parallel()

		store := &oauth2clientsmock.StoreMock{
			DeleteClientsForOwnerFunc: func(
				_ context.Context, q database.Tx, scope tenancy.Scope, userID string,
			) (int64, error) {
				// The executor is the request's, so the registrations and the
				// rest of the subject's footprint commit or roll back together.
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

			// Nothing is retained, so nothing is reported as retained. There is
			// no anonymization to fall back to: the name and the description are
			// free text somebody typed, which is the part that identifies people.
			test.MapEmpty(t, outcome.Retained)
		})
	})

	T.Run("deletes rather than withdrawing", func(t *testing.T) {
		t.Parallel()

		// The ruling, asserted against the store rather than read off the doc
		// comment: an eraser that reached for ArchiveClient would leave
		// belongs_to_user, the name and the description on every row it touched.
		store := &oauth2clientsmock.StoreMock{
			DeleteClientsForOwnerFunc: func(
				context.Context, database.Tx, tenancy.Scope, string,
			) (int64, error) {
				return 1, nil
			},
		}

		eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
		})

		test.SliceLen(t, 1, store.DeleteClientsForOwnerCalls())
		test.SliceEmpty(t, store.ArchiveClientCalls())
	})

	T.Run("a subject who registered nothing is not a failure", func(t *testing.T) {
		t.Parallel()

		store := &oauth2clientsmock.StoreMock{
			DeleteClientsForOwnerFunc: func(
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

		eraser, err := privacy.NewEraser(&oauth2clientsmock.StoreMock{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, testScope, subject)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewEraser(&oauth2clientsmock.StoreMock{}, privacy.RequestScope)
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, tenancy.Scope{}, subject)
			must.ErrorIs(t, eraseErr, privacy.ErrUnscopedRequest)
		})
	})

	T.Run("reports a delete that failed rather than a partial total", func(t *testing.T) {
		t.Parallel()

		store := &oauth2clientsmock.StoreMock{
			DeleteClientsForOwnerFunc: func(
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

	collector, err := privacy.NewCollector(&oauth2clientsmock.StoreMock{}, &testReader{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterCollector(privacy.DefaultKey, collector))

	eraser, err := privacy.NewEraser(&oauth2clientsmock.StoreMock{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterEraser(privacy.DefaultKey, eraser))

	test.Eq(t, []string{privacy.DefaultKey}, registry.CollectorKeys())
	test.Eq(t, []string{privacy.DefaultKey}, registry.EraserKeys())
}
