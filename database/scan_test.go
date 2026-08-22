package database

import (
	"database/sql"
	stderrors "errors"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// scanDB returns a mock database and its controller, closed on cleanup.
func scanDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New()
	must.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	return db, mock
}

// scanInt reads the single column these tests select.
func scanInt(scanner Scanner) (int, error) {
	var value int

	err := scanner.Scan(&value)

	return value, err
}

func TestScanAll(T *testing.T) {
	T.Parallel()

	const query = "SELECT n FROM t"

	T.Run("collects one value per row", func(t *testing.T) {
		t.Parallel()

		db, mock := scanDB(t)
		mock.ExpectQuery(query).WillReturnRows(
			sqlmock.NewRows([]string{"n"}).AddRow(1).AddRow(2).AddRow(3))

		got, err := ScanAll(t.Context(), db, "thing", query, nil, scanInt)

		must.NoError(t, err)
		test.Eq(t, []int{1, 2, 3}, got)
	})

	T.Run("an empty result is no values and no error", func(t *testing.T) {
		t.Parallel()

		db, mock := scanDB(t)
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"n"}))

		got, err := ScanAll(t.Context(), db, "thing", query, nil, scanInt)

		must.NoError(t, err)
		test.SliceEmpty(t, got)
	})

	T.Run("reports the query failure", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("connection refused")

		db, mock := scanDB(t)
		mock.ExpectQuery(query).WillReturnError(sentinel)

		_, err := ScanAll(t.Context(), db, "thing", query, nil, scanInt)

		test.ErrorIs(t, err, sentinel)
	})

	T.Run("reports the scan failure rather than a short list", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("unscannable")

		db, mock := scanDB(t)
		mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))

		_, err := ScanAll(t.Context(), db, "thing", query, nil,
			func(Scanner) (int, error) { return 0, sentinel })

		test.ErrorIs(t, err, sentinel)
	})

	// The failure the hand-written loops kept missing: a connection that drops
	// mid-read ends Next's loop without an error of its own, so a store that
	// trusted Next handed back a silently truncated result set.
	T.Run("reports an iteration failure that ended the loop early", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("connection lost")

		db, mock := scanDB(t)
		mock.ExpectQuery(query).WillReturnRows(
			sqlmock.NewRows([]string{"n"}).AddRow(1).RowError(1, sentinel).AddRow(2))

		_, err := ScanAll(t.Context(), db, "thing", query, nil, scanInt)

		test.ErrorIs(t, err, sentinel)
	})

	// A close failure on an otherwise-successful read still reaches the caller,
	// which is what the named error return is for.
	T.Run("surfaces a close failure when nothing worse went wrong", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("close failed")

		db, mock := scanDB(t)
		mock.ExpectQuery(query).WillReturnRows(
			sqlmock.NewRows([]string{"n"}).AddRow(1).CloseError(sentinel))

		_, err := ScanAll(t.Context(), db, "thing", query, nil, scanInt)

		test.ErrorIs(t, err, sentinel)
	})

	// The real cause must not be masked by the cleanup: a caller matching on the
	// query's own failure would otherwise be told the rows would not close.
	T.Run("a close failure does not mask a scan failure", func(t *testing.T) {
		t.Parallel()

		scanErr := platformerrors.New("unscannable")
		closeErr := platformerrors.New("close failed")

		db, mock := scanDB(t)
		mock.ExpectQuery(query).WillReturnRows(
			sqlmock.NewRows([]string{"n"}).AddRow(1).CloseError(closeErr))

		_, err := ScanAll(t.Context(), db, "thing", query, nil,
			func(Scanner) (int, error) { return 0, scanErr })

		test.ErrorIs(t, err, scanErr)
		test.False(t, stderrors.Is(err, closeErr))
	})
}

func TestScanStrings(T *testing.T) {
	T.Parallel()

	const query = "SELECT id FROM t"

	T.Run("collects the identifiers", func(t *testing.T) {
		t.Parallel()

		db, mock := scanDB(t)
		mock.ExpectQuery(query).WillReturnRows(
			sqlmock.NewRows([]string{"id"}).AddRow("a").AddRow("b"))

		got, err := ScanStrings(t.Context(), db, "id", query, nil)

		must.NoError(t, err)
		test.Eq(t, []string{"a", "b"}, got)
	})
}
