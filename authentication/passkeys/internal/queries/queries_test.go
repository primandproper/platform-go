package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passkeys/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI.
//
// The .sql files are what sqlc is run over, and the whole value of running it is
// that they are the statements the store executes. A hand-edit to one — or a
// column list changed without regenerating — would leave sqlc checking SQL
// nobody runs, which is a green check over an unchecked store.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			// The committed file carries the generated-code header, which is the
			// generator's rather than this function's.
			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_EmitsEveryStatementTheStoreNames is the other half of the drift
// gate: the store reaches these statements through the generated params types,
// so a statement renamed here and not there is a compile failure — but one
// dropped here entirely would only surface as a missing method.
func TestRender_EmitsEveryStatementTheStoreNames(T *testing.T) {
	T.Parallel()

	names := []string{
		CreateCredentialQuery,
		GetCredentialQuery,
		GetCredentialByCredentialIDQuery,
		GetArchivedCredentialQuery,
		ListCredentialsForUserQuery,
		RecordCredentialUseQuery,
		ArchiveCredentialForUserQuery,
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

// TestRender_RegistersTheTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// This table gets no standard set — nothing pages it — so nothing registers it as
// a side effect of emitting one. A consumer reading the registry back to truncate
// a database between integration tests would otherwise miss it, and the symptom
// would be a different test failing later on rows the previous one left behind.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	test.True(t, querygen.TableRegistered(CredentialsTable),
		test.Sprintf("%s is not registered", CredentialsTable))
}

// TestCredentialsTable_IsTheTableTheDDLCreates is the cross-check between the two
// halves of "what table does this store own": the canonical spelling here, which
// the registry and the store's prefix rendering both read, and the name the
// migrations package creates.
//
// Neither derives from the other on purpose — one is a Go constant a statement
// interpolates, the other is in the DDL — so this is where a rename in one and
// not the other stops being invisible.
func TestCredentialsTable_IsTheTableTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+CredentialsTable)
		})
	}
}

// TestCredentialColumns_AreTheColumnsTheDDLDeclares keeps the projection order
// and the schema in step. A column list is what every predicate and every
// projection here is derived from, so a column renamed in the DDL and not here
// renders SQL that sqlc rejects — which is the good failure, and this is the one
// that names the column.
func TestCredentialColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, column := range CredentialColumns {
				test.StrContains(t, ddl, column, test.Sprintf("column %q", column))
			}
		})
	}
}

// TestCredentialColumns_CarryTheConventionTriple pins what the column list says
// about every statement rendered from it. querygen derives the archived
// predicate and the last_updated_at stamp from this list, so dropping either
// column here would quietly turn every single-row read into one that answers
// with revoked passkeys.
func TestCredentialColumns_CarryTheConventionTriple(t *testing.T) {
	t.Parallel()

	for _, column := range []string{
		querygen.IDColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	} {
		test.True(t, slices.Contains(CredentialColumns, column), test.Sprintf("column %q", column))
	}
}

// TestInsertColumns_LeaveTheDatabaseItsOwn covers the two absences the create
// rests on: the columns the server assigns, and last_used_at.
func TestInsertColumns_LeaveTheDatabaseItsOwn(t *testing.T) {
	t.Parallel()

	insert := InsertColumns()

	for _, column := range []string{
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
		// A passkey that has just been registered has verified nothing, and the
		// column is what makes "enrolled and never used" answerable.
		LastUsedAtColumn,
	} {
		test.False(t, slices.Contains(insert, column), test.Sprintf("column %q", column))
	}

	for _, column := range []string{
		querygen.IDColumn,
		ScopeColumn,
		UserColumn,
		CredentialIDColumn,
		PublicKeyColumn,
		SignCountColumn,
	} {
		test.True(t, slices.Contains(insert, column), test.Sprintf("column %q", column))
	}
}

// TestRecordUse_StampsTheRowAsWellAsTheCeremony is the assertion behind the
// MySQL row-count caveat the store's doc names.
//
// MySQL's :execrows counts rows *changed* rather than matched, so a write that
// assigned only the two ceremony columns would report zero for a replayed
// assertion carrying the same counter — and the store reads zero as "no such
// credential". The conventional stamp in the SET list is what keeps the count
// meaning the same thing on all three engines.
func TestRecordUse_StampsTheRowAsWellAsTheCeremony(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), RecordCredentialUseQuery)

			test.StrContains(t, statement, SignCountColumn+" = ")
			test.StrContains(t, statement, LastUsedAtColumn+" = ")
			test.StrContains(t, statement, querygen.LastUpdatedAtColumn+" = ")
		})
	}
}

// TestArchivedRead_AssertsTheComplement is what makes the archive's read-back an
// assertion rather than a second chance.
//
// Every other single-row statement here filters archived_at IS NULL, so the
// read-back has to filter its complement: a guard that matched nothing cannot
// then be read back as a success.
func TestArchivedRead_AssertsTheComplement(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			statement := statementNamed(t, Render(d), GetArchivedCredentialQuery)

			test.StrContains(t, statement, "archived_at IS NOT NULL")
			test.StrNotContains(t, statement, "archived_at IS NULL")
		})
	}
}

// TestEveryStatementBindsTheScope is the tenancy clause, asserted against the
// rendered corpus rather than against a reading of the file.
//
// There is no unscoped statement here and there is not meant to be one: the read
// that omits the scope is the read that cannot tell one tenant's passkeys from
// another's, and a store cannot offer one it does not have.
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
