package migrations

import (
	"errors"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func allDialects() []dialect.Dialect {
	return []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}
}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			test.True(t, len(stmts) >= 4)
		}
	})

	T.Run("substitutes the prefix", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "custom")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "custom_authz_roles")
			test.StrContains(t, joined, "custom_authz_permissions")
			test.StrContains(t, joined, "custom_authz_role_permissions")
			test.StrContains(t, joined, "custom_authz_role_hierarchy")
			test.StrNotContains(t, joined, ddl.Placeholder)
		}
	})

	T.Run("an empty prefix is valid", func(t *testing.T) {
		t.Parallel()

		stmts, err := Statements(dialect.SQLite, "")
		must.NoError(t, err)

		test.StrContains(t, strings.Join(stmts, "\n"), "roles")
	})

	// Dependency order matters: the mapping tables reference roles and
	// permissions, so those must be created first.
	T.Run("creates referenced tables first", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			rolesAt := strings.Index(joined, "CREATE TABLE IF NOT EXISTS authz_roles")
			permsAt := strings.Index(joined, "CREATE TABLE IF NOT EXISTS authz_permissions")
			mappingAt := strings.Index(joined, "CREATE TABLE IF NOT EXISTS authz_role_permissions")

			test.True(t, rolesAt >= 0)
			test.True(t, permsAt >= 0)
			test.True(t, mappingAt > rolesAt)
			test.True(t, mappingAt > permsAt)
		}
	})

	// Comments are stripped before splitting on semicolons: prose routinely
	// contains one, and splitting first tears the comment in half, leaving its
	// tail masquerading as SQL.
	T.Run("strips comments", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.StrNotContains(t, stmt, "--")
			}
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements("cockroach", "authz")

		test.True(t, errors.Is(err, dialect.ErrUnsupported))
	})

	// The prefix is interpolated into DDL, so it is restricted rather than
	// escaped.
	T.Run("rejects prefixes that are not identifier fragments", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{
			`authz"; DROP TABLE users; --`,
			"authz-",
			"authz ",
			"1authz",
			"auth z",
		} {
			_, err := Statements(dialect.SQLite, prefix)

			test.True(t, errors.Is(err, dialect.ErrInvalidIdentifier))
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("joins statements into one body", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects() {
			body, err := SQL(d, "authz")
			must.NoError(t, err)

			test.StrHasSuffix(t, ";\n", body)
			test.StrContains(t, body, "authz_roles")
		}
	})

	T.Run("propagates rendering errors", func(t *testing.T) {
		t.Parallel()

		_, err := SQL("cockroach", "authz")
		test.True(t, errors.Is(err, dialect.ErrUnsupported))

		_, err = SQL(dialect.SQLite, "bad-prefix")
		test.True(t, errors.Is(err, dialect.ErrInvalidIdentifier))
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts an empty namespace", func(t *testing.T) {
		t.Parallel()

		// Empty renders the component's own names, which is the default.
		test.NoError(t, ValidatePrefix(""))
	})

	T.Run("accepts a plain identifier fragment", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix("ddb"))
	})

	T.Run("rejects a trailing separator", func(t *testing.T) {
		t.Parallel()

		// The renderer supplies the separator; a namespace carrying one too
		// would render a doubled separator rather than an error.
		test.ErrorIs(t, ValidatePrefix("ddb_"), ddl.ErrPrefixTrailingSeparator)
	})

	T.Run("rejects a namespace that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		for _, namespace := range []string{"ddb-1", "1ddb", "ddb 1"} {
			test.ErrorIs(t, ValidatePrefix(namespace), dialect.ErrInvalidIdentifier,
				test.Sprintf("namespace %q", namespace))
		}
	})

	T.Run("rejects a namespace that pushes an index name past the limit", func(t *testing.T) {
		t.Parallel()

		// Index names are the longest identifiers this schema renders, and are
		// what a table-only check would miss.
		namespace := strings.Repeat("n", ddl.MaxIdentifierLength-len("authz_role_permissions_permission_idx"))

		test.ErrorIs(t, ValidatePrefix(namespace), ddl.ErrPrefixTooLong)
	})
}
