package querygen

import (
	"slices"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegistry(T *testing.T) {
	T.Parallel()

	T.Run("returns what was registered, sorted", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()
		r.Register("widgets", "accounts", "meal_plans")

		test.SliceEqOp(t, []string{"accounts", "meal_plans", "widgets"}, r.Tables())
	})

	T.Run("is empty until something registers", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, NewRegistry().Tables())
	})

	T.Run("registering nothing registers nothing", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()
		r.Register()

		test.SliceEmpty(t, r.Tables())
	})

	T.Run("accumulates across calls rather than replacing", func(t *testing.T) {
		t.Parallel()

		// The whole point: two sources feed one list, and the second does not
		// take the first's tables away.
		r := NewRegistry()
		r.Register("widgets")
		r.Register("accounts")
		r.Register("meal_plans")

		test.SliceEqOp(t, []string{"accounts", "meal_plans", "widgets"}, r.Tables())
	})

	T.Run("holds one entry per name however many times it arrives", func(t *testing.T) {
		t.Parallel()

		// The multi-dialect case: the same table registered once per pass.
		r := NewRegistry()
		for range 3 {
			r.Register("widgets", "accounts")
		}

		test.SliceEqOp(t, []string{"accounts", "widgets"}, r.Tables())
	})

	T.Run("the zero value registers", func(t *testing.T) {
		t.Parallel()

		var r Registry
		r.Register("widgets")

		test.SliceEqOp(t, []string{"widgets"}, r.Tables())
		test.True(t, r.Has("widgets"))
	})

	T.Run("Has answers for both cases", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()
		r.Register("widgets")

		test.True(t, r.Has("widgets"))
		test.False(t, r.Has("gizmos"))
	})

	T.Run("Has on an untouched registry is false rather than a panic", func(t *testing.T) {
		t.Parallel()

		var r Registry

		test.False(t, r.Has("widgets"))
	})

	T.Run("hands back a copy", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()
		r.Register("widgets", "accounts")

		tables := r.Tables()
		tables[0] = "clobbered"

		test.SliceEqOp(t, []string{"accounts", "widgets"}, r.Tables())
	})

	T.Run("rejects a name that is not an identifier", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()

		err := recovered(func() { r.Register("widgets; DROP TABLE accounts") })
		must.Error(t, err)
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)

		// And the good name alongside it does not sneak in, because the whole
		// call is rejected before anything is stored.
		err = recovered(func() { r.Register("widgets", "not an identifier") })
		must.Error(t, err)
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		test.SliceEmpty(t, r.Tables())
	})

	T.Run("survives concurrent registration", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()

		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				r.Register("widgets")
				r.Has("widgets")
				_ = r.Tables()
			})
		}

		wg.Wait()

		test.SliceEqOp(t, []string{"widgets"}, r.Tables())
	})
}

func TestStandardCRUDRegistration(T *testing.T) {
	T.Parallel()

	columns := []string{IDColumn, "name", CreatedAtColumn, LastUpdatedAtColumn, ArchivedAtColumn}

	T.Run("registers the table it emits for", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()
		For(dialect.Postgres).StandardCRUD("widgets", columns, WithRegistry(r))

		test.SliceEqOp(t, []string{"widgets"}, r.Tables())
	})

	T.Run("registers once across dialects", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()
		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			For(d).StandardCRUD("widgets", columns, WithRegistry(r))
		}

		test.SliceEqOp(t, []string{"widgets"}, r.Tables())
	})

	T.Run("shares a registry with a table nothing generated for", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()
		For(dialect.Postgres).StandardCRUD("widgets", columns, WithRegistry(r))
		r.Register("sessions")
		For(dialect.Postgres).StandardCRUD("accounts", columns, WithRegistry(r))

		test.SliceEqOp(t, []string{"accounts", "sessions", "widgets"}, r.Tables())
	})

	T.Run("registers a table whose queries were all omitted", func(t *testing.T) {
		t.Parallel()

		// The case the registry exists for: no SQL comes out, and the table is
		// still a table with rows in it.
		r := NewRegistry()
		queries := For(dialect.Postgres).StandardCRUD("widgets", columns, WithRegistry(r),
			WithOmitted(CreateQuery, GetQuery, ExistsQuery, ListQuery, UpdateQuery, ArchiveQuery, ScanIDsForReindexQuery, MarkAsIndexedQuery),
		)

		test.SliceEmpty(t, queries)
		test.SliceEqOp(t, []string{"widgets"}, r.Tables())
	})

	T.Run("does not register a table it refuses to emit for", func(t *testing.T) {
		t.Parallel()

		r := NewRegistry()

		err := recovered(func() {
			For(dialect.Postgres).StandardCRUD("widgets", []string{"name"}, WithRegistry(r))
		})
		must.Error(t, err)
		test.ErrorIs(t, err, ErrMissingIDColumn)
		test.SliceEmpty(t, r.Tables())
	})

	T.Run("rejects a nil registry rather than registering nowhere", func(t *testing.T) {
		t.Parallel()

		err := recovered(func() { WithRegistry(nil) })
		must.Error(t, err)
		test.ErrorIs(t, err, ErrNilRegistry)
	})

	T.Run("lands in the package-level registry by default", func(t *testing.T) {
		t.Parallel()

		const table = "standard_crud_default_registry_probe"

		test.False(t, TableRegistered(table))

		For(dialect.Postgres).StandardCRUD(table, columns)

		test.True(t, TableRegistered(table))
		test.SliceContainsOp(t, RegisteredTables(), table)
	})
}

func TestPackageRegistry(T *testing.T) {
	T.Parallel()

	// These share the package-level registry with every other test in the
	// package, so each asserts about the names it registered rather than about
	// the whole list.

	T.Run("registers a table nothing generated SQL for", func(t *testing.T) {
		t.Parallel()

		const table = "package_registry_hand_written_probe"

		test.False(t, TableRegistered(table))

		RegisterTable(table)

		test.True(t, TableRegistered(table))
		test.SliceContainsOp(t, RegisteredTables(), table)
	})

	T.Run("takes several at once", func(t *testing.T) {
		t.Parallel()

		tables := []string{"package_registry_sessions_probe", "package_registry_credentials_probe"}

		RegisterTable(tables...)

		test.SliceContainsSubsetOp(t, RegisteredTables(), tables)
	})

	T.Run("reads back sorted", func(t *testing.T) {
		t.Parallel()

		test.True(t, slices.IsSorted(RegisteredTables()))
	})

	T.Run("reads back without duplicates", func(t *testing.T) {
		t.Parallel()

		tables := RegisteredTables()

		test.SliceLen(t, len(tables), slices.Compact(slices.Clone(tables)))
	})

	T.Run("rejects a name that is not an identifier", func(t *testing.T) {
		t.Parallel()

		err := recovered(func() { RegisterTable("still not an identifier") })
		must.Error(t, err)
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})

	T.Run("reports an unregistered table as absent", func(t *testing.T) {
		t.Parallel()

		test.False(t, TableRegistered("package_registry_never_registered_probe"))
	})
}
