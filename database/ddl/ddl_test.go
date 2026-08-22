package ddl

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testSchema mirrors the shape every real schema has: two tables, an index on
// one of them, and the same names spelled in all three dialects. The MySQL body
// deliberately omits the partial index, so the dedup in Identifiers is exercised
// by a schema where the dialects genuinely differ rather than by three copies of
// one string.
var testSchema = Schema{
	Component: "widget",
	Postgres: `CREATE TABLE IF NOT EXISTS {{PREFIX}}widget_items (id TEXT PRIMARY KEY);
CREATE INDEX IF NOT EXISTS {{PREFIX}}widget_items_claim_idx ON {{PREFIX}}widget_items (id) WHERE id IS NOT NULL;
CREATE TABLE IF NOT EXISTS {{PREFIX}}widget_totals (id TEXT PRIMARY KEY);`,
	MySQL: `CREATE TABLE IF NOT EXISTS {{PREFIX}}widget_items (id VARCHAR(64) PRIMARY KEY);
CREATE TABLE IF NOT EXISTS {{PREFIX}}widget_totals (id VARCHAR(64) PRIMARY KEY);`,
	SQLite: `CREATE TABLE IF NOT EXISTS {{PREFIX}}widget_items (id TEXT PRIMARY KEY);
CREATE INDEX IF NOT EXISTS {{PREFIX}}widget_items_claim_idx ON {{PREFIX}}widget_items (id);
CREATE TABLE IF NOT EXISTS {{PREFIX}}widget_totals (id TEXT PRIMARY KEY);`,
}

func TestQualify(T *testing.T) {
	T.Parallel()

	T.Run("an empty namespace renders nothing", func(t *testing.T) {
		t.Parallel()

		// Empty is the ordinary case, and it must not render a bare separator:
		// "_widget_items" is a legal identifier, so nothing downstream would
		// catch it.
		test.EqOp(t, "", Qualify(""))
	})

	T.Run("a namespace gains exactly one separator", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "ddb_", Qualify("ddb"))
	})
}

func TestSchema_Identifiers(T *testing.T) {
	T.Parallel()

	T.Run("collects every name across all dialects, sorted and deduplicated", func(t *testing.T) {
		t.Parallel()

		// widget_items appears in all three bodies and its index in two; both
		// must appear once.
		test.Eq(t, []string{
			"widget_items",
			"widget_items_claim_idx",
			"widget_totals",
		}, testSchema.Identifiers(""))
	})

	T.Run("qualifies every name with the namespace", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{
			"ddb_widget_items",
			"ddb_widget_items_claim_idx",
			"ddb_widget_totals",
		}, testSchema.Identifiers("ddb"))
	})

	T.Run("a schema with no placeholders yields nothing", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, Schema{Component: "empty"}.Identifiers("ddb"))
	})
}

func TestValidNamespace(T *testing.T) {
	T.Parallel()

	T.Run("accepts a bare identifier fragment, or nothing", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"", "ddb", "_", "T1", "a_b_c", "audit"} {
			test.True(t, ValidNamespace(prefix), test.Sprintf("namespace %q", prefix))
		}
	})

	T.Run("rejects anything that is not one", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{
			"1audit",                      // leading digit
			"ddb-1",                       // not an identifier character
			"a b",                         // separator
			"audit_; DROP TABLE users;--", // the reason the check exists
			"naïve",                       // non-ASCII, however it renders
			"a\xffb",                      // not even valid UTF-8
			"ddb\n",                       // trailing newline
		} {
			test.False(t, ValidNamespace(prefix), test.Sprintf("namespace %q", prefix))
		}
	})

	T.Run("a schema qualifier is not a namespace", func(t *testing.T) {
		t.Parallel()

		// dialect.ValidIdentifier admits one dot, because a table may be
		// schema-qualified. A prefix may not: the dot would name a schema this
		// module does not create.
		test.True(t, dialect.ValidIdentifier("app.audit"))
		test.False(t, ValidNamespace("app.audit"))
	})

	T.Run("says nothing about the names a prefix renders", func(t *testing.T) {
		t.Parallel()

		// A trailing separator and an over-long rendering are both legal
		// character-wise; catching them needs a schema, which is
		// Schema.ValidatePrefix's job.
		test.True(t, ValidNamespace("ddb_"))
		test.Error(t, testSchema.ValidatePrefix("ddb_"))
	})
}

func TestSchema_ValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts an empty namespace", func(t *testing.T) {
		t.Parallel()

		// Empty is a value, not a missing one: it renders the component's own
		// names, which is what a consumer with one application per database
		// wants.
		test.NoError(t, testSchema.ValidatePrefix(""))
	})

	T.Run("accepts a plain identifier fragment", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, testSchema.ValidatePrefix("ddb"))
	})

	T.Run("rejects a trailing separator", func(t *testing.T) {
		t.Parallel()

		// The renderer supplies the separator. Accepting "ddb_" would render
		// ddb__widget_items — a legal identifier, and a table nobody meant to
		// name, which is exactly the failure this check exists to prevent.
		err := testSchema.ValidatePrefix("ddb_")
		must.Error(t, err)
		test.ErrorIs(t, err, ErrPrefixTrailingSeparator)
		test.StrContains(t, err.Error(), "widget")
	})

	T.Run("rejects a namespace that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		for _, namespace := range []string{"ddb-1", "ddb 1", "1ddb", `ddb"; DROP TABLE users; --`} {
			err := testSchema.ValidatePrefix(namespace)
			must.Error(t, err, must.Sprintf("namespace %q", namespace))
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier, test.Sprintf("namespace %q", namespace))
		}
	})

	T.Run("rejects a namespace that pushes an identifier past the limit", func(t *testing.T) {
		t.Parallel()

		// The longest name this schema renders is widget_items_claim_idx, at 22
		// bytes. A namespace that leaves the tables legal but the index too long
		// is the case a table-only check would miss.
		longest := len("widget_items_claim_idx")
		namespace := strings.Repeat("n", MaxIdentifierLength-longest)

		err := testSchema.ValidatePrefix(namespace)
		must.Error(t, err)
		test.ErrorIs(t, err, ErrPrefixTooLong)
		test.StrContains(t, err.Error(), "claim_idx")
	})

	T.Run("accepts a namespace that lands exactly on the limit", func(t *testing.T) {
		t.Parallel()

		// One byte shorter than the rejected case above: the boundary is
		// inclusive, and an off-by-one here would reject a legal schema.
		longest := len("widget_items_claim_idx")
		namespace := strings.Repeat("n", MaxIdentifierLength-longest-1)

		must.NoError(t, testSchema.ValidatePrefix(namespace))
		test.EqOp(t, MaxIdentifierLength, len(Qualify(namespace)+"widget_items_claim_idx"))
	})
}

func TestSchema_Statements(T *testing.T) {
	T.Parallel()

	T.Run("renders every supported dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			stmts, err := testSchema.Statements(d, "ddb")
			must.NoError(t, err, must.Sprintf("dialect %q", d))
			must.SliceNotEmpty(t, stmts)

			joined := strings.Join(stmts, "\n")
			test.StrContains(t, joined, "ddb_widget_items", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "ddb_widget_totals", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	T.Run("renders the component's own names for an empty namespace", func(t *testing.T) {
		t.Parallel()

		stmts, err := testSchema.Statements(dialect.SQLite, "")
		must.NoError(t, err)

		joined := strings.Join(stmts, "\n")
		test.StrContains(t, joined, "widget_items")
		// No leading separator: "_widget_items" would be legal SQL and wrong.
		test.StrNotContains(t, joined, "_widget_items (")
	})

	T.Run("splits into individually executable statements", func(t *testing.T) {
		t.Parallel()

		stmts, err := testSchema.Statements(dialect.Postgres, "")
		must.NoError(t, err)

		test.SliceLen(t, 3, stmts)
		for _, stmt := range stmts {
			test.StrNotContains(t, stmt, ";")
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := testSchema.Statements("oracle", "ddb")
		must.Error(t, err)
		test.ErrorIs(t, err, dialect.ErrUnsupported)
		test.StrContains(t, err.Error(), "widget")
	})

	T.Run("propagates a rejected namespace", func(t *testing.T) {
		t.Parallel()

		// The dialect is resolved before the namespace is vetted, so this also
		// covers that a valid dialect does not mask an invalid namespace.
		_, err := testSchema.Statements(dialect.Postgres, "ddb_")
		test.ErrorIs(t, err, ErrPrefixTrailingSeparator)
	})
}

func TestSchema_SQL(T *testing.T) {
	T.Parallel()

	T.Run("joins the statements into one body", func(t *testing.T) {
		t.Parallel()

		body, err := testSchema.SQL(dialect.Postgres, "ddb")
		must.NoError(t, err)

		test.StrHasSuffix(t, ";\n", body)
		test.StrContains(t, body, "ddb_widget_items")

		// One terminator per statement, which is what makes the body safe to
		// hand to a tool that splits on semicolons.
		test.EqOp(t, 3, strings.Count(body, ";"))
	})

	T.Run("propagates an unsupported dialect and returns no body", func(t *testing.T) {
		t.Parallel()

		body, err := testSchema.SQL("oracle", "ddb")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
		test.EqOp(t, "", body)
	})

	T.Run("propagates a rejected namespace and returns no body", func(t *testing.T) {
		t.Parallel()

		body, err := testSchema.SQL(dialect.SQLite, "ddb-1")
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		test.EqOp(t, "", body)
	})
}
