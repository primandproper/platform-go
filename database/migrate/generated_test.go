package migrate

import (
	"io/fs"
	"math"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/dialect"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const testGeneratedDDL = `CREATE TABLE generated_widgets (
    id TEXT PRIMARY KEY,
    label TEXT NOT NULL
);

CREATE INDEX generated_widgets_by_label ON generated_widgets (label);
`

func TestMigrationVersion(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cases := map[string]struct {
			version uint64
			ok      bool
		}{
			"00001_users.sql":    {version: 1, ok: true},
			"00037_outbox.sql":   {version: 37, ok: true},
			"20260727_big.sql":   {version: 20260727, ok: true},
			"users.sql":          {version: 0, ok: false},
			"00001_users.txt":    {version: 0, ok: false},
			"_00001_leading.sql": {version: 0, ok: false},
			"":                   {version: 0, ok: false},
		}

		for name, tc := range cases {
			version, ok := migrationVersion(name)
			test.EqOp(t, tc.ok, ok, test.Sprintf("name %q", name))
			test.EqOp(t, tc.version, version, test.Sprintf("name %q", name))
		}
	})

	// Names goose itself refuses. Claiming a version for a file goose skips
	// would make the collision check in mergeGenerated guard a slot nothing
	// occupies, so these have to report false rather than a best-effort number.
	T.Run("names goose skips", func(t *testing.T) {
		t.Parallel()

		names := []string{
			"0007.sql",                       // digits, but no separator
			"0007outbox_widgets.sql",         // separator too late to delimit the version
			"00000_zero.sql",                 // goose requires a version of at least one
			"9223372036854775808_max.sql",    // MaxInt64 + 1, unrepresentable to goose
			"99999999999999999999999_up.sql", // wide enough to wrap a uint64 accumulator
		}

		for _, name := range names {
			version, ok := migrationVersion(name)
			test.False(t, ok, test.Sprintf("name %q", name))
			test.EqOp(t, uint64(0), version, test.Sprintf("name %q", name))
		}
	})

	T.Run("largest version goose can hold", func(t *testing.T) {
		t.Parallel()

		version, ok := migrationVersion("9223372036854775807_max.sql")
		test.True(t, ok)
		test.EqOp(t, uint64(math.MaxInt64), version)
	})
}

func TestGeneratedMigration_validate(T *testing.T) {
	T.Parallel()

	T.Run("rejects unusable migrations", func(t *testing.T) {
		t.Parallel()

		cases := map[string]generatedMigration{
			"zero version":      {version: 0, name: "ok", body: "SELECT 1;"},
			"empty name":        {version: 1, name: "", body: "SELECT 1;"},
			"path in name":      {version: 1, name: "../escape", body: "SELECT 1;"},
			"extension in name": {version: 1, name: "outbox.sql", body: "SELECT 1;"},
			"empty sql":         {version: 1, name: "ok", body: "   \n  "},
		}

		for name, g := range cases {
			test.Error(t, g.validate(), test.Sprintf("case %q", name))
		}

		valid := generatedMigration{version: 1, name: "create_outbox_messages", body: "SELECT 1;"}
		test.NoError(t, valid.validate())
	})
}

func TestWithGeneratedMigration(T *testing.T) {
	T.Parallel()

	T.Run("adds the migration to the filesystem", func(t *testing.T) {
		t.Parallel()

		m, err := New(dialect.SQLite, testMigrations(t),
			WithGeneratedMigration(37, "create_generated_widgets", testGeneratedDDL),
		)
		must.NoError(t, err)

		content, err := fs.ReadFile(m.fsys, "00037_create_generated_widgets.sql")
		must.NoError(t, err)

		// Annotated on the same terms as a file on disk, so goose accepts it.
		test.True(t, strings.Contains(string(content), gooseUpAnnotation))
		test.True(t, strings.Contains(string(content), "CREATE TABLE generated_widgets"))

		// The files on disk are still there.
		for _, name := range []string{"00001_users.sql", "00002_widgets.sql", "00003_multi.sql"} {
			_, readErr := fs.ReadFile(m.fsys, name)
			test.NoError(t, readErr, test.Sprintf("expected %q to survive the merge", name))
		}
	})

	T.Run("rejects a version a file on disk already uses", func(t *testing.T) {
		t.Parallel()

		// 00002_widgets.sql is in testdata. A silent overwrite here would be a
		// corrupt sequence, so this has to fail at construction.
		_, err := New(dialect.SQLite, testMigrations(t),
			WithGeneratedMigration(2, "create_generated_widgets", testGeneratedDDL),
		)
		must.Error(t, err)
		test.True(t, strings.Contains(err.Error(), "00002_widgets.sql"))
	})

	T.Run("rejects two generated migrations sharing a version", func(t *testing.T) {
		t.Parallel()

		_, err := New(dialect.SQLite, testMigrations(t),
			WithGeneratedMigration(37, "first", testGeneratedDDL),
			WithGeneratedMigration(37, "second", testGeneratedDDL),
		)
		test.Error(t, err)
	})

	T.Run("rejects an invalid generated migration", func(t *testing.T) {
		t.Parallel()

		_, err := New(dialect.SQLite, testMigrations(t), WithGeneratedMigration(0, "bad", testGeneratedDDL))
		test.Error(t, err)

		_, err = New(dialect.SQLite, testMigrations(t), WithGeneratedMigration(37, "bad", "  "))
		test.Error(t, err)
	})

	T.Run("applies the generated migration end to end", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		db := openSQLite(t)

		m, err := New(dialect.SQLite, testMigrations(t),
			WithGeneratedMigration(37, "create_generated_widgets", testGeneratedDDL),
			WithLogger(loggingnoop.NewLogger()),
		)
		must.NoError(t, err)

		must.NoError(t, m.Migrate(ctx, db))

		// The generated table exists alongside the ones from disk.
		test.EqOp(t, 0, countRows(t, db, "migrate_test_users"))
		test.EqOp(t, 0, countRows(t, db, "generated_widgets"))

		// The trailing statement of the multi-statement body ran too, which the
		// table existing does not prove.
		var indexes int
		must.NoError(t, db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'generated_widgets_by_label'`,
		).Scan(&indexes))
		test.EqOp(t, 1, indexes)

		// Idempotent, same as any other migration.
		must.NoError(t, m.Migrate(ctx, db))
	})

	T.Run("is a no-op when no generated migrations are supplied", func(t *testing.T) {
		t.Parallel()

		m, err := New(dialect.SQLite, testMigrations(t))
		must.NoError(t, err)

		entries, err := fs.ReadDir(m.fsys, ".")
		must.NoError(t, err)
		test.SliceLen(t, 3, entries)
	})
}
