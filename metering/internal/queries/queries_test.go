package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/metering/migrations"

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

// TestRender_RegistersTheTables is the registry half of the same guarantee the
// canonical .sql files are the query half of: a consumer reading the registry
// back to truncate a database between integration tests gets every table this
// component has rows in.
func TestRender_RegistersTheTables(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
//
// Fourteen, from the twelve builders this package replaced. Two of the twelve
// became one — the seed the fold opens with is the seed Consume opens with —
// and the upsert whose conflict branch was chosen per aggregation became three
// named folds.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		InsertEventQuery,
		EventExistsQuery,
		PruneEventsQuery,
		InsertTotalQuery,
		GetTotalQuery,
		GetTotalForUpdateQuery,
		FoldSumTotalQuery,
		FoldMaxTotalQuery,
		FoldLastTotalQuery,
		ApplyConsumeQuery,
		SelectFlushableQuery,
		ClaimTotalQuery,
		MarkFlushedQuery,
		ReleaseFlushQuery,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.Eq(t, want, statementNames(Render(d)))
		})
	}
}

// TestColumns_AreTheColumnsTheDDLDeclares is the cross-check between the two
// halves of "what does this table hold": the lists here, which every statement
// is rendered from, and the DDL a consumer actually creates the tables with.
//
// Neither derives from the other on purpose, so a column added to one and not
// the other stops being invisible. It matters most for the four columns
// [TotalProjection] leaves out, each of which is a decision about a column that
// has to exist.
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

			for _, column := range slices.Concat(EventColumns, TotalColumns) {
				test.StrContains(t, ddl, column, test.Sprintf("%s is not in the %s DDL", column, d))
			}
		})
	}
}

// TestTotalProjection_IsTheRowMinusTheFourNothingReads pins the gap between the
// whole row and the list every statement over the totals table is rendered
// from, since that gap is what leaves those statements with no archived
// predicate and no stamp of their own.
func TestTotalProjection_IsTheRowMinusTheFourNothingReads(t *testing.T) {
	t.Parallel()

	for _, column := range TotalProjection {
		test.True(t, slices.Contains(TotalColumns, column),
			test.Sprintf("%s is projected but is not a column", column))
	}

	for _, column := range []string{
		querygen.ArchivedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.CreatedAtColumn,
		ClaimedUntilColumn,
	} {
		test.False(t, slices.Contains(TotalProjection, column),
			test.Sprintf("%s is projected and should not be", column))
	}
}

// TestRender_KeysEveryTotalStatementOnTheNaturalKey is the property this schema
// cannot be wrong about.
//
// (subject, meter, period_start) is the row a period's usage accumulates in. A
// statement addressing a total by the subject alone would fold two meters
// together; by the subject and meter alone it would fold every period a
// customer has ever had into one, which is an invoice for the lifetime of the
// account.
func TestRender_KeysEveryTotalStatementOnTheNaturalKey(T *testing.T) {
	T.Parallel()

	// The seed names the key as its conflict target on the one dialect that
	// spells a target at all; the other nine address a row with it.
	const addressing = 9

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range []string{
				GetTotalQuery, GetTotalForUpdateQuery,
				FoldSumTotalQuery, FoldMaxTotalQuery, FoldLastTotalQuery,
				ApplyConsumeQuery, ClaimTotalQuery, MarkFlushedQuery, ReleaseFlushQuery,
			} {
				keyed := statement(t, Render(d), name)

				for _, column := range []string{SubjectColumn, MeterColumn, PeriodStartColumn} {
					test.StrContains(t, keyed, column+" = sqlc.arg("+column+")",
						test.Sprintf("%s does not key on %s", name, column))
				}
			}

			// The count is the whole corpus's, so a tenth statement growing a
			// key predicate that names one of these columns and not the others
			// is a difference rather than a silence.
			rendered := Render(d)

			test.EqOp(t, addressing, strings.Count(rendered, PeriodStartColumn+" = sqlc.arg("+PeriodStartColumn+")"))
		})
	}
}

// TestRender_DedupesOnTheMeterAndTheKey pins the ledger's own key, which is the
// pair rather than the idempotency key alone.
//
// Callers are told to use a request ID, and one request routinely feeds several
// meters — an API call billing both a request count and a byte count. Keyed on
// the key alone the second meter's insert is silently deduped against the
// first, and that customer is under-billed for it forever.
func TestRender_DedupesOnTheMeterAndTheKey(T *testing.T) {
	T.Parallel()

	T.Run("postgres names the pair as the conflict target", func(t *testing.T) {
		t.Parallel()

		insert := statement(t, Render(dialect.Postgres), InsertEventQuery)

		test.StrContains(t, insert, "ON CONFLICT (meter, idempotency_key) DO NOTHING")
		test.StrNotContains(t, insert, "INSERT IGNORE")
	})

	T.Run("mysql and sqlite take a modifier and name no target", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, statement(t, Render(dialect.MySQL), InsertEventQuery), "INSERT IGNORE INTO")
		test.StrContains(t, statement(t, Render(dialect.SQLite), InsertEventQuery), "INSERT OR IGNORE INTO")
	})

	T.Run("the probe reads the same pair", func(t *testing.T) {
		t.Parallel()

		// The probe is what a consume about to be refused asks before refusing,
		// and an answer keyed differently from the insert's would be an answer
		// about a different row.
		for _, d := range everyDialect {
			probe := statement(t, Render(d), EventExistsQuery)

			test.StrContains(t, probe, MeterColumn+" = sqlc.arg("+MeterColumn+")", test.Sprintf("dialect %q", d))
			test.StrContains(t, probe, IdempotencyKeyColumn+" = sqlc.arg("+IdempotencyKeyColumn+")",
				test.Sprintf("dialect %q", d))
		}
	})
}

// TestRender_FoldsInTheStatement pins the property that makes concurrent ingest
// safe: the arithmetic is the server's.
//
// Two recorders folding into the same period at the same instant would
// otherwise both read the total, both add their own quantity to it, and between
// them lose one — silently, and in the direction that under-bills. A fold that
// had become an assignment would pass every single-threaded test.
func TestRender_FoldsInTheStatement(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.StrContains(t, statement(t, rendered, FoldSumTotalQuery),
				"quantity = quantity + sqlc.arg(quantity)")

			test.StrContains(t, statement(t, rendered, FoldMaxTotalQuery),
				"quantity = CASE WHEN quantity < sqlc.arg(quantity) THEN sqlc.arg(quantity) ELSE quantity END")

			// Guarded on the event time rather than applied unconditionally, so
			// a record arriving late leaves the newer reading standing.
			test.StrContains(t, statement(t, rendered, FoldLastTotalQuery),
				"quantity = CASE WHEN last_occurred_at < sqlc.arg(last_occurred_at) "+
					"THEN sqlc.arg(quantity) ELSE quantity END")
		})
	}
}

// TestRender_GuardsTheLastReadingStrictly pins the comparison the last fold's
// quantity is guarded on, and pins it to the one the column itself moves on.
//
// The two are one condition because the row has to be one record's reading
// under that record's time. An admitted tie is not the corner it reads as:
// SQLite stores a bound time truncated to the whole second, so every record
// inside one second compares equal to the column there, and a redelivery
// stamped an hour late but inside a second already recorded would take the
// quantity while the column kept the newer instant.
func TestRender_GuardsTheLastReadingStrictly(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			fold := statement(t, Render(d), FoldLastTotalQuery)

			test.StrContains(t, fold, "CASE WHEN last_occurred_at < sqlc.arg(last_occurred_at) "+
				"THEN sqlc.arg(quantity)")

			// The equal case belongs to the row, on every dialect and in every
			// spelling of it.
			test.StrNotContains(t, fold, ">=")
			test.StrNotContains(t, fold, "<=")
		})
	}
}

// TestRender_MovesTheEventTimeForwardOnly pins the column a last-aggregation
// meter orders by.
//
// A record arriving late — a queue redelivering an hour behind — must not drag
// the row's notion of "latest" backwards and let the next out-of-order record
// win. Every fold therefore moves the column forward and never back.
//
// The exception is the write Consume makes, which runs against a row it holds
// the lock on: the comparison was already made in Go against the committed
// value, so a second one in the statement would be a second opinion.
func TestRender_MovesTheEventTimeForwardOnly(T *testing.T) {
	T.Parallel()

	const forward = "last_occurred_at = CASE WHEN last_occurred_at < sqlc.arg(last_occurred_at) " +
		"THEN sqlc.arg(last_occurred_at) ELSE last_occurred_at END"

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, name := range []string{FoldSumTotalQuery, FoldMaxTotalQuery, FoldLastTotalQuery} {
				test.StrContains(t, statement(t, rendered, name), forward, test.Sprintf("%s does not", name))
			}

			apply := statement(t, rendered, ApplyConsumeQuery)

			test.StrContains(t, apply, "last_occurred_at = sqlc.arg(last_occurred_at)")
			test.StrNotContains(t, apply, "CASE WHEN")
		})
	}
}

// TestRender_NamesNoDialect is the property the CASE above bought.
//
// The scalar maximum is GREATEST on two of these servers and MAX on the third,
// and it is the one expression this corpus would have had to spell twice —
// querygen renders no statement containing one, so there is no home above for
// the fact. Written as a CASE there is no fact: the three files say the same
// thing, and MySQL's analyzer resolves a type for an argument compared against
// a column where it resolves none for one buried in a function call.
func TestRender_NamesNoDialect(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.StrNotContains(t, rendered, "GREATEST(")
			test.StrNotContains(t, rendered, "MAX(")
		})
	}
}

// TestRender_LocksWhereTheDialectLocks pins the two clauses that decide what
// two concurrent callers mean.
//
// Consume's read holds one row for the rest of its transaction, so the second
// consumer reads the total the first committed rather than deciding against a
// stale one. The claim's read skips what another flusher holds, so a fleet
// takes disjoint batches. SQLite has neither and needs neither: one writer at a
// time is its storage model.
func TestRender_LocksWhereTheDialectLocks(T *testing.T) {
	T.Parallel()

	T.Run("the locked read is the unlocked one with a suffix", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			rendered := Render(d)

			unlocked := strings.TrimSuffix(strings.TrimSpace(statement(t, rendered, GetTotalQuery)), ";")
			locked := statement(t, rendered, GetTotalForUpdateQuery)

			// Both begin with their annotation's remainder, so the comparison
			// is of the statements rather than of the names above them.
			_, unlockedSQL, _ := strings.Cut(unlocked, "\n")
			_, lockedSQL, _ := strings.Cut(locked, "\n")

			test.StrHasPrefix(t, strings.TrimSpace(unlockedSQL), strings.TrimSpace(lockedSQL),
				test.Sprintf("dialect %q", d))
		}
	})

	T.Run("postgres and mysql take the lock", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL} {
			rendered := Render(d)

			test.StrContains(t, statement(t, rendered, GetTotalForUpdateQuery), "FOR UPDATE",
				test.Sprintf("dialect %q", d))
			test.StrContains(t, statement(t, rendered, SelectFlushableQuery), "FOR UPDATE SKIP LOCKED",
				test.Sprintf("dialect %q", d))
		}
	})

	T.Run("sqlite takes neither", func(t *testing.T) {
		t.Parallel()

		test.StrNotContains(t, Render(dialect.SQLite), "FOR UPDATE")
	})
}

// TestRender_GuardsTheLeaseAndTheSettles pins the three predicates a flush pass
// puts its correctness on.
//
// The lease guards on the total still owing the provider something, so one
// settled between the read and the lease drops out of the batch rather than
// being posted twice. Both settles guard on the sequence the flusher read,
// which is what stops a flusher whose lease lapsed mid-post from advancing a
// sequence a second flusher has already moved — the one failure this package
// cannot repair, since two posts of the same delta under two keys are two
// charges.
func TestRender_GuardsTheLeaseAndTheSettles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.StrContains(t, statement(t, rendered, ClaimTotalQuery), "AND quantity > flushed_quantity")

			for _, name := range []string{MarkFlushedQuery, ReleaseFlushQuery} {
				test.StrContains(t, statement(t, rendered, name), "flush_sequence = sqlc.arg(flush_sequence)",
					test.Sprintf("%s does not guard on the sequence", name))
			}

			// The attempt count is spent at the lease rather than at the
			// failure, so a total whose post reliably kills its flusher
			// eventually gives up instead of being reclaimed forever.
			test.StrContains(t, statement(t, rendered, ClaimTotalQuery), "flush_attempts = flush_attempts + 1")
		})
	}
}

// TestRender_LeavesTheFlushedQuantityAloneOnFailure pins the absence that keeps
// a retried post carrying the same delta under the same sequence.
//
// The post may have reached the provider and failed on the way back. Advancing
// what the provider has been told about, on the path where nobody knows whether
// it was told, is how usage goes uninvoiced.
func TestRender_LeavesTheFlushedQuantityAloneOnFailure(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			release := statement(t, Render(d), ReleaseFlushQuery)

			test.StrNotContains(t, release, FlushedQuantityColumn)
			test.StrNotContains(t, release, FlushSequenceColumn+" = "+FlushSequenceColumn)
		})
	}
}

// TestRender_KeepsRetentionOffUnflushedPeriods pins the guard that keeps a
// retention pass from destroying evidence somebody is still going to need.
//
// An event row whose period still owes the provider usage is the only record of
// what that unposted total is made of — and deleting it re-opens the
// idempotency key it held, so a redelivery of that same event would be counted
// a second time into a total nobody has invoiced yet.
func TestRender_KeepsRetentionOffUnflushedPeriods(T *testing.T) {
	T.Parallel()

	T.Run("the guard names the doomed row under the arm's own qualifier", func(t *testing.T) {
		t.Parallel()

		// Two arms, two names. A guard qualified with the wrong one would
		// resolve against the totals table its own subquery names, which is a
		// predicate that runs and dooms the wrong rows.
		test.StrContains(t, statement(t, Render(dialect.Postgres), PruneEventsQuery),
			"NOT EXISTS (SELECT 1 FROM metering_totals t WHERE t.subject = doomed.subject")

		test.StrContains(t, statement(t, Render(dialect.MySQL), PruneEventsQuery),
			"NOT EXISTS (SELECT 1 FROM metering_totals t WHERE t.subject = metering_events.subject")
	})

	T.Run("the guard compares the two quantities", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			test.StrContains(t, statement(t, Render(d), PruneEventsQuery),
				"t.quantity > t.flushed_quantity", test.Sprintf("dialect %q", d))
		}
	})

	T.Run("the pass stays bounded and ordered", func(t *testing.T) {
		t.Parallel()

		// The cap is the loop's condition rather than a courtesy, and the order
		// is what makes a backlog drain oldest first.
		for _, d := range everyDialect {
			prune := statement(t, Render(d), PruneEventsQuery)

			test.StrContains(t, prune, "LIMIT", test.Sprintf("dialect %q", d))
			test.StrContains(t, prune, "ORDER BY", test.Sprintf("dialect %q", d))
			test.StrContains(t, prune, RecordedAtColumn+" <= sqlc.arg("+HorizonArg+")", test.Sprintf("dialect %q", d))
		}
	})
}

// TestRender_ReadsAndWritesArchivedRowsAlike pins the absence of the predicate
// every conventional single-row statement carries.
//
// archived_at is in the schema for the convention's sake and nothing writes it.
// A total is a billing record: hiding one would hide an invoice line, and a
// read that excluded archived rows would report a customer's usage as zero.
func TestRender_ReadsAndWritesArchivedRowsAlike(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.StrNotContains(t, Render(d), querygen.ArchivedAtColumn)
		})
	}
}

// TestRender_StampsFromTheCallersClock pins that no statement here reads the
// server's clock.
//
// A total's timeline is one clock's, and it is the clock the flusher schedules
// against: next_flush, last_updated_at and recorded_at are compared against
// each other and against the times a caller supplies, so a column stamped by
// the server would put half the timeline on a machine nobody is looking at.
func TestRender_StampsFromTheCallersClock(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			test.StrNotContains(t, rendered, "CURRENT_TIMESTAMP")
			test.StrNotContains(t, rendered, "NOW()")
			test.StrContains(t, rendered, querygen.LastUpdatedAtColumn+" = sqlc.narg("+querygen.LastUpdatedAtColumn+")")
		})
	}
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
// assertion about the claim is not accidentally satisfied by the settle.
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
