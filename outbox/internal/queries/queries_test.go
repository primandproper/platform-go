package queries

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/outbox/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// These tests read rendered SQL. They cannot say whether a server accepts it —
// that is sqlc, which runs over the committed files, and the outbox's container
// tests, which run it — but they can pin the parts that are silently wrong
// rather than loudly wrong: an ordering tuple that lost half of itself, a claim
// that stopped excluding quarantined rows, a lock clause that appeared where
// there is nothing to lock.

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI.
//
// The .sql files are what sqlc is run over, and the whole value of running it is
// that they are the statements the outbox executes. A hand-edit to one — or a
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
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_RegistersTheTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// Nothing here emits a standard set, so nothing registers the table as a side
// effect of emitting for it — Render registers it explicitly, and this pins that
// it does. A consumer reading the registry back to truncate a database between
// integration tests otherwise leaves rows behind, and the symptom is a different
// test failing later on rows the previous one left.
func TestRender_RegistersTheTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestOutboxTable_IsWhatTheDDLCreates is the cross-check between the canonical
// spelling every statement interpolates and the schema that creates the table.
// Neither derives from the other, which is what makes a rename visible here
// rather than at the first query.
func TestOutboxTable_IsWhatTheDDLCreates(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			test.StrContains(t, ddl, "CREATE TABLE IF NOT EXISTS "+OutboxTable+" (")
		})
	}
}

// TestColumns_AreTheColumnsTheDDLDeclares pins the other half of the
// schema-as-data claim, and it is the half a rendering test cannot make: the
// column list above is what the statements name and what the generated row
// types are built from, so a column renamed in a migration and not here yields a
// corpus sqlc rejects — but a column *missing* from the list yields one it
// accepts, over a projection quietly narrower than the table.
func TestColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			declared := columnsOf(t, ddl, OutboxTable)
			must.SliceNotEmpty(t, declared, must.Sprintf("no CREATE TABLE for %s", OutboxTable))

			want := slices.Clone(Columns)
			slices.Sort(want)
			slices.Sort(declared)

			test.Eq(t, want, declared)
		})
	}
}

// TestColumnLists_AreSubsetsOfTheTable keeps the four narrower lists honest.
// Each is what one statement supplies, assigns, or projects, and a name in one
// of them that the table does not have is a statement sqlc would reject on a
// dialect this test does not have to wait for.
func TestColumnLists_AreSubsetsOfTheTable(T *testing.T) {
	T.Parallel()

	for name, list := range map[string][]string{
		"InsertColumns":  InsertColumns,
		"FailureColumns": FailureColumns,
		"ClaimedColumns": ClaimedColumns,
		"Nullable":       Nullable,
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, column := range list {
				test.True(t, slices.Contains(Columns, column), test.Sprintf("column %q", column))
			}
		})
	}
}

// TestFailureColumns_LeaveTheClaimsCountAndTheRetirementAlone is the one
// column-list assertion worth making on its own, because both absences are
// semantic rather than typographical.
//
// A failure that assigned attempts would hand the counter back to a relay that
// has already consumed a claim, which is how a message that reliably kills its
// relay gets reclaimed forever instead of quarantining. One that assigned
// published_at would retire the row it just rescheduled.
func TestFailureColumns_LeaveTheClaimsCountAndTheRetirementAlone(t *testing.T) {
	t.Parallel()

	test.False(t, slices.Contains(FailureColumns, AttemptsColumn))
	test.False(t, slices.Contains(FailureColumns, PublishedAtColumn))
}

// TestRender_EmitsTheStatementsTheOutboxExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the outbox does not run.
//
// The claim appears twice, under its name and that name plus SkipLocked: a lock
// clause is statement text rather than a bound value, so it is answered by a
// second statement rather than by an argument.
func TestRender_EmitsTheStatementsTheOutboxExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		"InsertOutboxMessage",
		"SelectClaimableOutboxMessages",
		"SelectClaimableOutboxMessagesSkipLocked",
		"ClaimOutboxMessages",
		"FetchClaimedOutboxMessages",
		"MarkOutboxMessagesPublished",
		"RecordOutboxMessageFailure",
		"OutboxBacklog",
		"ReapPublishedOutboxMessages",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.Eq(t, want, statementNames(Render(d)))
		})
	}
}

// TestRender_LocksOnlyWhereTheDialectCan is the claim's dialect split.
//
// The unlocked form never carries the clause, on any dialect: it is what a
// single-relay deployment runs, and a lock clause on it would be a lease mode
// that quietly locked. The locked form carries it exactly where the dialect has
// it, and on SQLite the two statements are the same text under two names —
// which is correct rather than redundant, since one writer at a time is that
// engine's whole storage model and RelayConfig narrows the mode before it can
// reach the statement there.
func TestRender_LocksOnlyWhereTheDialectCan(T *testing.T) {
	T.Parallel()

	const clause = "FOR UPDATE SKIP LOCKED"

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := statements(Render(d))

			test.StrNotContains(t, rendered["SelectClaimableOutboxMessages"], clause)

			locked := rendered[SkipLockedName("SelectClaimableOutboxMessages")]
			if d.SupportsSkipLocked() {
				test.StrContains(t, locked, clause)
			} else {
				test.StrNotContains(t, locked, clause)
			}
		})
	}
}

// TestRender_TheClaimAndItsPredicateAgreeOnEarlier is the ordering guarantee,
// checked rather than asserted in a comment.
//
// The per-key predicate admits a keyed message only when no earlier unpublished
// row shares its key, and "earlier" is (created_at, id) rather than created_at
// alone — one Enqueue stamps every row with a single instant, so a bare `<`
// would let a whole batch be claimable at once and a failure on the first would
// publish the second ahead of it. The ORDER BY has to break ties the same way,
// or a batch can contain a pair it is about to reorder.
func TestRender_TheClaimAndItsPredicateAgreeOnEarlier(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range []string{
				"SelectClaimableOutboxMessages",
				SkipLockedName("SelectClaimableOutboxMessages"),
			} {
				claim := statement(t, Render(d), name)

				test.StrContains(t, claim, "prior.created_at < m.created_at")
				test.StrContains(t, claim, "prior.created_at = m.created_at AND prior.id < m.id")
				test.StrContains(t, claim, "ORDER BY m.created_at, m.id")

				// The read back of what the claim leased publishes in that same
				// order, which is the half a batch would otherwise reorder.
				test.StrContains(t, statement(t, Render(d), "FetchClaimedOutboxMessages"),
					"ORDER BY created_at, id")
			}
		})
	}
}

// TestRender_TheClaimSkipsWhatItMustNeverPublishTwice enumerates the four
// conditions a claimable row satisfies. Each is a way the relay could publish
// something it should not: an already-published row, a quarantined one, one
// whose backoff has not elapsed, or one another relay currently holds.
func TestRender_TheClaimSkipsWhatItMustNeverPublishTwice(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range []string{
				"SelectClaimableOutboxMessages",
				SkipLockedName("SelectClaimableOutboxMessages"),
			} {
				claim := statement(t, Render(d), name)

				test.StrContains(t, claim, "m.published_at IS NULL")
				test.StrContains(t, claim, "m.quarantined = FALSE")
				test.StrContains(t, claim, "m.next_attempt <= sqlc.arg("+NowArg+")")
				test.StrContains(t, claim,
					"m.claimed_until IS NULL OR m.claimed_until <= sqlc.arg("+LeaseExpiredByArg+")")
			}
		})
	}
}

// TestRender_TheClaimBoundsTheBatch pins the limit, because an unbounded claim
// is one that leases the whole backlog into a single relay's memory and holds
// every one of those leases while it publishes them serially.
func TestRender_TheClaimBoundsTheBatch(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range []string{
				"SelectClaimableOutboxMessages",
				SkipLockedName("SelectClaimableOutboxMessages"),
			} {
				test.StrContains(t, statement(t, Render(d), name), "LIMIT")
			}

			// So is the reap, for the reason a retention pass is capped at all:
			// a month of aged-out rows is a DELETE holding locks for minutes.
			test.StrContains(t, statement(t, Render(d), "ReapPublishedOutboxMessages"), "LIMIT")
		})
	}
}

// TestRender_TheClaimIncrementsTheAttempt is why the claim is a write at all.
//
// The count moves when a message is taken rather than when it fails, so a relay
// that dies mid-publish has still consumed an attempt — without which a message
// that reliably kills its relay is reclaimed forever instead of quarantining.
func TestRender_TheClaimIncrementsTheAttempt(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.StrContains(t, statement(t, Render(d), "ClaimOutboxMessages"), "attempts = attempts + 1")
		})
	}
}

// TestRender_MarkPublishedClearsTheLeaseAndTheReason pins the two NULLs the
// statement owns. A published message holding a lease is a row a later claim
// would wait on, and one still carrying the reason an earlier attempt failed is
// a row an operator reads as broken.
func TestRender_MarkPublishedClearsTheLeaseAndTheReason(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			marked := statement(t, Render(d), "MarkOutboxMessagesPublished")

			test.StrContains(t, marked, ClaimedUntilColumn+" = NULL")
			test.StrContains(t, marked, LastErrorColumn+" = NULL")
		})
	}
}

// TestRender_TheInsertBindsOneInstantTwice is the enqueue's whole shape: a new
// message is eligible immediately because its first next_attempt is its creation
// time, and the two columns are one argument because they are one moment.
func TestRender_TheInsertBindsOneInstantTwice(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			insert := statement(t, Render(d), "InsertOutboxMessage")

			test.EqOp(t, 2, strings.Count(insert, "sqlc.arg("+CreatedAtArg+")"))
			test.StrContains(t, insert, NextAttemptColumn)

			// The three state columns the schema defaults are not supplied, so
			// an enqueued message starts unclaimed, unpublished and unattempted
			// whatever a caller passes.
			for _, column := range []string{ClaimedUntilColumn, PublishedAtColumn, AttemptsColumn, QuarantinedColumn} {
				test.False(t, slices.Contains(InsertColumns, column), test.Sprintf("column %q", column))
			}
		})
	}
}

// TestRender_NoStatementBindsMoreThanOneRowsWorth is the ban on the multi-row
// insert, restated as something a test can see: the corpus holds one INSERT and
// it supplies one tuple.
//
// A VALUES list whose length is the batch's is a statement whose text is a
// function of its argument count, which no schema check can be run over — and it
// is what this package was ported to retire.
func TestRender_NoStatementBindsMoreThanOneRowsWorth(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.EqOp(t, 1, strings.Count(rendered, "INSERT INTO"))
			test.EqOp(t, 1, strings.Count(rendered, ") VALUES ("))
		})
	}
}

// TestRender_TheBacklogIsOneRoundTripAndNoRowWhenEmpty pins the health probe's
// shape. The depth and the age answer one question and have to be one snapshot;
// the grouping is what makes a drained queue an absent row rather than a row of
// zeroes with an unreadable instant in it.
func TestRender_TheBacklogIsOneRoundTripAndNoRowWhenEmpty(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			probe := statement(t, Render(d), "OutboxBacklog")

			test.StrContains(t, probe, "COUNT(*) AS depth")
			test.StrContains(t, probe, "AS oldest")
			test.StrContains(t, probe, "GROUP BY "+OutboxTable+"."+QuarantinedColumn)

			// Quarantined rows are excluded from both numbers: they are never
			// going to be published, so counting them would make a permanently
			// broken message look like a permanently growing backlog.
			test.EqOp(t, 2, strings.Count(probe, QuarantinedColumn+" = FALSE"))
		})
	}
}

// TestRender_TheReapComparesAgainstABoundHorizon is the clock question every
// sweep in this module answers, and the answer here is the Relay's clock.
//
// published_at is stamped by the Relay from the clock it was constructed with,
// and the cutoff is computed from that same clock, so a comparison against the
// server's CURRENT_TIMESTAMP would be two clocks — years apart under a test
// clock that only moves when a test moves it.
func TestRender_TheReapComparesAgainstABoundHorizon(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			reap := statement(t, Render(d), "ReapPublishedOutboxMessages")

			test.StrContains(t, reap, "sqlc.arg("+BeforeArg+")")
			test.StrNotContains(t, reap, querygen.NowExpression)

			// Only published rows are doomed. Without this the pass is a
			// truncate run a batch at a time.
			test.StrContains(t, reap, PublishedAtColumn+" IS NOT NULL")
		})
	}
}

// TestRender_NothingBindsAScope is the tenancy roster for a package that has
// none, checked rather than left to a comment.
//
// The outbox is the documented cross-tenant exception: one relay drains one
// queue for a whole deployment, and every statement here either addresses rows
// it is already holding or the queue as a whole. A statement that grew a scope
// predicate would be a filter this schema has no column for, and this is where
// that stops being invisible.
func TestRender_NothingBindsAScope(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.StrNotContains(t, Render(d), "scope")
		})
	}
}

// TestRender_NoStatementNamesAnUnprefixableTable is what makes the prefix
// substitution total: unison replaces the canonical table name in every
// statement at construction, and a name spelled some other way would survive
// into a query against a table the consumer's namespace does not have.
func TestRender_NoStatementNamesAnUnprefixableTable(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, body := range statements(Render(d)) {
				test.StrContains(t, body, OutboxTable, test.Sprintf("%s names no table", name))
			}
		})
	}
}

func TestFileName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "postgres_generated.sql", FileName(dialect.Postgres))
	test.EqOp(t, "mysql_generated.sql", FileName(dialect.MySQL))
	test.EqOp(t, "sqlite_generated.sql", FileName(dialect.SQLite))
}

func TestSkipLockedName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "SelectClaimableOutboxMessagesSkipLocked", SkipLockedName("SelectClaimableOutboxMessages"))
}

// statement returns one named statement out of a rendered file.
func statement(t *testing.T, rendered, name string) string {
	t.Helper()

	found, ok := statements(rendered)[name]
	must.True(t, ok, must.Sprintf("no statement named %q", name))

	return found
}

// statementNames lists the query names a rendered file declares, in the order
// it declares them — which is the order Render assembles them in, and so the
// order a reader of the .sql meets them.
func statementNames(rendered string) []string {
	var ordered []string

	for line := range strings.SplitSeq(rendered, "\n") {
		if match := annotation.FindStringSubmatch(line); match != nil {
			ordered = append(ordered, match[1])
		}
	}

	return ordered
}

// statements splits a rendered file into its named statements.
func statements(rendered string) map[string]string {
	found := map[string]string{}

	var (
		name string
		body strings.Builder
	)

	flush := func() {
		if name != "" {
			found[name] = body.String()
		}

		body.Reset()
	}

	for line := range strings.SplitSeq(rendered, "\n") {
		if match := annotation.FindStringSubmatch(line); match != nil {
			flush()

			name = match[1]

			continue
		}

		body.WriteString(line)
		body.WriteString("\n")
	}

	flush()

	return found
}

// annotation matches the `-- name: X :type` line sqlc reads above a statement.
var annotation = regexp.MustCompile(`^-- name: (\w+) :`)

// columnsOf reads one table's column names out of rendered DDL.
//
// It parses the CREATE TABLE body rather than asking a database, so the check
// runs with nothing installed: what it needs is the first identifier of every
// line that declares a column, and the constraint lines this schema writes are
// the ones it skips.
func columnsOf(t *testing.T, ddl, table string) []string {
	t.Helper()

	open := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS "+table+" (")
	if open < 0 {
		return nil
	}

	body := ddl[open:]
	body = body[strings.Index(body, "(")+1 : strings.Index(body, "\n);")]

	var columns []string

	for line := range strings.SplitSeq(body, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}

		switch strings.ToUpper(fields[0]) {
		case "PRIMARY", "UNIQUE", "FOREIGN", "CONSTRAINT", "CHECK", "KEY", "INDEX":
			continue
		}

		columns = append(columns, fields[0])
	}

	return columns
}
