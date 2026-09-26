package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/grants/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI: the .sql files are what sqlc checks and what unison
// generates the executed statements from, so a hand-edit to one leaves sqlc
// checking SQL nobody runs.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

func TestRender_EmitsEveryStatementTheStoreNames(T *testing.T) {
	T.Parallel()

	names := []string{
		PutGrantQuery,
		GetGrantQuery,
		GetGrantForProviderQuery,
		GetRevokedGrantQuery,
		ListGrantsForSubjectsQuery,
		RefreshGrantQuery,
		RevokeGrantQuery,
		ArchiveGrantQuery,
		DeleteGrantsForSubjectQuery,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, name := range names {
				test.StrContains(t, rendered, "-- name: "+name+" ", test.Sprintf("statement %q", name))
			}

			test.EqOp(t, len(names), strings.Count(rendered, "-- name: "))
		})
	}
}

// TestRender_RegistersTheTable keeps the table in the registry a consumer reads
// back to truncate a database between integration tests. Nothing here renders a
// standard set, so nothing registers it as a side effect of one.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	test.True(t, querygen.TableRegistered(GrantsTable), test.Sprintf("%s is not registered", GrantsTable))
}

func TestGrantsTable_IsTheTableTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+GrantsTable)
		})
	}
}

func TestGrantColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, column := range GrantColumns {
				test.StrContains(t, ddl, column, test.Sprintf("column %q", column))
			}
		})
	}
}

// TestMetadataColumns_AreGrantColumnsWithoutTheTokens pins the export's
// projection: every column of the table but the two that hold a credential.
func TestMetadataColumns_AreGrantColumnsWithoutTheTokens(t *testing.T) {
	t.Parallel()

	test.Eq(t, without(GrantColumns, AccessTokenColumn, RefreshTokenColumn), MetadataColumns)
}

func TestInsertColumns_LeaveTheDatabaseItsOwn(t *testing.T) {
	t.Parallel()

	insert := InsertColumns()

	for _, column := range []string{
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	} {
		test.False(t, slices.Contains(insert, column), test.Sprintf("column %q", column))
	}
}

// TestPut_ReplacesEverythingButTheKeyAndTheCreationStamp is what makes a consent
// a replacement, asserted against the rendered text. The conflict branch takes
// a new id, so an in-flight refresh against the old grant matches nothing. It
// clears the revocation, so a consent revives a revoked key. It leaves
// created_at alone.
func TestPut_ReplacesEverythingButTheKeyAndTheCreationStamp(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), PutGrantQuery)
			_, branch, found := strings.Cut(statement, "ON ")
			must.True(t, found)

			for _, column := range []string{
				querygen.IDColumn,
				RevocationReasonColumn,
				AccessTokenColumn,
				RefreshTokenColumn,
				querygen.ArchivedAtColumn + " = NULL",
			} {
				test.StrContains(t, branch, column, test.Sprintf("column %q", column))
			}

			for _, column := range []string{ScopeColumn + " =", SubjectColumn + " =", ProviderColumn + " =", querygen.CreatedAtColumn} {
				test.StrNotContains(t, branch, column, test.Sprintf("column %q", column))
			}
		})
	}
}

// TestRefresh_IsACompareAndSet is the property the refresh rests on, asserted
// against the rendered text: the write names the ciphertext it requires the row
// still to hold, under an argument name apart from the one it assigns.
func TestRefresh_IsACompareAndSet(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), RefreshGrantQuery)

			test.StrContains(t, statement, AccessTokenColumn+" = sqlc.arg("+AccessTokenColumn+")")
			test.StrContains(t, statement, AccessTokenColumn+" = sqlc.arg("+ExpectedAccessTokenArg+")")
			test.StrContains(t, statement, querygen.ArchivedAtColumn+" IS NULL")

			// MySQL's :execrows counts rows changed rather than matched; the
			// stamp is one more column every run of this statement changes.
			test.StrContains(t, statement, querygen.LastUpdatedAtColumn+" = ")
		})
	}
}

// TestRevoke_EmptiesTheTokens is what makes a revoked row a record rather than a
// credential.
func TestRevoke_EmptiesTheTokens(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), RevokeGrantQuery)

			test.StrContains(t, statement, AccessTokenColumn+" = ")
			test.StrContains(t, statement, RefreshTokenColumn+" = ")
			test.StrContains(t, statement, RevocationReasonColumn+" = ")
			test.StrContains(t, statement, querygen.ArchivedAtColumn+" IS NULL")
		})
	}
}

func TestRevokedRead_AssertsTheComplement(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), GetRevokedGrantQuery)

			test.StrContains(t, statement, "archived_at IS NOT NULL")
			test.StrNotContains(t, statement, "archived_at IS NULL")
		})
	}
}

// TestSubjectStatements_SeeRevokedRows is the property the privacy pair and the
// consent are built on: all three name a subject rather than a row, and all
// three have to reach revoked grants.
func TestSubjectStatements_SeeRevokedRows(T *testing.T) {
	T.Parallel()

	for _, name := range []string{ListGrantsForSubjectsQuery, DeleteGrantsForSubjectQuery, PutGrantQuery} {
		T.Run(name, func(T *testing.T) {
			T.Parallel()

			for _, d := range everyDialect {
				T.Run(string(d), func(t *testing.T) {
					t.Parallel()

					statement := statementNamed(t, Render(d), name)

					test.StrNotContains(t, statement, querygen.ArchivedAtColumn+" IS NULL")
					test.StrContains(t, statement, SubjectColumn)
				})
			}
		})
	}
}

// TestListForSubjects_NeverSelectsATokenColumn is the export's guarantee against
// the rendered text: the ciphertext is not in the projection at all.
func TestListForSubjects_NeverSelectsATokenColumn(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), ListGrantsForSubjectsQuery)

			projection, _, found := strings.Cut(statement, "\nFROM ")
			must.True(t, found, must.Sprint("the read has no FROM clause"))

			test.StrNotContains(t, projection, querygen.Qualify(GrantsTable, AccessTokenColumn)+",")
			test.StrNotContains(t, projection, querygen.Qualify(GrantsTable, RefreshTokenColumn))
			test.StrContains(t, projection, querygen.Qualify(GrantsTable, querygen.ArchivedAtColumn))
		})
	}
}

// TestEveryStatementBindsTheScope is the tenancy clause, asserted against the
// rendered corpus.
func TestEveryStatementBindsTheScope(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, statement := range strings.Split(Render(d), "-- name: ")[1:] {
				test.StrContains(t, statement, "sqlc.arg(scope)",
					test.Sprintf("statement %q", strings.SplitN(statement, " ", 2)[0]))
			}
		})
	}
}

// statementNamed returns one rendered statement out of the corpus.
func statementNamed(t *testing.T, rendered, name string) string {
	t.Helper()

	marker := "-- name: " + name + " "

	start := strings.Index(rendered, marker)
	must.True(t, start >= 0, must.Sprintf("statement %q is not in the corpus", name))

	rest := rendered[start:]
	if next := strings.Index(rest[len(marker):], "-- name: "); next >= 0 {
		rest = rest[:len(marker)+next]
	}

	return rest
}
