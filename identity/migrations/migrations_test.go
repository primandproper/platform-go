package migrations

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allDialects is what every rendering assertion runs against: a schema that is
// right on two of three is the failure mode this package exists to prevent.
var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// tableNames is every table this schema creates, unprefixed.
var tableNames = []string{
	"identity_users",
	"identity_user_roles",
	"identity_accounts",
	"identity_memberships",
	"identity_membership_roles",
	"identity_invitations",
	"identity_invitation_roles",
}

func TestStatements(T *testing.T) {
	T.Parallel()

	T.Run("renders every table in every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "")
			must.NoError(t, err)
			must.SliceNotEmpty(t, stmts)

			joined := strings.Join(stmts, "\n")
			for _, table := range tableNames {
				test.StrContains(t, joined, table, test.Sprintf("%s is missing %s", d, table))
			}

			for _, stmt := range stmts {
				test.False(t, strings.Contains(stmt, "{{"),
					test.Sprintf("%s left a placeholder: %s", d, stmt))
			}
		}
	})

	T.Run("applies the prefix to every identifier", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "app")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")
			for _, table := range tableNames {
				test.StrContains(t, joined, "app_"+table)
			}

			// An unprefixed name left behind would create a table in the shared
			// namespace this prefix exists to avoid.
			for _, table := range tableNames {
				test.False(t, strings.Contains(joined, " "+table+" "),
					test.Sprintf("%s left %s unprefixed", d, table))
			}
		}
	})

	T.Run("rejects a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		// The prefix is vetted against every identifier it renders, not against
		// a pattern, so one that is legal alone and produces an illegal index
		// name fails here rather than at a consumer's first migration.
		must.Error(t, ValidatePrefix("has space"))
		must.Error(t, ValidatePrefix("trailing_"))
		must.Error(t, ValidatePrefix(strings.Repeat("x", 200)))

		must.NoError(t, ValidatePrefix(""))
		must.NoError(t, ValidatePrefix("app"))

		for _, d := range allDialects {
			_, err := Statements(d, "has space")
			must.Error(t, err)
		}
	})

	T.Run("SQL is the statements rejoined", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := Statements(d, "app")
			must.NoError(t, err)

			body, err := SQL(d, "app")
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.StrContains(t, body, strings.TrimSpace(stmt))
			}
		}
	})

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := Statements(dialect.Dialect("oracle"), "")
		must.Error(t, err)
	})
}

func TestTables(T *testing.T) {
	T.Parallel()

	T.Run("is every table the schema creates", func(t *testing.T) {
		t.Parallel()

		// Against the same hand-written list every other assertion in this file
		// runs against, sorted: complete is the property this list is exported
		// for, so it is pinned rather than derived from the thing it reads.
		want := slices.Clone(tableNames)
		slices.Sort(want)

		names, err := Tables("")
		must.NoError(t, err)
		test.Eq(t, want, names)
	})

	T.Run("renders at the prefix", func(t *testing.T) {
		t.Parallel()

		names, err := Tables("app")
		must.NoError(t, err)
		must.SliceLen(t, len(tableNames), names)

		for _, name := range names {
			test.StrHasPrefix(t, "app_identity_", name)
		}
	})

	T.Run("names what the DDL creates, at the same prefix", func(t *testing.T) {
		t.Parallel()

		// The list a consumer truncates by has to agree with the statements
		// that created the tables, and agreeing at the empty prefix is not the
		// same as agreeing at theirs.
		for _, d := range allDialects {
			stmts, err := Statements(d, "app")
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			names, err := Tables("app")
			must.NoError(t, err)

			for _, name := range names {
				test.StrContains(t, joined, "CREATE TABLE IF NOT EXISTS "+name+" ",
					test.Sprintf("%s does not create %s", d, name))
			}
		}
	})

	T.Run("rejects a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		// The names are interpolated into whatever the caller builds out of
		// them, so an unvetted prefix would leave this the one door into the
		// package that hands back an identifier nothing checked.
		for _, prefix := range []string{"has space", "trailing_", strings.Repeat("x", 200)} {
			_, err := Tables(prefix)
			test.Error(t, err, test.Sprintf("prefix %q", prefix))
		}
	})
}

// indexNames is every index any version of this schema creates, unprefixed.
// Postgres and SQLite spell them as CREATE INDEX statements, and MySQL as inline
// keys in version 1 and an ALTER TABLE's ADD KEY after it. The point of the list
// is that the three still agree: a MySQL-only name would be a name
// ValidatePrefix stopped measuring on the other two.
var indexNames = []string{
	"identity_users_scope_idx",
	"identity_users_email_token_digest_idx",
	"identity_user_roles_role_idx",
	"identity_accounts_scope_idx",
	"identity_accounts_billing_idx",
	"identity_accounts_customer_idx",
	"identity_memberships_user_idx",
	"identity_memberships_account_idx",
	"identity_membership_roles_role_idx",
	"identity_invitations_email_idx",
	"identity_invitations_from_idx",
	"identity_invitations_account_idx",
}

// TestSchema_IndexNamesAgreeAcrossDialects is what is left of this package's
// share of the re-runnability rule once internal/schemaconvention owns the rule
// itself.
//
// That MySQL declares every key inline, and that Postgres and SQLite guard every
// standalone index with IF NOT EXISTS, is asserted there for all twenty-five
// schema-shipping packages at once — the failure is invisible from inside any
// one of them, which is how fourteen of them carried it simultaneously.
//
// What stays here is the consequence that is identity's alone. MySQL scopes an
// index name to its table and would accept shorter ones than the other two
// dialects need, but ValidatePrefix measures the longest identifier a prefix
// renders across all three bodies at once, so a name only two of them spelled
// would be a name that check stopped measuring here.
func TestSchema_IndexNamesAgreeAcrossDialects(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		stmts, err := Statements(d, "")
		must.NoError(T, err)

		joined := strings.Join(stmts, "\n")
		for _, name := range indexNames {
			test.StrContains(T, joined, name,
				test.Sprintf("%s is missing %s", d, name))
		}
	}
}

func TestSchema_ScopeColumnHasNoDefault(T *testing.T) {
	T.Parallel()

	// The empty string is tenancy.Global(), not the absence of a scope. A
	// column that supplied it for a write which did not name one would hand the
	// global scope to whoever forgot the column — the mistake tenancy.Scope
	// exists to make unspellable in Go, enforced here for a writer that did not
	// come through SQLStore.
	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, stmt := range stmts {
				for line := range strings.SplitSeq(stmt, "\n") {
					trimmed := strings.TrimSpace(line)
					if !strings.HasPrefix(trimmed, "scope ") {
						continue
					}

					test.StrContains(t, trimmed, "NOT NULL")
					test.StrNotContains(t, trimmed, "DEFAULT")
				}
			}
		})
	}
}

func TestSchema_UniquenessCoversArchivedRows(T *testing.T) {
	T.Parallel()

	// Freeing a username when its owner is soft-deleted means a later registrant
	// can take it, and every audit row naming that handle then refers to two
	// people. The uniqueness is therefore unconditional in every dialect — no
	// partial clause on the two that have them.
	for _, d := range []dialect.Dialect{dialect.Postgres, dialect.SQLite} {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			stmts, err := Statements(d, "")
			must.NoError(t, err)

			for _, stmt := range stmts {
				if !strings.Contains(stmt, "CREATE UNIQUE INDEX") {
					continue
				}

				test.StrNotContains(t, stmt, "WHERE",
					test.Sprintf("unique index is partial: %s", stmt))
			}
		})
	}
}

// TestSchemaFiles_MatchTheMigrations is the regeneration gate for the
// committed schema files unison's config names, living beside them: each must
// be exactly what the migrations render for its dialect, at the empty prefix.
// A hand-edit to one leaves sqlc analyzing DDL no database runs, which is the
// checked-versus-executed gap in its other direction.
func TestSchemaFiles_MatchTheMigrations(T *testing.T) {
	T.Parallel()

	for _, d := range allDialects {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(filepath.Join("schema", string(d)+".sql"))
			must.NoError(t, err)

			rendered, err := SQL(d, "")
			must.NoError(t, err)

			test.EqOp(t, rendered+"\n", string(committed),
				test.Sprintf("run `make unison` and commit schema/%s.sql", d))
		})
	}
}

func TestSequence(T *testing.T) {
	T.Parallel()

	// Pinned here as well as refused at render time, so a malformed sequence
	// fails this package's build rather than a consumer's migration run.
	T.Run("validates", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, sequence.Validate())
	})

	T.Run("reports the latest version", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, uint64(2), Latest())
	})

	// Every version carries every dialect. A version missing one would render
	// as ErrUnsupported only once a consumer on that dialect reached it.
	T.Run("renders every version in every dialect", func(t *testing.T) {
		t.Parallel()

		for i := range sequence {
			for _, d := range allDialects {
				stmts, err := sequence[i].Schema.Statements(d, "")
				must.NoError(t, err, must.Sprintf("version %d dialect %q", sequence[i].Version, d))
				test.SliceNotEmpty(t, stmts, test.Sprintf("version %d dialect %q", sequence[i].Version, d))
			}
		}
	})

	// A fresh install is the versions in order, with nothing dropped or added
	// between them.
	T.Run("renders a fresh install as every version in order", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			var want []string

			for i := range sequence {
				stmts, err := sequence[i].Schema.Statements(d, "app")
				must.NoError(t, err)

				want = append(want, stmts...)
			}

			got, err := Statements(d, "app")
			must.NoError(t, err)
			test.Eq(t, want, got, test.Sprintf("dialect %q", d))
		}
	})
}

func TestStatementsSince(T *testing.T) {
	T.Parallel()

	// A database created from v15.0.0 has version 1's tables: it owes the
	// customer index and none of the creates it already ran.
	T.Run("renders only what a database at version 1 owes", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := StatementsSince(d, "", 1)
			must.NoError(t, err)

			want, versionErr := sequence[1].Schema.Statements(d, "")
			must.NoError(t, versionErr)
			test.Eq(t, want, stmts, test.Sprintf("dialect %q", d))

			must.SliceLen(t, 1, stmts, must.Sprintf("dialect %q", d))
			test.StrContains(t, stmts[0], "identity_accounts_customer_idx", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, stmts[0], "CREATE TABLE", test.Sprintf("dialect %q", d))
		}
	})

	// The read the index serves carries archived_at IS NULL, scope and the
	// customer by equality and orders by id; an index that left one out would
	// leave the read sorting or filtering the scope's accounts again.
	T.Run("covers the customer read in every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := StatementsSince(d, "", 1)
			must.NoError(t, err)
			must.SliceLen(t, 1, stmts, must.Sprintf("dialect %q", d))

			for _, column := range []string{"scope", "payment_processor_customer_id", "archived_at", "id)"} {
				test.StrContains(t, stmts[0], column, test.Sprintf("dialect %q is missing %s", d, column))
			}

			test.True(t,
				strings.Index(stmts[0], "scope") < strings.Index(stmts[0], "payment_processor_customer_id"),
				test.Sprintf("dialect %q does not lead with the scope", d))

			if d != dialect.MySQL {
				test.StrContains(t, stmts[0], "WHERE archived_at IS NULL", test.Sprintf("dialect %q", d))
			}
		}
	})

	T.Run("owes nothing at the latest version", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := StatementsSince(d, "", Latest())
			must.NoError(t, err)
			test.SliceEmpty(t, stmts, test.Sprintf("dialect %q", d))

			body, sqlErr := SQLSince(d, "", Latest())
			must.NoError(t, sqlErr)
			test.EqOp(t, "", body, test.Sprintf("dialect %q", d))
		}
	})

	// A database past Latest was migrated by a newer release than this one, and
	// an empty answer would let this one go on writing tables it does not know.
	T.Run("refuses a version past the latest", func(t *testing.T) {
		t.Parallel()

		_, err := StatementsSince(dialect.Postgres, "", Latest()+1)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)

		_, err = SQLSince(dialect.Postgres, "", Latest()+1)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
	})

	// Owing nothing is not a reason to accept a configuration that would fail
	// the next time this package ships a version.
	T.Run("vets the dialect and prefix even when nothing is owed", func(t *testing.T) {
		t.Parallel()

		_, err := StatementsSince(dialect.Dialect("oracle"), "", Latest())
		test.ErrorIs(t, err, dialect.ErrUnsupported)

		_, err = SQLSince(dialect.Postgres, "app_", Latest())
		test.ErrorIs(t, err, ddl.ErrPrefixTrailingSeparator)
	})

	T.Run("substitutes the prefix", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := StatementsSince(d, "custom", 1)
			must.NoError(t, err)

			joined := strings.Join(stmts, "\n")

			test.StrContains(t, joined, "custom_identity_accounts", test.Sprintf("dialect %q", d))
			test.StrContains(t, joined, "custom_identity_accounts_customer_idx", test.Sprintf("dialect %q", d))
			test.StrNotContains(t, joined, ddl.Placeholder, test.Sprintf("dialect %q", d))
		}
	})

	T.Run("SQLSince is the statements rejoined", func(t *testing.T) {
		t.Parallel()

		for _, d := range allDialects {
			stmts, err := StatementsSince(d, "app", 1)
			must.NoError(t, err)

			body, err := SQLSince(d, "app", 1)
			must.NoError(t, err)

			for _, stmt := range stmts {
				test.StrContains(t, body, strings.TrimSpace(stmt))
			}
		}
	})
}
