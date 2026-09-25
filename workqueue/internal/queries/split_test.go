package queries

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The split corpus, read the way the Postgres one is read above: for the parts
// that are silently wrong rather than loudly wrong. Whether MySQL and SQLite
// accept a statement is sqlc's to say, and whether it behaves is workqueue's
// container suites'; what can be said from the text is whether a fence, a lock
// order, a re-check or a rounding direction is still there.

// splitDialects is the roster the split corpus is rendered for.
var splitDialects = []dialect.Dialect{dialect.MySQL, dialect.SQLite}

// splitCorpus is every statement the split corpus renders for one dialect,
// keyed by name.
func splitCorpus(t *testing.T, d dialect.Dialect) map[string]string {
	t.Helper()

	statements := map[string]string{}

	for block := range strings.SplitSeq(Render(d), "-- name: ") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		header, body, found := strings.Cut(block, "\n")
		must.True(t, found, must.Sprintf("statement %q has no body", header))

		name, _, _ := strings.Cut(header, " ")
		statements[name] = body
	}

	return statements
}

func splitStatement(t *testing.T, d dialect.Dialect, name string) string {
	t.Helper()

	found, ok := splitCorpus(t, d)[name]
	must.True(t, ok, must.Sprintf("no %s statement named %q", d, name))

	return found
}

// TestRenderSplit_MatchesTheCommittedFiles is the drift gate for the two files
// unison.split.yaml generates from.
func TestRenderSplit_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		committed, err := os.ReadFile(FileName(d))
		must.NoError(T, err)

		body := string(committed)
		if index := strings.Index(body, "-- name:"); index > 0 {
			body = body[index:]
		}

		test.EqOp(T, Render(d), body, test.Sprintf("run `make generate` and commit %s", FileName(d)))
	}
}

// TestRenderSplit_EmitsTheStatementsTheQueueExecutes pins the set, for the
// Postgres test's reason: a statement emitted and not executed is SQL nobody
// checks the other way round.
func TestRenderSplit_EmitsTheStatementsTheQueueExecutes(T *testing.T) {
	T.Parallel()

	expected := []string{
		"EnqueueItem",
		"SelectDueItems",
		"LeaseItems",
		"FetchLeasedItems",
		"ExtendItems",
		"CountHeldItems",
		"CompleteItems",
		"ReleaseItems",
		"RemoveItems",
		"RequeueItems",
		"SelectReapableItems",
		"DeleteReapedItems",
		"ReadQueueStats",
	}

	for _, d := range splitDialects {
		rendered := splitCorpus(T, d)

		test.MapLen(T, len(expected), rendered, test.Sprintf("dialect %s", d))

		for _, name := range expected {
			_, ok := rendered[name]
			test.True(T, ok, test.Sprintf("%s statement %q is not emitted", d, name))
		}
	}
}

// TestRenderSplit_TheLockingReadsSkipWhatTheyCannotHave. SKIP LOCKED is what
// keeps two MySQL claimers from waiting on each other, and it is the LIMIT
// sitting before it — on the read itself, not in a subquery beneath the lock —
// that makes a claimer's batch full. SQLite has no row lock and must not be
// handed a clause it cannot parse.
func TestRenderSplit_TheLockingReadsSkipWhatTheyCannotHave(T *testing.T) {
	T.Parallel()

	for _, name := range []string{"SelectDueItems", "SelectReapableItems"} {
		mysql := splitStatement(T, dialect.MySQL, name)
		test.True(T, strings.HasSuffix(mysql, "LIMIT ?\nFOR UPDATE SKIP LOCKED;"), test.Sprintf("mysql %s:\n%s", name, mysql))

		sqlite := splitStatement(T, dialect.SQLite, name)
		test.StrNotContains(T, sqlite, "FOR UPDATE", test.Sprintf("sqlite %s", name))
		test.True(T, strings.HasSuffix(sqlite, "LIMIT sqlc.arg("+LimitArg+");"), test.Sprintf("sqlite %s:\n%s", name, sqlite))
	}
}

// TestRenderSplit_TheLeaseRepeatsTheClaimsTest. A write has to hold every
// row-state test its select held, or it leases whatever the rows became in
// between — the outbox double-publish, in a different table.
func TestRenderSplit_TheLeaseRepeatsTheClaimsTest(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		s := newSplit(d)
		lease := splitStatement(T, d, "LeaseItems")

		test.StrContains(T, lease, s.claimable(), test.Sprintf("dialect %s", d))
		test.StrContains(T, splitStatement(T, d, "SelectDueItems"), s.claimable(), test.Sprintf("dialect %s", d))
		test.StrContains(T, splitStatement(T, d, "ReadQueueStats"), s.claimable(), test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_TheReadBackFindsTheClaimByItsName. The name is the fact: a
// key the read selected and the lease did not take is not this claim's.
func TestRenderSplit_TheReadBackFindsTheClaimByItsName(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		fetch := splitStatement(T, d, "FetchLeasedItems")

		test.StrContains(T, fetch, HolderColumn+" = sqlc.arg("+HolderArg+")", test.Sprintf("dialect %s", d))
		test.StrNotContains(T, fetch, KeysArg, test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_TheOutcomeWritesFenceOnTheClaim is the #727 fence on these
// two engines: every write reporting on a claim names it, and the ones that
// must not touch finished work exclude it.
func TestRenderSplit_TheOutcomeWritesFenceOnTheClaim(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		for name, unfinished := range map[string]bool{
			"ExtendItems":    true,
			"CountHeldItems": true,
			"CompleteItems":  false,
			"ReleaseItems":   true,
		} {
			body := splitStatement(T, d, name)

			test.StrContains(T, body, HolderColumn+" = sqlc.arg("+HolderArg+")", test.Sprintf("%s %s", d, name))
			test.EqOp(T, unfinished, strings.Contains(body, outstanding()), test.Sprintf("%s %s", d, name))
		}

		// The two operator writes hold no claim, and must not ask for one.
		for _, name := range []string{"RemoveItems", "RequeueItems"} {
			test.StrNotContains(T, splitStatement(T, d, name), HolderArg, test.Sprintf("%s %s", d, name))
		}
	}
}

// TestRenderSplit_TheOutcomeWritesReleaseTheClaimWithTheLease. A retired or
// handed-back item that still answered to the claim would answer to it again
// after a restart.
func TestRenderSplit_TheOutcomeWritesReleaseTheClaimWithTheLease(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		for _, name := range []string{"CompleteItems", "ReleaseItems"} {
			test.StrContains(T, splitStatement(T, d, name), "\t"+HolderColumn+" = NULL", test.Sprintf("%s %s", d, name))
		}
	}
}

// TestRenderSplit_EveryKeySetBindsLast. On both engines a set expands to a
// placeholder per element, and an argument bound after the expansion is
// numbered into the middle of it on SQLite.
func TestRenderSplit_EveryKeySetBindsLast(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		for name, body := range splitCorpus(T, d) {
			_, after, ok := strings.Cut(body, "sqlc.slice("+KeysArg+")")
			if !ok {
				continue
			}

			rest := after
			test.StrNotContains(T, rest, "sqlc.", test.Sprintf("%s %s binds after its set", d, name))
			test.StrNotContains(T, rest, "?", test.Sprintf("%s %s binds after its set", d, name))
		}
	}
}

// TestRenderSplit_TheKeyedWritesLockInKeyOrderOnMySQL. MySQL acquires an
// UPDATE's or a DELETE's row locks in the order the statement names, and two
// overlapping writers in one order queue rather than deadlock. SQLite has one
// writer and would not parse the clause.
func TestRenderSplit_TheKeyedWritesLockInKeyOrderOnMySQL(T *testing.T) {
	T.Parallel()

	for _, name := range []string{
		"ExtendItems", "CompleteItems", "ReleaseItems", "RemoveItems", "RequeueItems", "DeleteReapedItems",
	} {
		test.True(T, strings.HasSuffix(splitStatement(T, dialect.MySQL, name), "\nORDER BY work_queue_items.item_key;"),
			test.Sprintf("mysql %s", name))
		test.StrNotContains(T, splitStatement(T, dialect.SQLite, name), "ORDER BY", test.Sprintf("sqlite %s", name))
	}
}

// TestRenderSplit_TheEnqueueRestartIsAssignedLast. MySQL evaluates an ON
// DUPLICATE KEY UPDATE left to right and lets a later assignment see an
// earlier one; every branch of the merge tests completed_at, so clearing it
// anywhere but last would turn a restart into a merge for every column after it.
func TestRenderSplit_TheEnqueueRestartIsAssignedLast(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		enqueue := splitStatement(T, d, "EnqueueItem")

		test.True(T, strings.HasSuffix(enqueue, "\t"+CompletedAtColumn+" = NULL;"), test.Sprintf("dialect %s", d))
		test.StrNotContains(T, enqueue, LeaseColumn+" =", test.Sprintf("dialect %s", d))
		test.StrNotContains(T, enqueue, HolderColumn+" =", test.Sprintf("dialect %s", d))
	}

	test.StrContains(T, splitStatement(T, dialect.MySQL, "EnqueueItem"), "ON DUPLICATE KEY UPDATE")
	test.StrContains(T, splitStatement(T, dialect.SQLite, "EnqueueItem"),
		"ON CONFLICT ("+QueueColumn+", "+KeyColumn+") DO UPDATE SET")
}

// TestRenderSplit_ExtendOnlyMovesTheHorizonForward, and counts rather than
// trusts its own row count — MySQL reports rows changed, and an extension that
// lands under a longer lease changes nothing.
func TestRenderSplit_ExtendOnlyMovesTheHorizonForward(T *testing.T) {
	T.Parallel()

	test.StrContains(T, splitStatement(T, dialect.MySQL, "ExtendItems"),
		LeaseColumn+" = GREATEST(work_queue_items."+LeaseColumn+", ")
	test.StrContains(T, splitStatement(T, dialect.SQLite, "ExtendItems"),
		LeaseColumn+" = max(work_queue_items."+LeaseColumn+", ")
}

// TestRenderSplit_TheClockIsTheServersAtItsFinestGrain. No instant is bound,
// on either engine; and the clock read is the one that carries sub-second
// precision — MySQL's bare CURRENT_TIMESTAMP and SQLite's are both
// second-granular, and a lease or a delay rounded to a second is wrong in a
// direction this package cannot choose.
func TestRenderSplit_TheClockIsTheServersAtItsFinestGrain(T *testing.T) {
	T.Parallel()

	bareMySQLNow := regexp.MustCompile(`CURRENT_TIMESTAMP($|[^(])`)

	for _, d := range splitDialects {
		for name, body := range splitCorpus(T, d) {
			for _, column := range []string{EnqueuedAtColumn, AvailableAtColumn, LeaseColumn, CompletedAtColumn} {
				test.StrNotContains(T, body, "sqlc.arg("+column+")", test.Sprintf("%s %s binds %s", d, name, column))
			}

			switch d {
			case dialect.MySQL:
				test.False(T, bareMySQLNow.MatchString(body), test.Sprintf("mysql %s reads a second-granular clock", name))
			case dialect.SQLite:
				test.StrNotContains(T, body, "CURRENT_TIMESTAMP", test.Sprintf("sqlite %s reads a second-granular clock", name))
			}
		}
	}
}

// TestRenderSplit_SQLiteNeverShortensALease is the rounding direction, read
// off the text: SQLite's clock is millisecond text, so a microsecond count is
// rounded up to whole milliseconds, and a lease is padded by one more because
// the now it is added to was truncated. A delay is rounded up and not padded —
// early is harmless there, and a pad would make a zero-delay enqueue miss the
// claim right after it. The behavior is pinned against a real database in
// workqueue's SQLite suite.
func TestRenderSplit_SQLiteNeverShortensALease(T *testing.T) {
	T.Parallel()

	lease := "((sqlc.arg(" + LeaseArg + ") + 999) / 1000 + 1) / 1000.0"
	delay := "((sqlc.arg(" + DelayArg + ") + 999) / 1000) / 1000.0"

	test.StrContains(T, splitStatement(T, dialect.SQLite, "LeaseItems"), lease)
	test.StrContains(T, splitStatement(T, dialect.SQLite, "ExtendItems"), lease)
	test.StrContains(T, splitStatement(T, dialect.SQLite, "EnqueueItem"), delay)
	test.StrContains(T, splitStatement(T, dialect.SQLite, "ReleaseItems"), delay)

	// And the retention window is subtracted rounded up, so nothing is reaped
	// before its window has run.
	test.StrContains(T, splitStatement(T, dialect.SQLite, "SelectReapableItems"),
		"printf('-%.3f seconds', ((sqlc.arg("+RetentionArg+") + 999) / 1000) / 1000.0)")
}

// TestRenderSplit_TheReapRepeatsItsTest, for the lease's reason.
func TestRenderSplit_TheReapRepeatsItsTest(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		s := newSplit(d)

		test.StrContains(T, splitStatement(T, d, "SelectReapableItems"), s.reapable(), test.Sprintf("dialect %s", d))
		test.StrContains(T, splitStatement(T, d, "DeleteReapedItems"), s.reapable(), test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_NoStatementNamesAnUnprefixableTable, for the Postgres test's
// reason.
func TestRenderSplit_NoStatementNamesAnUnprefixableTable(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		for name, body := range splitCorpus(T, d) {
			test.StrContains(T, body, ItemsTable, test.Sprintf("%s %s", d, name))
		}
	}
}
