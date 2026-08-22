package retention

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestTable_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a plain table and a schema-qualified one", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"widgets", "archive.widgets", "_private"} {
			must.NoError(t, Table{Name: name, Column: "created_at"}.Validate(dialect.SQLite),
				must.Sprintf("table %q", name))
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		err := Table{Name: "widgets", Column: "created_at"}.Validate(dialect.Dialect("oracle"))
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects names that would not render a legal identifier", func(t *testing.T) {
		t.Parallel()

		// The reason these are checked rather than quoted: they are
		// interpolated into query text, so a name that is not an identifier is
		// a statement nobody wrote.
		for _, name := range []string{"", "widgets; DROP TABLE users", "has space", "1leading", "a.b.c"} {
			err := Table{Name: name, Column: "created_at"}.Validate(dialect.Postgres)
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier, test.Sprintf("table %q", name))
		}
	})

	T.Run("rejects columns that would not render a legal identifier", func(t *testing.T) {
		t.Parallel()

		err := Table{Name: "widgets", Column: "created_at) --"}.Validate(dialect.Postgres)
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)

		err = Table{Name: "widgets", Column: "created_at", KeyColumn: "id, 1"}.Validate(dialect.Postgres)
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})

	T.Run("an empty column is rejected", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, Table{Name: "widgets"}.Validate(dialect.SQLite), dialect.ErrInvalidIdentifier)
	})
}

func TestTable_Describe(T *testing.T) {
	T.Parallel()

	T.Run("names the table", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "widgets", Table{Name: "widgets", Column: "created_at"}.Describe())
	})
}

func TestTable_buildDelete(T *testing.T) {
	T.Parallel()

	cutoff := baseTime

	T.Run("postgres bounds the delete through a subselect on the key", func(t *testing.T) {
		t.Parallel()

		query, args := Table{Name: "widgets", Column: "created_at"}.buildDelete(dialect.Postgres, cutoff, 500)

		test.EqOp(t,
			"DELETE FROM widgets WHERE id IN (SELECT id FROM widgets WHERE created_at <= $1 ORDER BY created_at LIMIT $2)",
			query)
		test.Eq(t, []any{cutoff.UTC(), 500}, args)
	})

	T.Run("sqlite renders the same shape with unnumbered placeholders", func(t *testing.T) {
		t.Parallel()

		query, _ := Table{Name: "widgets", Column: "created_at", KeyColumn: "widget_id"}.
			buildDelete(dialect.SQLite, cutoff, 10)

		test.EqOp(t,
			"DELETE FROM widgets WHERE widget_id IN "+
				"(SELECT widget_id FROM widgets WHERE created_at <= ? ORDER BY created_at LIMIT ?)",
			query)
	})

	T.Run("mysql uses ORDER BY and LIMIT directly", func(t *testing.T) {
		t.Parallel()

		// Not a stylistic preference: MySQL refuses to read from the table it
		// is deleting from in an IN subquery, so the portable form is not
		// available there at all.
		query, _ := Table{Name: "widgets", Column: "expires_at"}.buildDelete(dialect.MySQL, cutoff, 10)

		test.EqOp(t, "DELETE FROM widgets WHERE expires_at <= ? ORDER BY expires_at LIMIT ?", query)
		test.StrNotContains(t, query, "SELECT")
	})

	T.Run("the cutoff is bound as UTC", func(t *testing.T) {
		t.Parallel()

		// The SQLite driver stores a bound time.Time as Go's own String()
		// rendering, so a value bound in another zone would compare
		// lexicographically against UTC values and silently mis-order.
		zone := time.FixedZone("elsewhere", 5*60*60)

		_, args := Table{Name: "widgets", Column: "created_at"}.
			buildDelete(dialect.Postgres, cutoff.In(zone), 1)

		bound, ok := args[0].(time.Time)
		must.True(t, ok)
		test.EqOp(t, time.UTC, bound.Location())
	})
}

func TestTable_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("removes only rows at or before the cutoff", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "old", baseTime.Add(-time.Hour), 3)
		insertWidgets(t, client, "new", baseTime.Add(time.Hour), 2)

		target := Table{Name: widgetsTable, Column: "created_at"}

		removed, err := target.Sweep(t.Context(), client.Writer(), dialect.SQLite, baseTime, 100)
		must.NoError(t, err)
		test.EqOp(t, int64(3), removed)
		test.Eq(t, []string{"new-0", "new-1"}, widgetIDs(t, client))
	})

	T.Run("honors the batch bound and takes the oldest rows first", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "oldest", baseTime.Add(-3*time.Hour), 2)
		insertWidgets(t, client, "newer", baseTime.Add(-time.Hour), 2)

		target := Table{Name: widgetsTable, Column: "created_at"}

		removed, err := target.Sweep(t.Context(), client.Writer(), dialect.SQLite, baseTime, 2)
		must.NoError(t, err)
		test.EqOp(t, int64(2), removed)

		// Oldest first is what makes successive batches monotonic progress
		// through a backlog rather than an arbitrary two rows from anywhere.
		test.Eq(t, []string{"newer-0", "newer-1"}, widgetIDs(t, client))
	})

	T.Run("a row with no timestamp never expires", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		_, err := client.Writer().ExecContext(t.Context(),
			"INSERT INTO widgets (id, created_at, expires_at) VALUES (?, ?, NULL)", "unbounded", baseTime.UTC())
		must.NoError(t, err)

		removed, err := Table{Name: widgetsTable, Column: "expires_at"}.
			Sweep(t.Context(), client.Writer(), dialect.SQLite, baseTime.Add(time.Hour), 100)
		must.NoError(t, err)
		test.EqOp(t, int64(0), removed)
		test.EqOp(t, int64(1), countWidgets(t, client))
	})

	T.Run("reports the error from a table that does not exist", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		_, err := Table{Name: "absent", Column: "created_at"}.
			Sweep(t.Context(), client.Writer(), dialect.SQLite, baseTime, 10)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "absent")
	})
}

func TestTable_Backlog(T *testing.T) {
	T.Parallel()

	T.Run("counts what remains at or before the cutoff", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "old", baseTime.Add(-time.Hour), 4)
		insertWidgets(t, client, "new", baseTime.Add(time.Hour), 6)

		backlog, err := Table{Name: widgetsTable, Column: "created_at"}.
			Backlog(t.Context(), client.Reader(), dialect.SQLite, baseTime, 1000)
		must.NoError(t, err)
		test.EqOp(t, int64(4), backlog)
	})

	T.Run("saturates at the ceiling", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "old", baseTime.Add(-time.Hour), 5)

		// The gauge answers "is the backlog growing", and paying an unbounded
		// COUNT to answer it exactly would make the reading most expensive
		// exactly when the problem is worst.
		backlog, err := Table{Name: widgetsTable, Column: "created_at"}.
			Backlog(t.Context(), client.Reader(), dialect.SQLite, baseTime, 2)
		must.NoError(t, err)
		test.EqOp(t, int64(2), backlog)
	})

	T.Run("reports the error from a table that does not exist", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		_, err := Table{Name: "absent", Column: "created_at"}.
			Backlog(t.Context(), client.Reader(), dialect.SQLite, baseTime, 10)
		test.Error(t, err)
	})
}
