package privacy_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/phonecodes"
	phonecodesmock "github.com/primandproper/platform-go/v14/authentication/phonecodes/mock"
	"github.com/primandproper/platform-go/v14/authentication/phonecodes/privacy"
	"github.com/primandproper/platform-go/v14/dataprivacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var (
	firstScope  = tenancy.Of("acct_1")
	secondScope = tenancy.Of("acct_2")

	// testScope is the confinement a request arrives with.
	testScope = firstScope
)

// subject is who these tests are about.
var subject = dataprivacy.Subject{ID: "contact_1", Type: dataprivacy.SubjectUser}

// errStoreUnavailable stands in for a store that cannot answer.
var errStoreUnavailable = platformerrors.New("the phone code store is unavailable")

// codeIn is one stored code, in a scope.
func codeIn(scope tenancy.Scope, id string) *phonecodes.Code {
	return &phonecodes.Code{
		ID:          id,
		Scope:       scope,
		SubjectID:   subject.ID,
		PhoneNumber: "+15555550100",
		Attempts:    2,
		MaxAttempts: 5,
		IssuedAt:    time.Date(2026, time.September, 21, 12, 0, 0, 0, time.UTC),
		ExpiresAt:   time.Date(2026, time.September, 21, 12, 10, 0, 0, time.UTC),
	}
}

// testReader is a SQLQueryExecutor nothing runs on: the store is a mock.
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

// withTx runs fn inside a real transaction; database.Tx is producible only by
// the database package.
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

type testClientConfig struct {
	connectionString string
}

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func TestNewCollector(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewCollector(nil, &testReader{}, privacy.RequestScope)
		test.ErrorIs(t, err, privacy.ErrNilStore)

		_, err = privacy.NewCollector(&phonecodesmock.StoreMock{}, nil, privacy.RequestScope)
		test.ErrorIs(t, err, privacy.ErrNilExecutor)

		_, err = privacy.NewCollector(&phonecodesmock.StoreMock{}, &testReader{}, nil)
		test.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestCollector_Collect(T *testing.T) {
	T.Parallel()

	T.Run("collects every scope the resolver names", func(t *testing.T) {
		t.Parallel()

		var reader database.SQLQueryExecutor = &testReader{}

		store := &phonecodesmock.StoreMock{
			ListForSubjectFunc: func(
				_ context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subjectID string,
			) ([]*phonecodes.Code, error) {
				test.EqOp(t, reader, q)
				test.EqOp(t, subject.ID, subjectID)

				return []*phonecodes.Code{codeIn(scope, "code_in_"+scope.String()), nil}, nil
			},
		}

		collector, err := privacy.NewCollector(store, reader, privacy.FixedScopes(firstScope, secondScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []privacy.Export
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 2, collected)
		test.EqOp(t, "code_in_acct_1", collected[0].ID)
		test.EqOp(t, "code_in_acct_2", collected[1].ID)
		test.EqOp(t, "+15555550100", collected[0].PhoneNumber)
		test.EqOp(t, 2, collected[0].Attempts)
		test.EqOp(t, 5, collected[0].MaxAttempts)
	})

	T.Run("a spent code is in the export, with when", func(t *testing.T) {
		t.Parallel()

		redeemedAt := time.Date(2026, time.September, 21, 12, 5, 0, 0, time.UTC)

		store := &phonecodesmock.StoreMock{
			ListForSubjectFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string) ([]*phonecodes.Code, error) {
				code := codeIn(firstScope, "code_1")
				code.RedeemedAt = &redeemedAt

				return []*phonecodes.Code{code}, nil
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)

		var collected []privacy.Export
		must.NoError(t, json.Unmarshal(fragment, &collected))
		must.SliceLen(t, 1, collected)
		must.NotNil(t, collected[0].RedeemedAt)
		test.True(t, redeemedAt.Equal(*collected[0].RedeemedAt))
	})

	T.Run("a subject who holds nothing is a domain holding nothing", func(t *testing.T) {
		t.Parallel()

		store := &phonecodesmock.StoreMock{
			ListForSubjectFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string) ([]*phonecodes.Code, error) {
				return nil, nil
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		fragment, err := collector.Collect(t.Context(), testScope, subject)
		must.NoError(t, err)
		test.Nil(t, fragment)
	})

	T.Run("reports a read that failed rather than a short export", func(t *testing.T) {
		t.Parallel()

		store := &phonecodesmock.StoreMock{
			ListForSubjectFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string) ([]*phonecodes.Code, error) {
				return nil, errStoreUnavailable
			},
		}

		collector, err := privacy.NewCollector(store, &testReader{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), testScope, subject)
		test.ErrorIs(t, err, errStoreUnavailable)
	})

	T.Run("a request naming no scope is refused by RequestScope", func(t *testing.T) {
		t.Parallel()

		collector, err := privacy.NewCollector(&phonecodesmock.StoreMock{}, &testReader{}, privacy.RequestScope)
		must.NoError(t, err)

		_, err = collector.Collect(t.Context(), tenancy.Scope{}, subject)
		test.ErrorIs(t, err, privacy.ErrUnscopedRequest)
	})
}

func TestNewEraser(T *testing.T) {
	T.Parallel()

	T.Run("refuses what it cannot work without", func(t *testing.T) {
		t.Parallel()

		_, err := privacy.NewEraser(nil, privacy.RequestScope)
		test.ErrorIs(t, err, privacy.ErrNilStore)

		_, err = privacy.NewEraser(&phonecodesmock.StoreMock{}, nil)
		test.ErrorIs(t, err, privacy.ErrNilScopeResolver)
	})
}

func TestEraser_Erase(T *testing.T) {
	T.Parallel()

	T.Run("deletes in the request's transaction, across every scope", func(t *testing.T) {
		t.Parallel()

		withTx(t, func(tx database.Tx) {
			var scopes []tenancy.Scope

			store := &phonecodesmock.StoreMock{
				DeleteForSubjectFunc: func(_ context.Context, got database.Tx, scope tenancy.Scope, subjectID string) (int64, error) {
					test.EqOp(t, tx, got)
					test.EqOp(t, subject.ID, subjectID)

					scopes = append(scopes, scope)

					return 2, nil
				},
			}

			eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope, secondScope))
			must.NoError(t, err)

			outcome, err := eraser.Erase(t.Context(), tx, testScope, subject)
			must.NoError(t, err)
			test.EqOp(t, int64(4), outcome.Deleted)
			test.Eq(t, []tenancy.Scope{firstScope, secondScope}, scopes)

			// It deletes and never revokes: a revocation keeps the row.
			test.SliceEmpty(t, store.RevokeForSubjectCalls())
		})
	})

	T.Run("refuses to run outside a transaction", func(t *testing.T) {
		t.Parallel()

		eraser, err := privacy.NewEraser(&phonecodesmock.StoreMock{}, privacy.FixedScopes(firstScope))
		must.NoError(t, err)

		_, err = eraser.Erase(t.Context(), nil, testScope, subject)
		test.ErrorIs(t, err, privacy.ErrNilExecutor)
	})

	T.Run("reports a delete that failed", func(t *testing.T) {
		t.Parallel()

		withTx(t, func(tx database.Tx) {
			store := &phonecodesmock.StoreMock{
				DeleteForSubjectFunc: func(context.Context, database.Tx, tenancy.Scope, string) (int64, error) {
					return 0, errStoreUnavailable
				},
			}

			eraser, err := privacy.NewEraser(store, privacy.FixedScopes(firstScope))
			must.NoError(t, err)

			_, err = eraser.Erase(t.Context(), tx, testScope, subject)
			test.ErrorIs(t, err, errStoreUnavailable)
		})
	})
}

func TestRegistersUnderTheDefaultKey(t *testing.T) {
	t.Parallel()

	registry := dataprivacy.NewRegistry()

	collector, err := privacy.NewCollector(&phonecodesmock.StoreMock{}, &testReader{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterCollector(privacy.DefaultKey, collector))

	eraser, err := privacy.NewEraser(&phonecodesmock.StoreMock{}, privacy.RequestScope)
	must.NoError(t, err)
	must.NoError(t, registry.RegisterEraser(privacy.DefaultKey, eraser))

	test.Eq(t, []string{privacy.DefaultKey}, registry.CollectorKeys())
	test.Eq(t, []string{privacy.DefaultKey}, registry.EraserKeys())
}
