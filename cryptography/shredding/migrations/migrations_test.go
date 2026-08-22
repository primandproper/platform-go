package migrations

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every supported dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "sh")
			must.NoError(t, err, must.Sprintf("dialect %s", d))
			must.SliceNotEmpty(t, stmts)

			// The table is created before the index that references it.
			test.StrContains(t, stmts[0], "CREATE TABLE")
			test.StrContains(t, stmts[0], "sh_shredding_subject_keys")

			for _, stmt := range stmts {
				test.StrNotContains(t, stmt, ddl.Placeholder, test.Sprintf("dialect %s", d))

				// Comments are stripped, which matters: goose splits a
				// migration on semicolons and would tear a '--' comment
				// containing one in half.
				test.StrNotContains(t, stmt, "--")
			}
		}
	})

	T.Run("keys the table on the subject pair", func(t *testing.T) {
		t.Parallel()

		// A primary key on the ID alone would silently make a user and an
		// account sharing an identifier into one subject with one key — and
		// shredding either would destroy the other's data.
		for _, d := range allDialects {
			stmts, err := Statements(d, "sh")
			must.NoError(t, err)

			test.StrContains(t, stmts[0], "PRIMARY KEY (subject_type, subject_id)",
				test.Sprintf("dialect %s", d))
		}
	})

	T.Run("lets the key material be null", func(t *testing.T) {
		t.Parallel()

		// The tombstone depends on it. A NOT NULL wrapped_key would force a
		// shred to delete the row, and with it the record that the destruction
		// happened at all.
		for _, d := range allDialects {
			stmts, err := Statements(d, "sh")
			must.NoError(t, err)

			for line := range strings.SplitSeq(stmts[0], "\n") {
				if strings.Contains(line, "wrapped_key") {
					test.StrNotContains(t, line, "NOT NULL", test.Sprintf("dialect %s", d))
				}
			}
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "sh")
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a prefix that would not render a legal identifier", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"drop table;--", "has space", "1leading"} {
			_, err := Statements(dialect.SQLite, prefix)
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier, test.Sprintf("prefix %q", prefix))
		}
	})
}

func TestSQL(T *testing.T) {
	T.Parallel()

	T.Run("joins the statements into one migration body", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			body, err := SQL(d, "sh")
			must.NoError(t, err, must.Sprintf("dialect %s", d))

			stmts, err := Statements(d, "sh")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.StrContains(t, body, stmt, test.Sprintf("dialect %s", d))
			}
		}
	})
}

func TestValidatePrefix(T *testing.T) {
	T.Parallel()

	T.Run("accepts the default and a namespace", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidatePrefix(""))
		test.NoError(t, ValidatePrefix("ddb"))
	})

	T.Run("rejects a trailing separator", func(t *testing.T) {
		t.Parallel()

		test.Error(t, ValidatePrefix("ddb_"))
	})
}
