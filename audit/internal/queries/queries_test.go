package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/audit/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

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
// that they are the statements the stores execute. A hand-edit to one — or a
// column list changed without regenerating — would leave sqlc checking SQL
// nobody runs, which is a green check over an unchecked store.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			// The committed file carries the generated-code header, which is
			// the generator's rather than this function's.
			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make unison` and commit %s", FileName(d)))
		})
	}
}

// TestRender_RegistersBothTables is the registry half of the same guarantee the
// canonical .sql files are the query half of: a consumer reading the registry
// back to truncate a database between integration tests gets every table this
// component has rows in.
func TestRender_RegistersBothTables(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestColumns_AreTheColumnsTheDDLDeclares is the cross-check between the two
// halves of "what does this schema hold": the lists here, which every statement
// is rendered from, and the DDL a consumer actually creates the tables with.
//
// Neither derives from the other on purpose, so a column added to one and not
// the other stops being invisible. It matters most for the two the chain's
// shape list leaves out: archived_at buys the statements no predicate and
// created_at buys the insert no argument, and both of those are decisions about
// a column that has to exist.
func TestColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, table := range TableNames {
				must.StrContains(t, ddl, table)
			}

			for _, column := range slices.Concat(EntryColumns, ChainColumns) {
				test.StrContains(t, ddl, column, test.Sprintf("%s is not in the %s DDL", column, d))
			}
		})
	}
}

// TestRender_EmitsTheStatementsTheStoresExecute pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement no store runs.
//
// The list is the corpus in the order it renders, and three of the twenty are
// not audit's own — they are the erasure's, because dataprivacy/auditerasure
// owns no table and its statements address these.
func TestRender_EmitsTheStatementsTheStoresExecute(T *testing.T) {
	T.Parallel()

	want := []string{
		ChainQuery,
		LockChainQuery,
		CreateChainQuery,
		AdvanceChainHeadQuery,
		RecordChainPruneQuery,
		InsertEntryQuery,
		GetEntryQuery,
		GetEntryBySeqQuery,
		ListChainEntriesQuery,
		ListEntriesQuery,
		querygen.DescendingName(ListEntriesQuery),
		PrunableScopesQuery,
		PrunableScopesFromQuery,
		BacklogQuery,
		PruneBoundsQuery,
		PruneTargetQuery,
		PruneEntriesQuery,
		EraseEntriesQuery,
		EraseChainsQuery,
		SubjectMentionsQuery,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.Eq(t, want, statementNames(Render(d)))
		})
	}
}

// TestRender_KeysTheChainOnItsScope is the natural-key property, and it is the
// one this schema cannot be wrong about.
//
// audit_log_chains has no id: the scope is the key, and the row it addresses is
// what serializes two writers recording into one tenant. A statement that
// addressed the row by anything else would be a statement that locked the wrong
// row, or none.
func TestRender_KeysTheChainOnItsScope(T *testing.T) {
	T.Parallel()

	keyed := []string{ChainQuery, LockChainQuery, AdvanceChainHeadQuery, RecordChainPruneQuery}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, name := range keyed {
				statement := statement(t, rendered, name)

				test.StrContains(t, statement, "scope = sqlc.arg(scope)",
					test.Sprintf("%s does not key on the scope", name))
				test.StrNotContains(t, statement, "id = sqlc.arg(id)",
					test.Sprintf("%s keys on an id this table does not have", name))
			}

			// The insert names the same key as its conflict target, on the one
			// dialect that spells a target at all.
			test.StrContains(t, Render(dialect.Postgres), "ON CONFLICT (scope) DO NOTHING")
		})
	}
}

// TestRender_LocksOnlyWhereTheDialectCan pins the one statement in this corpus
// whose text differs between dialects for a reason that is not an expression.
//
// The locked read is what serializes two writers into one scope. Postgres and
// MySQL take the row lock; SQLite has neither the clause nor the concurrency it
// exists for, so its arm is the unlocked read — correct there rather than
// missing.
func TestRender_LocksOnlyWhereTheDialectCan(T *testing.T) {
	T.Parallel()

	T.Run("postgres and mysql hold the row", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL} {
			test.StrContains(t, statement(t, Render(d), LockChainQuery), "FOR UPDATE",
				test.Sprintf("dialect %q", d))
		}
	})

	T.Run("sqlite takes the same read unlocked", func(t *testing.T) {
		t.Parallel()

		rendered := Render(dialect.SQLite)

		test.StrNotContains(t, statement(t, rendered, LockChainQuery), "FOR UPDATE")

		// Same projection and same predicate as the unlocked read, which is the
		// property that makes them one question asked under two locks.
		test.EqOp(t,
			strings.TrimSpace(statement(t, rendered, ChainQuery)[len(ChainQuery):]),
			strings.TrimSpace(statement(t, rendered, LockChainQuery)[len(LockChainQuery):]))
	})
}

// TestRender_WindowsTheReadsOverRecordedAt pins the fragment this corpus needed
// querygen to export.
//
// The entries table has no created_at, so the window every other list in this
// module derives from its column list has to be named here — and it must be
// named against recorded_at, which is the column a reader means when they ask
// for events in a date range.
func TestRender_WindowsTheReadsOverRecordedAt(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			// The paged read binds the filter's own argument names, because
			// that is where its bounds come from.
			listing := statement(t, rendered, ListEntriesQuery)
			test.StrContains(t, listing, "recorded_at > COALESCE(sqlc.narg(created_after)")
			test.StrContains(t, listing, "recorded_at < COALESCE(sqlc.narg(created_before)")

			// The verification walk binds names of its own, because its bounds
			// are two parameters of a method rather than a filter's fields.
			walk := statement(t, rendered, ListChainEntriesQuery)
			test.StrContains(t, walk, "recorded_at > COALESCE(sqlc.narg(recorded_after)")
			test.StrContains(t, walk, "recorded_at < COALESCE(sqlc.narg(recorded_before)")

			// And no statement anywhere windows over a column this table does
			// not have.
			test.StrNotContains(t, rendered, "created_at >")
			test.StrNotContains(t, rendered, "created_at <")
		})
	}
}

// TestRender_NarrowsTheListingRatherThanFilteringToASentinel pins the reading
// an absent selector gets, which is the one thing about this listing that is a
// disclosure rather than a wrong answer if it is backwards.
//
// The empty string is a real scope — the one platform-level events are recorded
// in — so a predicate that read an absent argument as "the empty one" would
// answer an operator console asking for everything with the platform's own
// events, and would leave "only the platform's" inexpressible.
func TestRender_NarrowsTheListingRatherThanFilteringToASentinel(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			listing := statement(t, Render(d), ListEntriesQuery)

			for _, column := range SelectorColumns {
				arg := SelectorArg(column)

				test.StrContains(t, listing, column+" = sqlc.narg("+arg+")",
					test.Sprintf("%s does not narrow on a nullable argument", column))
				test.StrNotContains(t, listing, column+" = COALESCE(sqlc.narg("+arg+")",
					test.Sprintf("%s filters an absent argument to a sentinel", column))
			}
		})
	}
}

// TestRender_CountsTheFilterButNotTheWindow pins which of the two counts the
// window rides in.
//
// filtered_count answers "how many rows match this filter" and total_count
// answers "how many are there to filter". A window in both would make a total
// that shrank as a caller narrowed the date range, which is a progress bar
// measuring its own progress.
func TestRender_CountsTheFilterButNotTheWindow(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			listing := statement(t, Render(d), ListEntriesQuery)

			filtered := between(t, listing, "filtered_count")
			total := between(t, listing, "total_count")

			test.StrContains(t, filtered, "sqlc.narg(created_after)")
			test.StrNotContains(t, total, "sqlc.narg(created_after)")

			// Both carry every selector, or the subset would be larger than the
			// set it is a subset of.
			for _, column := range SelectorColumns {
				test.StrContains(t, filtered, column+" = sqlc.narg(")
				test.StrContains(t, total, column+" = sqlc.narg(")
			}
		})
	}
}

// TestRender_PagesTheScopeListingInTwoStatements pins the enumeration the
// retention sweep's keyset walk needs.
//
// Every other keyset walk in this module coalesces an absent cursor to the
// empty string, which is safe because no id is empty. A scope can be, so the
// first page and the pages after it are two named statements rather than one
// with an optional cursor — otherwise the first page would begin just past the
// platform's own scope and the log's own events would never be swept.
func TestRender_PagesTheScopeListingInTwoStatements(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			first := statement(t, rendered, PrunableScopesQuery)
			test.StrNotContains(t, first, querygen.CursorArg)

			after := statement(t, rendered, PrunableScopesFromQuery)
			test.StrContains(t, after, "scope > sqlc.arg("+querygen.CursorArg+")")

			// Both are DISTINCT: a scope holding a thousand aged entries is one
			// scope to visit, not a thousand.
			test.StrContains(t, first, "SELECT DISTINCT")
			test.StrContains(t, after, "SELECT DISTINCT")
		})
	}
}

// TestRender_BoundsThePrune pins the cap, which is the whole reason the delete
// is a shape rather than a statement.
//
// It is also where the key matters: the pass removes a prefix of one scope's
// chain, and (scope, seq) is the unique index that prefix is defined by.
func TestRender_BoundsThePrune(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			prune := statement(t, Render(d), PruneEntriesQuery)

			test.StrContains(t, prune, "LIMIT")
			test.StrContains(t, prune, "scope = sqlc.arg(scope)")
			test.StrContains(t, prune, "seq <= sqlc.arg("+ThroughSeqArg+")")
			test.StrContains(t, prune, "ORDER BY")
		})
	}
}

// TestRender_ErasesByScopeSetAndCountsByDisjunction pins the two properties
// dataprivacy/auditerasure depends on and cannot check from its own package.
//
// The deletes key on a set because a subject may own several scopes, and the
// count is a disjunction because an entry naming the subject twice — as the
// actor and as the resource — is one entry rather than two.
func TestRender_ErasesByScopeSetAndCountsByDisjunction(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, name := range []string{EraseEntriesQuery, EraseChainsQuery} {
				erase := statement(t, rendered, name)

				test.StrContains(t, erase, "DELETE FROM")
				test.StrContains(t, erase, ScopesArg, test.Sprintf("%s does not key on the scope set", name))
			}

			mentions := statement(t, rendered, SubjectMentionsQuery)
			test.StrContains(t, mentions, "actor_id = sqlc.arg("+SubjectIDArg+")")
			test.StrContains(t, mentions, "OR")
			test.StrContains(t, mentions, "resource_id = sqlc.arg("+SubjectIDArg+")")
		})
	}
}

// TestRender_LeavesTheEntriesTableItsOwnShape pins the three predicates this
// corpus must not have grown.
//
// The entries table carries none of the convention's timestamps, so nothing
// rendered from its column list may filter on one. An archived predicate in
// particular would be a read that hid rows the schema has no way to create.
func TestRender_LeavesTheEntriesTableItsOwnShape(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.StrNotContains(t, rendered, querygen.ArchivedAtColumn)
			test.StrNotContains(t, rendered, querygen.IncludeArchivedArg)

			// The chain's writes do stamp, from the server rather than from a
			// bound value: the column is bookkeeping on a row nothing hashes.
			for _, name := range []string{AdvanceChainHeadQuery, RecordChainPruneQuery} {
				write := statement(t, rendered, name)

				test.StrContains(t, write, querygen.LastUpdatedAtColumn+" = ")
				test.StrNotContains(t, write, querygen.LastUpdatedAtColumn+" = sqlc.")
			}
		})
	}
}

// TestRender_InsertsOneEntryPerStatement pins the shape the multi-row INSERT
// became.
//
// A VALUES list assembled per call has no static text for sqlc to check, so it
// is the one form that cannot be on this tier at all. What replaced it is one
// statement per entry, which is why the insert names each column exactly once.
func TestRender_InsertsOneEntryPerStatement(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			insert := statement(t, Render(d), InsertEntryQuery)

			test.EqOp(t, 1, strings.Count(insert, "VALUES"))

			for _, column := range EntryColumns {
				test.EqOp(t, 1, strings.Count(insert, "sqlc.arg("+column+")")+
					strings.Count(insert, "sqlc.narg("+column+")"),
					test.Sprintf("%s is not bound exactly once", column))
			}
		})
	}
}

// TestSelectorArgs_AreNotTheColumnNames pins the separation MySQL forces and
// the reading it protects.
//
// A selector's argument shares a predicate with its column but not a name: one
// arm of that predicate is a bare cast, which MySQL's analyzer resolves to a
// string type of its own while the other two resolve the column's, so under one
// name the three engines converge on nothing.
func TestSelectorArgs_AreNotTheColumnNames(t *testing.T) {
	t.Parallel()

	for _, column := range SelectorColumns {
		test.NotEqOp(t, column, SelectorArg(column))
		test.StrHasSuffix(t, SelectorArgSuffix, SelectorArg(column))
		test.True(t, slices.Contains(EntryColumns, column),
			test.Sprintf("%s is a selector over a column the table does not have", column))
	}
}

// TestKeyedEntryColumns_AreTheRowLessTheID pins the idiom a read keyed on
// something other than the id uses: the id predicate is rendered from the
// presence of the column, so a read keyed on (scope, seq) hands over a list
// without it and names it in the projection instead.
func TestKeyedEntryColumns_AreTheRowLessTheID(t *testing.T) {
	t.Parallel()

	test.False(t, slices.Contains(KeyedEntryColumns, querygen.IDColumn))
	test.EqOp(t, len(EntryColumns)-1, len(KeyedEntryColumns))

	for _, column := range KeyedEntryColumns {
		test.True(t, slices.Contains(EntryColumns, column))
	}

	// It still projects the whole row, which is what the seq-keyed read hands
	// its caller.
	for _, d := range everyDialect {
		test.StrContains(t, statement(t, Render(d), GetEntryBySeqQuery),
			querygen.Qualify(EntriesTable, querygen.IDColumn))
	}
}

// TestChainStateColumns_AreTheRowLessTheTwoNothingAssigns pins the gap between
// the whole chain row and the list its statements are rendered from, since that
// gap is what leaves them with no archived predicate and the genesis row with
// one bound value.
func TestChainStateColumns_AreTheRowLessTheTwoNothingAssigns(t *testing.T) {
	t.Parallel()

	for _, column := range ChainStateColumns {
		test.True(t, slices.Contains(ChainColumns, column),
			test.Sprintf("%s is rendered from but is not a column", column))
	}

	test.False(t, slices.Contains(ChainStateColumns, querygen.ArchivedAtColumn))
	test.False(t, slices.Contains(ChainStateColumns, querygen.CreatedAtColumn))
	test.True(t, slices.Contains(ChainStateColumns, querygen.LastUpdatedAtColumn))

	test.Eq(t, []string{ScopeColumn}, ChainInsertColumns)
}

// TestFileName names the file each dialect is committed to.
func TestFileName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "postgres_generated.sql", FileName(dialect.Postgres))
	test.EqOp(t, "mysql_generated.sql", FileName(dialect.MySQL))
	test.EqOp(t, "sqlite_generated.sql", FileName(dialect.SQLite))
}

// statementNames reads the query names out of a rendered file, in order.
func statementNames(rendered string) []string {
	var names []string

	for line := range strings.SplitSeq(rendered, "\n") {
		if after, found := strings.CutPrefix(line, "-- name: "); found {
			names = append(names, strings.Fields(after)[0])
		}
	}

	return names
}

// statement returns one named statement's text out of a rendered file, so an
// assertion about the prune is not accidentally satisfied by the listing.
func statement(t *testing.T, rendered, name string) string {
	t.Helper()

	for block := range strings.SplitSeq(rendered, "-- name: ") {
		if strings.HasPrefix(block, name+" ") {
			return block
		}
	}

	t.Fatalf("no statement named %q in the rendered file", name)

	return ""
}

// between returns the text of one of a listing's two count subqueries, so an
// assertion about the window's absence from the total is not satisfied by its
// presence in the filter beside it.
func between(t *testing.T, statement, alias string) string {
	t.Helper()

	end := strings.Index(statement, ") AS "+alias)
	must.Greater(t, -1, end, must.Sprintf("no %s subquery", alias))

	start := strings.LastIndex(statement[:end], "\t(\n")
	must.Greater(t, -1, start, must.Sprintf("no opening for the %s subquery", alias))

	return statement[start:end]
}
