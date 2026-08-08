package audit

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v10/database"
	"github.com/primandproper/platform-go/v10/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// sweepBatch drives one batch of a PruneTarget the way a retention.Sweeper
// does: inside a transaction it owns, with a row budget, against a cutoff it
// computed from the policy's age.
func sweepBatch(t *testing.T, client database.Client, target PruneTarget, age time.Duration, limit int) (int64, error) {
	t.Helper()

	cutoff := target.now().Add(-age)

	var removed int64
	err := client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
		var sweepErr error
		removed, sweepErr = target.Sweep(t.Context(), q, dialect.SQLite, cutoff, limit)

		return sweepErr
	})

	return removed, err
}

// mustSweep is sweepBatch for the cases where the sweep is expected to work.
func mustSweep(t *testing.T, client database.Client, target PruneTarget, age time.Duration, limit int) int64 {
	t.Helper()

	removed, err := sweepBatch(t, client, target, age, limit)
	must.NoError(t, err)

	return removed
}

// backlogOf reads a target's backlog the way the Sweeper does, off the read
// handle rather than inside the batch's transaction.
func backlogOf(t *testing.T, client database.Client, target PruneTarget, age time.Duration, ceiling int) int64 {
	t.Helper()

	backlog, err := target.Backlog(t.Context(), client.Reader(), dialect.SQLite, target.now().Add(-age), ceiling)
	must.NoError(t, err)

	return backlog
}

func TestRetentionConfig(T *testing.T) {
	T.Parallel()

	T.Run("EnsureDefaults fills every knob", func(t *testing.T) {
		t.Parallel()

		cfg := &RetentionConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultRetentionBasis, cfg.Basis)
		test.EqOp(t, DefaultRetention, cfg.Retention)
		test.EqOp(t, DefaultRetentionBatchSize, cfg.BatchSize)
		test.EqOp(t, DefaultScopePageSize, cfg.ScopePageSize)
	})

	T.Run("accepts the defaults it just filled", func(t *testing.T) {
		t.Parallel()

		cfg := &RetentionConfig{}
		cfg.EnsureDefaults()

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a retention window shorter than an hour", func(t *testing.T) {
		t.Parallel()

		cfg := &RetentionConfig{}
		cfg.EnsureDefaults()
		cfg.Retention = time.Second

		// A misplaced unit on a compliance parameter should stop a process from
		// starting, not quietly empty the table. retention.Policy cannot refuse
		// it — a zero age is legal there — so it has to be refused here.
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("refuses bounds that would remove nothing", func(t *testing.T) {
		t.Parallel()

		for _, mutate := range []func(*RetentionConfig){
			func(cfg *RetentionConfig) { cfg.BatchSize = -1 },
			func(cfg *RetentionConfig) { cfg.ScopePageSize = -1 },
		} {
			cfg := &RetentionConfig{}
			cfg.EnsureDefaults()
			mutate(cfg)

			test.Error(t, cfg.ValidateWithContext(t.Context()))
		}
	})
}

func TestPruneTarget_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts every supported dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			must.NoError(t, PruneTarget{}.Validate(d), must.Sprintf("dialect %q", d))
		}
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, PruneTarget{}.Validate("cassandra"), dialect.ErrUnsupported)
	})

	T.Run("rejects a prefix that is not a bare identifier", func(t *testing.T) {
		t.Parallel()

		// The prefix is interpolated into query text rather than bound, so this
		// is the gate that keeps it safe — and it runs at Sweeper construction,
		// not at four in the morning.
		for _, prefix := range []string{"audit-", "a b", `"; DROP TABLE audit_log_entries; --`} {
			test.ErrorIs(t, PruneTarget{TablePrefix: prefix}.Validate(dialect.SQLite),
				ErrInvalidTablePrefix, test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("tolerates any scope page size", func(t *testing.T) {
		t.Parallel()

		// A non-positive page takes the default. It changes how many queries a
		// batch costs and never how much it removes, so there is nothing here
		// worth refusing a process over.
		must.NoError(t, PruneTarget{ScopePageSize: -1}.Validate(dialect.SQLite))
	})
}

func TestPruneTarget_Describe(T *testing.T) {
	T.Parallel()

	T.Run("names the table entries are removed from", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "audit_log_entries", PruneTarget{}.Describe())
		test.EqOp(t, "ddb_audit_log_entries", PruneTarget{TablePrefix: "ddb"}.Describe())
	})
}

func TestPruneTarget_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("prunes past the window and leaves the rest verifiable", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)
		reader := newTestReader(t, client)

		first := entryFor("acct_1", "r1")
		record(t, client, recorder, first)

		c.advance(2 * time.Hour)
		record(t, client, recorder, entryFor("acct_1", "r2"))

		c.advance(2 * time.Hour)
		record(t, client, recorder, entryFor("acct_1", "r3"))

		test.EqOp(t, int64(1), mustSweep(t, client, PruneTarget{Clock: c}, 3*time.Hour, 100))
		test.EqOp(t, 2, countRows(t, client, "audit_log_entries", "1=1"))

		// The watermark the sweep left behind is what the oldest survivor is
		// anchored against; without it this would read as a deleted entry.
		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
		test.EqOp(t, 2, result.Checked)

		_, err = reader.Get(t.Context(), first.ID)
		test.ErrorIs(t, err, ErrEntryNotFound)
	})

	T.Run("leaves entries inside the window alone", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder, entryFor("acct_1", "r1"))
		c.advance(time.Minute)

		test.EqOp(t, int64(0), mustSweep(t, client, PruneTarget{Clock: c}, time.Hour, 100))
		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "1=1"))
	})

	T.Run("does nothing on an empty log", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		test.EqOp(t, int64(0), mustSweep(t, client, PruneTarget{Clock: newStubClock()}, time.Hour, 100))
	})

	T.Run("stops at the row budget and reports it undrained", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)
		reader := newTestReader(t, client)

		for i := range 5 {
			record(t, client, recorder, entryFor("acct_1", string(rune('a'+i))))
		}

		c.advance(4 * time.Hour)

		target := PruneTarget{Clock: c}

		// Exactly the budget: that is what tells the Sweeper there is more to
		// do and to spend another batch on it.
		test.EqOp(t, int64(2), mustSweep(t, client, target, time.Hour, 2))
		test.EqOp(t, 3, countRows(t, client, "audit_log_entries", "1=1"))

		// Still contiguous, still anchored: a batched sweep is several prefix
		// prunes, never a hole.
		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())

		test.EqOp(t, int64(2), mustSweep(t, client, target, time.Hour, 2))
		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "1=1"))

		// Short of the budget on the last batch, which is how the Sweeper
		// learns the log has drained.
		test.EqOp(t, int64(1), mustSweep(t, client, target, time.Hour, 2))
		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))

		result, err = reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
	})

	T.Run("spends one budget across several scopes", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder,
			entryFor("acct_1", "r1"), entryFor("acct_1", "r2"),
			entryFor("acct_2", "r1"), entryFor("acct_2", "r2"))
		c.advance(4 * time.Hour)

		// Three of the four, so the budget runs out mid-way through the second
		// scope rather than at a scope boundary.
		test.EqOp(t, int64(3), mustSweep(t, client, PruneTarget{Clock: c}, time.Hour, 3))
		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "1=1"))
	})

	T.Run("pages past the scope page size within one batch", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder,
			entryFor("acct_1", "r1"), entryFor("acct_2", "r1"), entryFor("acct_3", "r1"))
		c.advance(4 * time.Hour)

		// A page of one, three scopes: the page is not a cap, so the batch
		// keeps reading pages until it runs out of scopes. If it were a cap,
		// this would report one and claim to have drained.
		test.EqOp(t, int64(3), mustSweep(t, client, PruneTarget{Clock: c, ScopePageSize: 1}, time.Hour, 100))
		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
	})

	T.Run("prunes the empty scope", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		// The empty scope is where platform-level events go — including the
		// retention sweep's own accounting entry. A cursor that could not
		// express "no cursor yet" would make it the one scope never pruned.
		record(t, client, recorder, entryFor("", "r1"), entryFor("acct_1", "r1"))
		c.advance(4 * time.Hour)

		test.EqOp(t, int64(2), mustSweep(t, client, PruneTarget{Clock: c}, time.Hour, 100))
		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
	})

	T.Run("removes an entry recorded exactly at the cutoff", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder, entryFor("acct_1", "r1"))
		c.advance(time.Hour)

		// At or before, not strictly before — the same reading the backlog
		// count uses, so a row cannot sit in the backlog that no sweep takes.
		test.EqOp(t, int64(1), mustSweep(t, client, PruneTarget{Clock: c}, time.Hour, 100))
	})

	T.Run("lets a chain continue after its entries are all pruned", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)
		reader := newTestReader(t, client)

		first := entryFor("acct_1", "r1")
		record(t, client, recorder, first)

		c.advance(4 * time.Hour)

		test.EqOp(t, int64(1), mustSweep(t, client, PruneTarget{Clock: c}, time.Hour, 100))

		// The chain row outlives the entries, so the next write continues the
		// chain rather than restarting at a position already used.
		next := entryFor("acct_1", "r2")
		record(t, client, recorder, next)

		test.EqOp(t, int64(1), next.Seq)
		test.EqOp(t, first.Hash, next.PrevHash)

		result, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact())
	})

	T.Run("will not prune below an entry that must survive", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder, entryFor("acct_1", "r1"), entryFor("acct_1", "r2"))
		c.advance(4 * time.Hour)

		// Backdating the *second* entry is the clock-skew case: position 1 is
		// old enough to prune and position 0 is not. Deleting by timestamp
		// alone would punch a hole in the middle of the chain, which is
		// indistinguishable from tampering, so this prunes nothing at all.
		exec(t, client,
			"UPDATE audit_log_entries SET recorded_at = ? WHERE seq = 0",
			c.Now().UTC())

		test.EqOp(t, int64(0), mustSweep(t, client, PruneTarget{Clock: c}, time.Hour, 100))
		test.EqOp(t, 2, countRows(t, client, "audit_log_entries", "1=1"))

		// And it is visible: the blocked entry is still counted as backlog,
		// which is the number that says a policy is stuck rather than clean.
		test.EqOp(t, int64(1), backlogOf(t, client, PruneTarget{Clock: c}, time.Hour, 100))
	})
}

func TestPruneTarget_Backlog(T *testing.T) {
	T.Parallel()

	T.Run("counts what is past the window across every scope", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder, entryFor("acct_1", "r1"), entryFor("acct_2", "r1"))
		c.advance(4 * time.Hour)
		record(t, client, recorder, entryFor("acct_1", "r2"))

		test.EqOp(t, int64(2), backlogOf(t, client, PruneTarget{Clock: c}, time.Hour, 100))
	})

	T.Run("saturates at the ceiling", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		for i := range 5 {
			record(t, client, recorder, entryFor("acct_1", string(rune('a'+i))))
		}
		c.advance(4 * time.Hour)

		// A gauge, not an inventory: the reading must not get most expensive
		// exactly when the problem is worst.
		test.EqOp(t, int64(3), backlogOf(t, client, PruneTarget{Clock: c}, time.Hour, 3))
	})

	T.Run("reports a table it cannot read", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		exec(t, client, "DROP TABLE audit_log_entries")

		_, err := PruneTarget{}.Backlog(t.Context(), client.Reader(), dialect.SQLite, time.Now(), 100)
		test.Error(t, err)
	})
}

func TestPruneTarget_PropagatesFailures(T *testing.T) {
	T.Parallel()

	T.Run("reports a scope listing that fails", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder, entryFor("acct_1", "r1"))
		c.advance(4 * time.Hour)

		exec(t, client, "DROP TABLE audit_log_entries")

		_, err := sweepBatch(t, client, PruneTarget{Clock: c}, time.Hour, 100)
		test.Error(t, err)
	})

	T.Run("rolls the whole batch back when a watermark cannot be written", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		c := newStubClock()
		recorder := newTestRecorder(t, c)

		record(t, client, recorder, entryFor("acct_1", "r1"), entryFor("acct_2", "r1"))
		c.advance(4 * time.Hour)

		// Scopes list fine and the deletes would succeed, but the watermark
		// cannot be written — a half-applied migration looks exactly like this.
		exec(t, client, "DROP TABLE audit_log_chains")

		removed, err := sweepBatch(t, client, PruneTarget{Clock: c}, time.Hour, 100)
		test.Error(t, err)
		test.EqOp(t, int64(0), removed)

		// The rollback is the part that matters: a deletion whose watermark did
		// not land would leave a gap Verify could not tell from tampering. It
		// now covers both scopes rather than one, because the transaction is
		// the batch's and not the scope's.
		test.EqOp(t, 2, countRows(t, client, "audit_log_entries", "1=1"))
	})
}
