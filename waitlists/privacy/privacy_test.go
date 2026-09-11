package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsmock "github.com/primandproper/platform-go/v14/waitlists/mock"
	"github.com/primandproper/platform-go/v14/waitlists/privacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two catalogs a subject's signups might be in. Two, because the whole of
// what a ScopeResolver decides is which of them a request reaches.
var (
	firstScope  = tenancy.Of("acct_1")
	secondScope = tenancy.Of("acct_2")

	// testScope is the confinement a request arrives with, which the fulfiller
	// hands the collector and the eraser beside the subject.
	testScope = firstScope
)

// subject is the person these tests are about, and who is the same person as
// the store keys signups on.
var (
	subject = dataprivacy.Subject{ID: "user_1", Type: dataprivacy.SubjectUser}
	who     = waitlists.Subject{Type: waitlists.SubjectUser, ID: "user_1"}
)

// errStoreUnavailable stands in for a store that cannot answer.
var errStoreUnavailable = platformerrors.New("the store is unavailable")

// pageOf returns one full page of signups, as a store would.
func pageOf(held ...*waitlists.Signup) *filtering.QueryFilteredResult[waitlists.Signup] {
	return filtering.NewQueryFilteredResult(held, uint64(len(held)), uint64(len(held)),
		func(s *waitlists.Signup) string { return s.ID }, filtering.DefaultQueryFilter())
}

// signupIn is one stored signup, in a scope.
func signupIn(scope tenancy.Scope, id string) *waitlists.Signup {
	return &waitlists.Signup{
		ID:      id,
		Scope:   scope,
		ListID:  "wl_1",
		Contact: "ada@example.com",
		Subject: who,
		Status:  waitlists.StatusWaiting,
	}
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
// rather than a resolver: dataprivacy and waitlists spell the same kinds of
// principal the same way. A deployment whose two vocabularies drifted apart
// would erase nothing and report success.
func TestSubjectVocabulariesAgree(t *testing.T) {
	t.Parallel()

	test.EqOp(t, string(dataprivacy.SubjectUser), string(waitlists.SubjectUser))
	test.EqOp(t, string(dataprivacy.SubjectAccount), string(waitlists.SubjectAccount))
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
		// every signup the subject actually holds.
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

		_, err := privacy.NewCollector(nil, reader, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilStore)

		// The executor is required for the same reason the eraser's is: the
		// store keeps no connection of its own, so a collector without one has
		// nothing to read on.
		_, err = privacy.NewCollector(&waitlistsmock.SignupStoreMock{}, nil, privacy.RequestScope)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewCollector(&waitlistsmock.SignupStoreMock{}, reader, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("collects every scope the resolver names, archived signups included", func(t *testing.T) {
		t.Parallel()

		store := &waitlistsmock.SignupStoreMock{
			ListSignupsForSubjectFunc: func(
				_ context.Context,
				q database.SQLQueryExecutor,
				scope tenancy.Scope,
				asked waitlists.Subject,
				filter *filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[waitlists.Signup], error) {
				// The executor is the one the collector was built with, since an
				// export is a read with no transaction to be part of.
				test.EqOp(t, database.SQLQueryExecutor(reader), q)

				// The request's subject, rendered as the store keys on it.
				test.EqOp(t, who, asked)

				// An archived signup still holds the address it was made with,
				// and an export that omitted it would be missing data the table
				// holds.
				must.NotNil(t, filter)
				must.NotNil(t, filter.IncludeArchived)
				test.True(t, *filter.IncludeArchived)

				return pageOf(signupIn(scope, "signup_in_"+scope.String())), nil
			},
		}

		collector, err := privacy.NewCollector(store, reader, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []waitlists.Signup
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)
		test.EqOp(t, "signup_in_acct_1", collected[0].ID)
		test.EqOp(t, "signup_in_acct_2", collected[1].ID)
	})

	T.Run("a subject who joined nothing is a domain holding nothing", func(t *testing.T) {
		t.Parallel()

		// nil, nil is how a collector says "no data here", and the section is
		// then omitted from the artifact rather than written as an empty list an
		// export reads as a form.
		store := &waitlistsmock.SignupStoreMock{
			ListSignupsForSubjectFunc: func(
				context.Context,
				database.SQLQueryExecutor,
				tenancy.Scope,
				waitlists.Subject,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[waitlists.Signup], error) {
				return pageOf(), nil
			},
		}

		collector, err := privacy.NewCollector(store, reader, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
	})

	T.Run("no scopes is no reads", func(t *testing.T) {
		t.Parallel()

		store := &waitlistsmock.SignupStoreMock{}

		collector, err := privacy.NewCollector(store, reader, privacy.FixedScopes())
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
		test.SliceEmpty(t, store.ListSignupsForSubjectCalls())
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		collector, err := privacy.NewCollector(&waitlistsmock.SignupStoreMock{}, reader, privacy.RequestScope)
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
		store := &waitlistsmock.SignupStoreMock{
			ListSignupsForSubjectFunc: func(
				context.Context,
				database.SQLQueryExecutor,
				tenancy.Scope,
				waitlists.Subject,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[waitlists.Signup], error) {
				return nil, errStoreUnavailable
			},
		}

		collector, err := privacy.NewCollector(store, reader, privacy.FixedScopes(firstScope))
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

		_, err = privacy.NewEraser(&waitlistsmock.SignupStoreMock{}, nil)
		must.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestEraser_Erase(T *testing.T) {
	T.Parallel()

	T.Run("sums what it withdrew across every scope and reports the digests it kept", func(t *testing.T) {
		t.Parallel()

		store := &waitlistsmock.SignupStoreMock{
			WithdrawSignupsForSubjectFunc: func(
				_ context.Context, q database.Tx, scope tenancy.Scope, asked waitlists.Subject,
			) (int64, error) {
				// The executor is the request's, so the signups and the rest of
				// the subject's footprint commit or roll back together.
				test.NotNil(t, q)
				test.EqOp(t, who, asked)

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

			// Withdrawn rows are anonymized in place, not destroyed: the row
			// stays so the suppression does.
			test.EqOp(t, int64(5), outcome.Anonymized)
			test.EqOp(t, int64(0), outcome.Deleted)

			// And what each kept is on the record, with the number and the
			// reason, which is the shape that answers a regulator's question.
			must.MapContainsKey(t, outcome.Retained, privacy.RetainedDigests)
			test.StrHasPrefix(t, "5 contact digest(s)", outcome.Retained[privacy.RetainedDigests])
			test.True(t, strings.Contains(outcome.Retained[privacy.RetainedDigests], "not reversible"))
		})
	})

	T.Run("a subject who joined nothing is not a failure, and retains nothing", func(t *testing.T) {
		t.Parallel()

		store := &waitlistsmock.SignupStoreMock{
			WithdrawSignupsForSubjectFunc: func(
				context.Context, database.Tx, tenancy.Scope, waitlists.Subject,
			) (int64, error) {
				return 0, nil
			},
		}

		eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			outcome, eraseErr := eraser.Erase(t.Context(), q, testScope, subject)
			must.NoError(t, eraseErr)
			test.EqOp(t, int64(0), outcome.Anonymized)

			// No signups, no digests: a retention entry about nothing would be
			// a line in the request record about nothing.
			test.MapEmpty(t, outcome.Retained)
		})
	})

	T.Run("refuses to run outside a transaction", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewEraser(&waitlistsmock.SignupStoreMock{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, testScope, subject)
		must.ErrorIs(t, err, privacy.ErrNilExecutor)

		// And it wraps the module-wide sentinel, so a caller checking for a nil
		// input generally is answered too.
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("reports a resolver that could not answer", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewEraser(&waitlistsmock.SignupStoreMock{}, privacy.RequestScope)
		must.NoError(t, err)

		withTx(t, func(q database.Tx) {
			_, eraseErr := eraser.Erase(t.Context(), q, tenancy.Scope{}, subject)
			must.ErrorIs(t, eraseErr, privacy.ErrUnscopedRequest)
		})
	})

	T.Run("reports a withdrawal that failed rather than a partial total", func(t *testing.T) {
		t.Parallel()

		store := &waitlistsmock.SignupStoreMock{
			WithdrawSignupsForSubjectFunc: func(
				_ context.Context, _ database.Tx, scope tenancy.Scope, _ waitlists.Subject,
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
			test.EqOp(t, int64(0), outcome.Anonymized)
			test.MapEmpty(t, outcome.Retained)
		})
	})
}

func TestRegistersUnderTheDefaultKey(t *testing.T) {
	t.Parallel()

	// The key names the section an export's artifact carries these in, and a
	// registry refuses one it does not like — so the constant is exercised
	// against the thing that validates it rather than merely declared.
	registry := dataprivacy.NewRegistry()

	collector, err := privacy.NewCollector(&waitlistsmock.SignupStoreMock{}, reader, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterCollector(privacy.DefaultKey, collector))

	eraser, err := privacy.NewEraser(&waitlistsmock.SignupStoreMock{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterEraser(privacy.DefaultKey, eraser))

	test.Eq(t, []string{privacy.DefaultKey}, registry.CollectorKeys())
	test.Eq(t, []string{privacy.DefaultKey}, registry.EraserKeys())
}
