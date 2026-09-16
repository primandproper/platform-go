package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/settings"
	settingsmock "github.com/primandproper/platform-go/v14/settings/mock"
	"github.com/primandproper/platform-go/v14/settings/privacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two directories a subject's settings might be in. Two, because the whole
// of what a ScopeResolver decides is which of them a request reaches.
var (
	firstScope  = tenancy.Of("acct_1")
	secondScope = tenancy.Of("acct_2")

	// testScope is the confinement a request arrives with, which the fulfiller
	// hands the collector and the eraser beside the subject.
	testScope = firstScope
)

// subject is the person these tests are about.
var subject = dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}

// held is the same person as the store keys values on, which is the conversion
// TestSubjectVocabulariesAgree is about.
var held = settings.Subject{Type: settings.SubjectUser, ID: subject.ID}

// errStoreUnavailable stands in for a store that cannot answer.
var errStoreUnavailable = platformerrors.New("the store is unavailable")

// pageOf returns one full page of values, as a store would.
func pageOf(chosen ...*settings.Value) *filtering.QueryFilteredResult[settings.Value] {
	return filtering.NewQueryFilteredResult(chosen, uint64(len(chosen)), uint64(len(chosen)),
		func(v *settings.Value) string { return v.ID }, filtering.DefaultQueryFilter())
}

// valueIn is one stored answer, in a scope.
func valueIn(scope tenancy.Scope, id string) *settings.Value {
	return &settings.Value{
		ID:           id,
		Scope:        scope,
		Subject:      held,
		DefinitionID: "definition_1",
		Raw:          "daily",
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

// TestSubjectVocabulariesAgree pins what makes the subject mapping a conversion
// rather than a resolver: dataprivacy and settings spell the same kinds of
// principal the same way. A deployment whose two vocabularies drifted apart
// would erase nothing and report success.
func TestSubjectVocabulariesAgree(t *testing.T) {
	t.Parallel()

	test.EqOp(t, string(dataprivacy.SubjectUser), string(settings.SubjectUser))
	test.EqOp(t, string(dataprivacy.SubjectAccount), string(settings.SubjectAccount))
}

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
		// every preference the subject ever set.
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
		_, err = privacy.NewCollector(&settingsmock.ValueStoreMock{}, nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewCollector(&settingsmock.ValueStoreMock{}, &testReader{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("collects every scope the resolver names", func(t *testing.T) {
		t.Parallel()

		var reader database.SQLQueryExecutor = &testReader{}

		store := &settingsmock.ValueStoreMock{
			ListValuesForSubjectFunc: func(
				_ context.Context, q database.SQLQueryExecutor, scope tenancy.Scope,
				on settings.Subject, filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[settings.Value], error) {
				// The executor is the one the collector was built with, which is
				// the whole of what the constructor argument buys.
				test.EqOp(t, reader, q)
				test.EqOp(t, held, on)

				// A cleared value is archived rather than deleted and still says
				// what the subject chose, so the export asks for it.
				must.NotNil(t, filter.IncludeArchived)
				test.True(t, *filter.IncludeArchived)

				return pageOf(valueIn(scope, "value_in_"+scope.String())), nil
			},
		}

		collector, err := privacy.NewCollector(store, reader, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []settings.Value
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)
		test.EqOp(t, "value_in_acct_1", collected[0].ID)
		test.EqOp(t, "value_in_acct_2", collected[1].ID)
	})

	T.Run("does not mutate the filter it was handed", func(t *testing.T) {
		t.Parallel()

		// The archived toggle goes on a copy. CollectAll owns the filter it
		// walks the cursor with, and a collector that reached into it would be
		// deciding the next page's shape from inside the page it is reading.
		var seen []*filtering.QueryFilter

		store := &settingsmock.ValueStoreMock{
			ListValuesForSubjectFunc: func(
				_ context.Context, _ database.SQLQueryExecutor, scope tenancy.Scope,
				_ settings.Subject, filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[settings.Value], error) {
				seen = append(seen, filter)

				return pageOf(valueIn(scope, "value_1")), nil
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		must.SliceLen(t, 1, seen)

		// The copy the store saw carries the toggle; the walker's own filter is
		// a different value entirely.
		must.NotNil(t, seen[0].IncludeArchived)
	})

	T.Run("a subject who chose nothing is a domain holding nothing", func(t *testing.T) {
		t.Parallel()

		// nil, nil is how a collector says "no data here", and the section is
		// then omitted from the artifact rather than written as an empty list an
		// export reads as a form.
		store := &settingsmock.ValueStoreMock{
			ListValuesForSubjectFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope,
				settings.Subject, *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[settings.Value], error) {
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

		store := &settingsmock.ValueStoreMock{}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes())
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
		test.SliceEmpty(t, store.ListValuesForSubjectCalls())
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		collector, err := privacy.NewCollector(&settingsmock.ValueStoreMock{}, &testReader{}, privacy.RequestScope)
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
		store := &settingsmock.ValueStoreMock{
			ListValuesForSubjectFunc: func(
				context.Context, database.SQLQueryExecutor, tenancy.Scope,
				settings.Subject, *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[settings.Value], error) {
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

		_, err = privacy.NewEraser(&settingsmock.ValueStoreMock{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestEraser_Erase(T *testing.T) {
	T.Parallel()

	T.Run("sums what it destroyed across every scope", func(t *testing.T) {
		t.Parallel()

		store := &settingsmock.ValueStoreMock{
			DeleteValuesForSubjectFunc: func(
				_ context.Context, q database.Tx, scope tenancy.Scope, on settings.Subject,
			) (int64, error) {
				// The executor is the request's, so the settings and the rest of
				// the subject's footprint commit or roll back together — which is
				// the whole of what the store method's signature exists to require.
				test.NotNil(t, q)
				test.EqOp(t, held, on)

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

			// Nothing is retained, so nothing is reported as retained. A value
			// stripped of its subject belongs to nobody and answers nothing,
			// which is a deletion with extra steps.
			test.MapEmpty(t, outcome.Retained)
		})
	})

	T.Run("a subject who chose nothing is not a failure", func(t *testing.T) {
		t.Parallel()

		store := &settingsmock.ValueStoreMock{
			DeleteValuesForSubjectFunc: func(
				context.Context, database.Tx, tenancy.Scope, settings.Subject,
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

		eraser, err := privacy.NewEraser(&settingsmock.ValueStoreMock{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, testScope, subject)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewEraser(&settingsmock.ValueStoreMock{}, privacy.RequestScope)
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, tenancy.Scope{}, subject)
			must.ErrorIs(t, eraseErr, privacy.ErrUnscopedRequest)
		})
	})

	T.Run("reports a delete that failed rather than a partial total", func(t *testing.T) {
		t.Parallel()

		store := &settingsmock.ValueStoreMock{
			DeleteValuesForSubjectFunc: func(
				_ context.Context, _ database.Tx, scope tenancy.Scope, _ settings.Subject,
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

	collector, err := privacy.NewCollector(&settingsmock.ValueStoreMock{}, &testReader{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterCollector(privacy.DefaultKey, collector))

	eraser, err := privacy.NewEraser(&settingsmock.ValueStoreMock{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterEraser(privacy.DefaultKey, eraser))

	test.Eq(t, []string{privacy.DefaultKey}, registry.CollectorKeys())
	test.Eq(t, []string{privacy.DefaultKey}, registry.EraserKeys())
}
