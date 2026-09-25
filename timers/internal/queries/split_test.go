package queries

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The split corpus, read the way the Postgres one is read above: for the parts
// that are silently wrong rather than loudly wrong. Whether MySQL and SQLite
// accept a statement is sqlc's to say, and whether it behaves is timers'
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

// TestRenderSplit_EmitsTheStatementsTheSetExecutes pins the set, for the
// Postgres test's reason: a statement emitted and not executed is SQL nobody
// checks the other way round.
func TestRenderSplit_EmitsTheStatementsTheSetExecutes(T *testing.T) {
	T.Parallel()

	expected := []string{
		"ScheduleTimer",
		"SelectDueTimers",
		"LockDueTimers",
		"LeaseTimers",
		"FetchLeasedTimers",
		"ReadNextDueTimer",
		"CompleteTimers",
		"ReleaseTimers",
		"CancelTimers",
		"SelectReapableTimers",
		"DeleteReapedTimers",
		"ReadTimerStats",
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

// TestRenderSplit_TheOnlyLockingReadLocksByKey. SKIP LOCKED is what keeps two
// MySQL claimants from waiting on each other, and it is on the read that names
// the whole primary key and nothing else: a locking read over an index range
// also locks the first record past it, which is another set's earliest due
// timer as often as not, and SKIP LOCKED would hide that timer from its own
// set's claimants. So the two candidate reads lock nothing, and page with an
// offset where the claim needs to read on. SQLite has no row lock and must not
// be handed a clause it cannot parse.
func TestRenderSplit_TheOnlyLockingReadLocksByKey(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		for name, body := range splitCorpus(T, d) {
			if name == "LockDueTimers" {
				continue
			}

			test.StrNotContains(T, body, "FOR UPDATE", test.Sprintf("%s %s", d, name))
		}
	}

	lock := splitStatement(T, dialect.MySQL, "LockDueTimers")
	test.True(T, strings.HasSuffix(lock, "sqlc.slice("+KeysArg+"))\nFOR UPDATE SKIP LOCKED;"), test.Sprintf("mysql:\n%s", lock))
	test.StrNotContains(T, lock, "ORDER BY")
	test.StrNotContains(T, lock, "LIMIT")
	test.StrNotContains(T, splitStatement(T, dialect.SQLite, "LockDueTimers"), "FOR UPDATE")

	test.True(T, strings.HasSuffix(splitStatement(T, dialect.MySQL, "SelectDueTimers"), "LIMIT ?, ?;"))
	test.True(T, strings.HasSuffix(splitStatement(T, dialect.SQLite, "SelectDueTimers"),
		"LIMIT sqlc.arg("+LimitArg+") OFFSET sqlc.arg("+OffsetArg+");"))
}

// TestRenderSplit_TheOldestDebtFiresFirst. No priority, as in the Postgres
// claim: a timer already said what it wanted by naming an instant.
func TestRenderSplit_TheOldestDebtFiresFirst(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		test.StrContains(T, splitStatement(T, d, "SelectDueTimers"),
			"ORDER BY scheduled_timers."+RunAtColumn+", scheduled_timers."+KeyColumn+"\n",
			test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_TheLeaseRepeatsTheClaimsTest. A write has to hold every
// row-state test its select held, or it leases whatever the rows became in
// between — the outbox double-publish, in a different table. The locking read
// repeats it too, because its candidates came from a snapshot. The health read
// renders the same predicate, so its due count is what a claim hands out.
func TestRenderSplit_TheLeaseRepeatsTheClaimsTest(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		s := newSplit(d)

		test.StrContains(T, splitStatement(T, d, "SelectDueTimers"), s.due(), test.Sprintf("dialect %s", d))
		test.StrContains(T, splitStatement(T, d, "LockDueTimers"), s.due(), test.Sprintf("dialect %s", d))
		test.StrContains(T, splitStatement(T, d, "LeaseTimers"), s.due(), test.Sprintf("dialect %s", d))
		test.StrContains(T, splitStatement(T, d, "ReadTimerStats"), s.due(), test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_TheReadBackFindsTheClaimByItsName. The name is the fact: a
// key the read selected and the lease did not take is not this claim's.
func TestRenderSplit_TheReadBackFindsTheClaimByItsName(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		fetch := splitStatement(T, d, "FetchLeasedTimers")

		test.StrContains(T, fetch, HolderColumn+" = sqlc.arg("+HolderArg+")", test.Sprintf("dialect %s", d))
		test.StrNotContains(T, fetch, KeysArg, test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_TheOutcomeWritesFenceOnTheClaim is the #727 fence on these
// two engines: every write reporting on a firing names the claim, and the one
// that must not touch a fired timer excludes it.
func TestRenderSplit_TheOutcomeWritesFenceOnTheClaim(T *testing.T) {
	T.Parallel()

	unfired := "scheduled_timers." + FiredAtColumn + " IS NULL"

	for _, d := range splitDialects {
		for name, excludesFired := range map[string]bool{
			"CompleteTimers": false,
			"ReleaseTimers":  true,
		} {
			body := splitStatement(T, d, name)

			test.StrContains(T, body, HolderColumn+" = sqlc.arg("+HolderArg+")", test.Sprintf("%s %s", d, name))
			test.EqOp(T, excludesFired, strings.Contains(body, unfired), test.Sprintf("%s %s", d, name))
		}

		// Cancel holds no claim, and must not ask for one.
		test.StrNotContains(T, splitStatement(T, d, "CancelTimers"), HolderArg, test.Sprintf("%s cancel", d))
	}
}

// TestRenderSplit_EveryMoveOfTheInstantTakesTheName is what lets the outcome
// writes fence on the name alone, where the Postgres triples bind the instant
// beside it. The claim is the one statement that stamps a name, and it leaves
// run_at alone; every statement that writes run_at clears the name or, for a
// reschedule, revokes it under the very test that says the instant moved.
// Break any of the three and a stale claim's Complete lands on a schedule it
// was never handed.
func TestRenderSplit_EveryMoveOfTheInstantTakesTheName(T *testing.T) {
	T.Parallel()

	writesRunAt := regexp.MustCompile(`(?m)^\t` + RunAtColumn + ` = `)

	for _, d := range splitDialects {
		for name, body := range splitCorpus(T, d) {
			if !writesRunAt.MatchString(body) {
				continue
			}

			switch name {
			case "ReleaseTimers":
				test.StrContains(T, body, "\t"+HolderColumn+" = NULL,", test.Sprintf("%s %s", d, name))
			case "ScheduleTimer":
				moved := "scheduled_timers." + RunAtColumn + " <> "
				test.StrContains(T, body, HolderColumn+" = CASE\n\t\tWHEN "+moved, test.Sprintf("%s %s", d, name))
				test.StrContains(T, body, LeaseColumn+" = CASE\n\t\tWHEN "+moved, test.Sprintf("%s %s", d, name))
			default:
				T.Errorf("%s %s writes run_at and is not known to take the name with it", d, name)
			}
		}

		test.False(T, writesRunAt.MatchString(splitStatement(T, d, "LeaseTimers")), test.Sprintf("%s lease", d))
		test.StrContains(T, splitStatement(T, d, "LeaseTimers"), HolderColumn+" = sqlc.arg("+HolderArg+")",
			test.Sprintf("%s lease", d))
	}
}

// TestRenderSplit_TheRescheduleComparesTheInstantItFound. MySQL evaluates an
// ON DUPLICATE KEY UPDATE left to right and lets a later assignment see an
// earlier one: were run_at assigned before the two revocations, they would
// compare the new instant with itself and never revoke anything.
func TestRenderSplit_TheRescheduleComparesTheInstantItFound(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		schedule := splitStatement(T, d, "ScheduleTimer")

		assigned := strings.Index(schedule, "\t"+RunAtColumn+" = ")
		must.Positive(T, assigned, must.Sprintf("dialect %s", d))

		for _, revoked := range []string{LeaseColumn + " = CASE", HolderColumn + " = CASE"} {
			at := strings.Index(schedule, revoked)
			must.Positive(T, at, must.Sprintf("%s %s", d, revoked))
			test.Less(T, assigned, at, test.Sprintf("%s assigns run_at before %s", d, revoked))
		}
	}

	test.StrContains(T, splitStatement(T, dialect.MySQL, "ScheduleTimer"), "ON DUPLICATE KEY UPDATE")
	test.StrContains(T, splitStatement(T, dialect.SQLite, "ScheduleTimer"),
		"ON CONFLICT ("+SetColumn+", "+KeyColumn+") DO UPDATE SET")
}

// TestRenderSplit_TheOutcomeWritesReleaseTheClaimWithTheLease. A retired or
// handed-back firing that still answered to the claim would answer to it again
// after a reschedule restarted the row.
func TestRenderSplit_TheOutcomeWritesReleaseTheClaimWithTheLease(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		for _, name := range []string{"CompleteTimers", "ReleaseTimers"} {
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

			test.StrNotContains(T, after, "sqlc.", test.Sprintf("%s %s binds after its set", d, name))
			test.StrNotContains(T, after, "?", test.Sprintf("%s %s binds after its set", d, name))
		}
	}
}

// TestRenderSplit_TheKeyedWritesLockInKeyOrderOnMySQL. MySQL acquires an
// UPDATE's or a DELETE's row locks in the order the statement names, and two
// overlapping writers in one order queue rather than deadlock. SQLite has one
// writer and would not parse the clause.
func TestRenderSplit_TheKeyedWritesLockInKeyOrderOnMySQL(T *testing.T) {
	T.Parallel()

	for _, name := range []string{"LeaseTimers", "CompleteTimers", "ReleaseTimers", "CancelTimers", "DeleteReapedTimers"} {
		test.True(T, strings.HasSuffix(splitStatement(T, dialect.MySQL, name), "\nORDER BY scheduled_timers.timer_key;"),
			test.Sprintf("mysql %s", name))
		test.StrNotContains(T, splitStatement(T, dialect.SQLite, name), "ORDER BY", test.Sprintf("sqlite %s", name))
	}
}

// TestRenderSplit_TheClockIsTheServersAtItsFinestGrain. run_at is the one
// instant a caller names, and it crosses as a microsecond count rather than a
// bound time; nothing else is bound at all. The clock read is the one that
// carries sub-second precision — MySQL's bare CURRENT_TIMESTAMP and SQLite's
// are both second-granular, and a lease rounded to a second is wrong in a
// direction this package cannot choose.
func TestRenderSplit_TheClockIsTheServersAtItsFinestGrain(T *testing.T) {
	T.Parallel()

	bareMySQLNow := regexp.MustCompile(`CURRENT_TIMESTAMP($|[^(])`)

	for _, d := range splitDialects {
		for name, body := range splitCorpus(T, d) {
			for _, column := range []string{RunAtColumn, LeaseColumn, FiredAtColumn, "last_updated_at", "created_at"} {
				test.StrNotContains(T, body, "sqlc.arg("+column+")", test.Sprintf("%s %s binds %s", d, name, column))
			}

			switch d {
			case dialect.MySQL:
				test.False(T, bareMySQLNow.MatchString(body), test.Sprintf("mysql %s reads a second-granular clock", name))
				test.StrNotContains(T, body, "FROM_UNIXTIME", test.Sprintf("mysql %s reads the session's time zone", name))
			case dialect.SQLite:
				test.StrNotContains(T, body, "CURRENT_TIMESTAMP", test.Sprintf("sqlite %s reads a second-granular clock", name))
			}
		}
	}
}

// TestRenderSplit_TheMySQLClockIsUTC. run_at is the UTC wall clock whatever the
// session's time zone, so every clock a MySQL statement reads has to be the UTC
// one: a statement that compared run_at with CURRENT_TIMESTAMP would fire every
// timer early or late by the session's offset. CURRENT_TIMESTAMP appears only
// inside now, for the fraction of a second a zone cannot change. created_at is
// the one column whose DEFAULT reads the session's clock, so the insert writes
// it rather than leaving it to the DEFAULT.
func TestRenderSplit_TheMySQLClockIsUTC(T *testing.T) {
	T.Parallel()

	now := newSplit(dialect.MySQL).now()
	test.StrContains(T, now, "UTC_TIMESTAMP()")

	for name, body := range splitCorpus(T, dialect.MySQL) {
		rest := strings.ReplaceAll(body, now, "")

		for _, clock := range []string{"CURRENT_TIMESTAMP", "UTC_TIMESTAMP", "NOW(", "SYSDATE", "LOCALTIMESTAMP"} {
			test.StrNotContains(T, rest, clock, test.Sprintf("mysql %s reads %s outside now", name, clock))
		}
	}

	schedule := splitStatement(T, dialect.MySQL, "ScheduleTimer")
	inserted, _, _ := strings.Cut(schedule, "ON DUPLICATE KEY UPDATE")
	test.StrContains(T, inserted, "\t"+querygen.CreatedAtColumn+"\n)")
	test.StrContains(T, inserted, "\t"+now+"\n)")
}

// TestRenderSplit_TheKeyedStatementsForceThePrimaryKeyOnMySQL. A keyed
// statement names the whole primary key, and the claim's, the hand-back's and
// the reaper's also carry a range over the due index; were MySQL to take that
// range instead, it would lock the first record past it, which is as often as
// not another set's earliest due timer. So every statement that binds the keys
// is forced onto the primary key — FORCE INDEX where MySQL takes it, the
// optimizer hint on a DELETE, which does not — and the candidate reads, which
// lock nothing and are what the due index is for, are left alone.
func TestRenderSplit_TheKeyedStatementsForceThePrimaryKeyOnMySQL(T *testing.T) {
	T.Parallel()

	keyed := 0

	for name, body := range splitCorpus(T, dialect.MySQL) {
		if !strings.Contains(body, "sqlc.slice("+KeysArg+")") {
			test.StrNotContains(T, body, "INDEX", test.Sprintf("mysql %s", name))

			continue
		}

		keyed++

		switch {
		case strings.HasPrefix(body, "DELETE "):
			test.True(T, strings.HasPrefix(body, "DELETE /*+ INDEX("+TimersTable+" PRIMARY) */ FROM "+TimersTable+"\n"),
				test.Sprintf("mysql %s:\n%s", name, body))
		case strings.HasPrefix(body, "UPDATE "):
			test.True(T, strings.HasPrefix(body, "UPDATE "+TimersTable+" FORCE INDEX (PRIMARY) SET\n"),
				test.Sprintf("mysql %s:\n%s", name, body))
		default:
			test.StrContains(T, body, "\nFROM "+TimersTable+" FORCE INDEX (PRIMARY)\n", test.Sprintf("mysql %s", name))
		}
	}

	// The lock, the lease, the two outcome writes, the cancel and the reap.
	test.EqOp(T, 6, keyed)

	for name, body := range splitCorpus(T, dialect.SQLite) {
		test.StrNotContains(T, body, "INDEX", test.Sprintf("sqlite %s", name))
	}
}

// TestRenderSplit_SQLiteNeverFiresATimerEarly is the rounding direction, read
// off the text. SQLite stores milliseconds, so a scheduled instant is rounded
// up to the next whole one, and a lease is rounded up and then padded by one
// more because the now it is added to was truncated. A release's delay is
// rounded up and not padded — early is harmless there, and a pad would make a
// zero-delay hand-back miss the claim right after it. The behavior is pinned
// against a real database in timers' container suites.
func TestRenderSplit_SQLiteNeverFiresATimerEarly(T *testing.T) {
	T.Parallel()

	test.StrContains(T, splitStatement(T, dialect.SQLite, "ScheduleTimer"),
		"((sqlc.arg("+RunAtArg+") + 999) / 1000) / 1000.0, 'unixepoch')")
	test.StrContains(T, splitStatement(T, dialect.SQLite, "LeaseTimers"),
		"((sqlc.arg("+LeaseArg+") + 999) / 1000 + 1) / 1000.0")
	test.StrContains(T, splitStatement(T, dialect.SQLite, "ReleaseTimers"),
		"((sqlc.arg("+DelayArg+") + 999) / 1000) / 1000.0")

	// And the retention window is subtracted rounded up, so nothing is reaped
	// before its window has run.
	test.StrContains(T, splitStatement(T, dialect.SQLite, "SelectReapableTimers"),
		"printf('-%.3f seconds', ((sqlc.arg("+RetentionArg+") + 999) / 1000) / 1000.0)")

	// MySQL keeps the microsecond the set already rounded up to.
	test.StrContains(T, splitStatement(T, dialect.MySQL, "ScheduleTimer"),
		"INTERVAL sqlc.arg("+RunAtArg+") MICROSECOND")
}

// TestRenderSplit_TheReapRepeatsItsTest, for the lease's reason.
func TestRenderSplit_TheReapRepeatsItsTest(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		s := newSplit(d)

		test.StrContains(T, splitStatement(T, d, "SelectReapableTimers"), s.reapable(), test.Sprintf("dialect %s", d))
		test.StrContains(T, splitStatement(T, d, "DeleteReapedTimers"), s.reapable(), test.Sprintf("dialect %s", d))
	}
}

// TestRenderSplit_NoStatementNamesAnUnprefixableTable, for the Postgres test's
// reason.
func TestRenderSplit_NoStatementNamesAnUnprefixableTable(T *testing.T) {
	T.Parallel()

	for _, d := range splitDialects {
		for name, body := range splitCorpus(T, d) {
			test.StrContains(T, body, TimersTable, test.Sprintf("%s %s", d, name))
		}
	}
}
