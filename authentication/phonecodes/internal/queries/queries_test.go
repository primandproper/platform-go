package queries

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/phonecodes/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// statements splits a rendered corpus into its named statements.
func statements(t *testing.T, d dialect.Dialect) map[string]string {
	t.Helper()

	named := map[string]string{}
	header := regexp.MustCompile(`^-- name: (\w+) `)

	for block := range strings.SplitSeq(Render(d), "\n\n") {
		match := header.FindStringSubmatch(block)
		if match == nil {
			continue
		}

		named[match[1]] = block
	}

	return named
}

// TestRender_MatchesTheCommittedFiles is the regeneration gate. The .sql files
// are what sqlc is run over, and the whole value of running it is that they are
// the statements the store executes.
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

// TestRender_RegistersTheTable: this table gets no standard set, so nothing
// registers it as a side effect of emitting one.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	test.True(t, querygen.TableRegistered(CodesTable), test.Sprintf("%s is not registered", CodesTable))
}

func TestCodesTable_IsTheTableTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+CodesTable)

			for _, column := range Columns {
				test.StrContains(t, ddl, column, test.Sprintf("column %q", column))
			}
		})
	}
}

func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		IssueCodeQuery, GetCodeQuery, SpendCodeQuery, CountAttemptQuery,
		RevokeForSubjectQuery, ListForSubjectQuery, DeleteForSubjectQuery, SweepCodesQuery,
	}
	slices.Sort(want)

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			var got []string
			for name := range statements(t, d) {
				got = append(got, name)
			}

			slices.Sort(got)
			test.Eq(t, want, got)
		})
	}
}

// No read projects the digest: nothing in Go compares it, so projecting it
// would only hand a stored credential's digest to whatever a caller did next.
func TestRender_ProjectsNoDigest(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			named := statements(t, d)

			for _, name := range []string{GetCodeQuery, ListForSubjectQuery} {
				test.StrNotContains(t, named[name], CodeHashColumn, test.Sprintf("%s projects the digest", name))
			}
		})
	}
}

// Every statement but the sweep is scoped; the sweep is the machinery carve-out.
func TestRender_EveryStatementButTheSweepIsScoped(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, statement := range statements(t, d) {
				if name == SweepCodesQuery {
					test.StrNotContains(t, statement, "scope", test.Sprint(name))

					continue
				}

				test.StrContains(t, statement, "scope", test.Sprint(name))
			}
		})
	}
}

// The spend repeats every row-state test its answer depends on, and the count
// repeats every one but the digest.
func TestRender_GuardedWritesRepeatEveryRowStateTest(T *testing.T) {
	T.Parallel()

	guards := []string{
		"id = ",
		"scope = ",
		"redeemed_at IS NULL",
		"revoked_at IS NULL",
		"expires_at > ",
		"attempts = sqlc.arg(" + ExpectedAttemptsArg + ")",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			named := statements(t, d)

			for _, guard := range guards {
				test.StrContains(t, named[SpendCodeQuery], guard, test.Sprintf("spend lacks %q", guard))
				test.StrContains(t, named[CountAttemptQuery], guard, test.Sprintf("count lacks %q", guard))
			}

			test.StrContains(t, named[SpendCodeQuery], "code_hash = ")
			test.StrContains(t, named[CountAttemptQuery], "attempts = sqlc.arg(attempts)")
		})
	}
}

// The issue replaces the number's row outright: every column the insert binds
// is assigned on conflict, so attempts and both stamps start over.
func TestRender_IssueReplacesTheWholeRow(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			issue := statements(t, d)[IssueCodeQuery]

			for _, column := range []string{"id", AttemptsColumn, RedeemedAtColumn, RevokedAtColumn, CodeHashColumn, ExpiresAtColumn} {
				test.True(t, regexp.MustCompile(`(?m)^\t`+column+` = `).MatchString(issue),
					test.Sprintf("the conflict branch does not assign %q", column))
			}
		})
	}
}

// Every clock comparison binds the store's clock; the server's has no say.
func TestRender_ComparesAgainstNoServerClock(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, clock := range []string{"CURRENT_TIMESTAMP", "NOW()", "UTC_TIMESTAMP"} {
				test.StrNotContains(t, rendered, clock)
			}
		})
	}
}
