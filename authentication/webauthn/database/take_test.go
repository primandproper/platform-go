package database

import (
	"context"
	"database/sql"
	stderrors "errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The two halves of Consume's transaction that a real database will not fail
// on demand.
//
// Consume's guarantee is that the delete decides who owns the ceremony, which
// puts three outcomes on the delete: it removed a row, it removed none, or it
// failed. A real server gives the first two — the conformance suite and the
// container run both exercise them — and no reasonable database gives the third
// on request. Without these, the delete failing and the delete finding nothing
// are indistinguishable, and a ceremony would be reported as never begun
// because the server was unreachable halfway through the transaction.
func TestSessionStore_Consume_transactionFailures(T *testing.T) {
	T.Parallel()

	T.Run("reports a delete that fails rather than an absent ceremony", func(t *testing.T) {
		t.Parallel()

		store, mock := newMockStore(t)

		mock.ExpectBegin()
		mock.ExpectQuery("SELECT session_data, expires_at FROM webauthn_sessions").
			WithArgs("chal").
			WillReturnRows(sqlmock.NewRows([]string{"session_data", "expires_at"}).
				AddRow([]byte(`{"challenge":"chal"}`), time.Now().Add(time.Hour)))
		mock.ExpectExec("DELETE FROM webauthn_sessions").
			WithArgs("chal").
			WillReturnError(errServerGone)
		mock.ExpectRollback()

		session, err := store.Consume(t.Context(), "chal")
		test.ErrorIs(t, err, errServerGone)
		test.False(t, stderrors.Is(err, webauthn.ErrSessionNotFound))
		test.Nil(t, session)

		must.NoError(t, mock.ExpectationsWereMet())
	})

	// A driver that cannot say how many rows it removed cannot say who owns the
	// ceremony either, so the answer is the error rather than a count of zero —
	// which would read as "somebody else consumed it" and is exactly the case a
	// caller must not confuse with a lost connection.
	T.Run("reports a delete whose row count is unreadable", func(t *testing.T) {
		t.Parallel()

		store, mock := newMockStore(t)

		mock.ExpectBegin()
		mock.ExpectQuery("SELECT session_data, expires_at FROM webauthn_sessions").
			WithArgs("chal").
			WillReturnRows(sqlmock.NewRows([]string{"session_data", "expires_at"}).
				AddRow([]byte(`{"challenge":"chal"}`), time.Now().Add(time.Hour)))
		mock.ExpectExec("DELETE FROM webauthn_sessions").
			WithArgs("chal").
			WillReturnResult(sqlmock.NewErrorResult(errServerGone))
		mock.ExpectRollback()

		session, err := store.Consume(t.Context(), "chal")
		test.ErrorIs(t, err, errServerGone)
		test.False(t, stderrors.Is(err, webauthn.ErrSessionNotFound))
		test.Nil(t, session)

		must.NoError(t, mock.ExpectationsWereMet())
	})

	// The other side of the same statement, and the one that is not an error: a
	// delete that removed nothing means somebody else finished this ceremony
	// between the read and the delete.
	T.Run("reports a delete that removed nothing as an absent ceremony", func(t *testing.T) {
		t.Parallel()

		store, mock := newMockStore(t)

		mock.ExpectBegin()
		mock.ExpectQuery("SELECT session_data, expires_at FROM webauthn_sessions").
			WithArgs("chal").
			WillReturnRows(sqlmock.NewRows([]string{"session_data", "expires_at"}).
				AddRow([]byte(`{"challenge":"chal"}`), time.Now().Add(time.Hour)))
		mock.ExpectExec("DELETE FROM webauthn_sessions").
			WithArgs("chal").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectRollback()

		session, err := store.Consume(t.Context(), "chal")
		test.ErrorIs(t, err, webauthn.ErrSessionNotFound)
		test.Nil(t, session)

		must.NoError(t, mock.ExpectationsWereMet())
	})
}

// errServerGone is the failure the mocked driver injects.
var errServerGone = platformerrors.New("the database went away")

// mockClient is a database.Client over a mocked driver, for the failures a
// real server will not produce on request. It is a test double of the seam
// rather than of this package's own logic: every statement it runs is the one
// queries.go rendered.
type mockClient struct {
	db *sql.DB
}

var _ database.Client = (*mockClient)(nil)

func (c *mockClient) Dialect() dialect.Dialect          { return dialect.SQLite }
func (c *mockClient) Reader() database.SQLQueryExecutor { return c.db }
func (c *mockClient) Writer() database.SQLQueryExecutor { return c.db }
func (c *mockClient) Close() error                      { return c.db.Close() }
func (c *mockClient) CurrentTime() time.Time            { return time.Now() }

func (c *mockClient) WithTransaction(ctx context.Context, fn func(database.SQLQueryExecutor) error) error {
	return database.RunInTransaction(ctx, c.db,
		func(_ context.Context, tx database.SQLQueryExecutorAndTransactionManager) { _ = tx.Rollback() }, fn)
}

// newMockStore builds a store over a mocked driver.
func newMockStore(tb testing.TB) (*SessionStore, sqlmock.Sqlmock) {
	tb.Helper()

	db, mock, err := sqlmock.New()
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	store, err := NewSessionStore(&Config{}, &mockClient{db: db})
	must.NoError(tb, err)

	return store, mock
}
