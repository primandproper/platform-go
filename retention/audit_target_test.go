package retention

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The audit log is the second implementation of Target, and the one that
// justified making Target an interface: its sweep is not a bare DELETE. These
// tests drive it through the real Sweeper, because the questions worth asking
// about it are the Sweeper's — does the budget bound it, does the backlog read,
// does the run get accounted for — and none of them can be asked of the target
// alone.
//
// The narrower behavior of the target itself lives in package audit.

// auditEntry builds a minimally valid audit entry for a scope.
func auditEntry(scope, resourceID string) *audit.Entry {
	return &audit.Entry{
		EventType:    audit.EventUpdated,
		ResourceType: "recipe",
		ResourceID:   resourceID,
		Scope:        scope,
		Actor:        audit.Actor{ID: "user_1", Type: audit.ActorUser},
	}
}

// recordAuditEntries writes entries through a Recorder inside a transaction,
// the way a caller would.
func recordAuditEntries(t *testing.T, client database.Client, recorder audit.Recorder, entries ...*audit.Entry) {
	t.Helper()

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
		return recorder.Record(t.Context(), q, entries...)
	}))
}

// countAuditEntries reports how many entries survive, across every scope.
func countAuditEntries(t *testing.T, client database.Client) int64 {
	t.Helper()

	var n int64
	must.NoError(t, client.Reader().
		QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log_entries").Scan(&n))

	return n
}

func TestSweeper_auditLogTarget(T *testing.T) {
	T.Parallel()

	// newAuditSweeper builds a Sweeper over one audit-log policy, with a
	// Recorder sharing its clock so the entries can be aged on command.
	newAuditSweeper := func(t *testing.T, mutate func(*Policy)) (*Sweeper, *stubClock, audit.Recorder, database.Client) {
		t.Helper()

		client := newTestClient(t)
		c := newStubClock()

		// One clock for the Recorder, the Sweeper's cutoff, and the target's
		// watermark, so that advancing it ages the log the way years would.
		recorder := mustAuditRecorder(t, c)

		policy := Policy{
			Name:   audit.DefaultRetentionPolicyName,
			Target: audit.PruneTarget{Clock: c},
			Age:    24 * time.Hour,
			Basis:  audit.DefaultRetentionBasis,
		}
		if mutate != nil {
			mutate(&policy)
		}

		sweeper, err := NewSweeper(t.Context(), &SweeperConfig{}, client, []Policy{policy},
			WithSweeperClock(c), WithSweeperAuditRecorder(recorder))
		must.NoError(t, err)

		return sweeper, c, recorder, client
	}

	T.Run("prunes the log and leaves the survivors verifiable", func(t *testing.T) {
		t.Parallel()

		sweeper, c, recorder, client := newAuditSweeper(t, nil)

		recordAuditEntries(t, client, recorder,
			auditEntry("acct_1", "r1"), auditEntry("acct_1", "r2"), auditEntry("acct_2", "r1"))

		c.advance(48 * time.Hour)
		survivor := auditEntry("acct_1", "r3")
		recordAuditEntries(t, client, recorder, survivor)

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		must.SliceLen(t, 1, result.Policies)
		test.EqOp(t, int64(3), result.Policies[0].Removed)
		test.True(t, result.Policies[0].Drained)
		test.EqOp(t, "audit_log_entries", result.Policies[0].Target)

		// The chain is what a bare DELETE would have broken. The oldest
		// survivor is anchored against the watermark the prune left behind, so
		// retention's gap still reads as retention rather than as tampering.
		reader, err := audit.NewReader(client)
		must.NoError(t, err)

		verification, err := reader.Verify(t.Context(), "acct_1", time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, verification.Intact())
		test.EqOp(t, 1, verification.Checked)
	})

	T.Run("accounts for the pruning in the log it pruned", func(t *testing.T) {
		t.Parallel()

		sweeper, c, recorder, client := newAuditSweeper(t, nil)

		recordAuditEntries(t, client, recorder, auditEntry("acct_1", "r1"), auditEntry("acct_1", "r2"))
		c.advance(48 * time.Hour)

		_, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		reader, err := audit.NewReader(client)
		must.NoError(t, err)

		entries, err := reader.List(t.Context(),
			&audit.Query{ResourceTypes: []string{AuditResourceType}},
			filtering.DefaultQueryFilter(),
		)
		must.NoError(t, err)
		must.SliceLen(t, 1, entries.Data)

		// Until this change the one deletion this module performed against the
		// audit log was the one deletion nothing recorded. Now it is an entry
		// in the log, carrying the window it was deleted under and why.
		entry := entries.Data[0]
		test.EqOp(t, audit.DefaultRetentionPolicyName, entry.ResourceID)
		test.EqOp(t, "audit_log_entries", entry.Metadata["target"])
		test.EqOp(t, "2", entry.Metadata["rows_removed"])
		test.EqOp(t, audit.DefaultRetentionBasis, entry.Metadata["basis"])

		// Two entries pruned, one accounting entry written: the log is not
		// empty afterwards, it holds the record of having been emptied.
		test.EqOp(t, int64(1), countAuditEntries(t, client))
	})

	T.Run("stops at the batch cap and reports the backlog it left", func(t *testing.T) {
		t.Parallel()

		sweeper, c, recorder, client := newAuditSweeper(t, func(p *Policy) {
			p.BatchSize = 2
			p.MaxBatches = 1
		})

		recordAuditEntries(t, client, recorder,
			auditEntry("acct_1", "r1"), auditEntry("acct_1", "r2"), auditEntry("acct_1", "r3"))
		c.advance(48 * time.Hour)

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		must.SliceLen(t, 1, result.Policies)
		test.EqOp(t, int64(2), result.Policies[0].Removed)
		test.False(t, result.Policies[0].Drained)

		// The backlog is the reading the package's own sweeper never had: it is
		// what separates "pruned nothing because the log is clean" from "pruned
		// nothing because it is stuck".
		test.EqOp(t, int64(1), result.Policies[0].Backlog)
	})

	T.Run("refuses a policy whose table prefix would not render", func(t *testing.T) {
		t.Parallel()

		// Validated at construction, through the same Target.Validate every
		// other policy goes through — so a typo is a process that does not
		// start rather than a nightly failure into a log nobody reads.
		_, err := NewSweeper(t.Context(), &SweeperConfig{}, newTestClient(t), []Policy{{
			Name:   audit.DefaultRetentionPolicyName,
			Target: audit.PruneTarget{TablePrefix: "audit-"},
			Age:    audit.DefaultRetention,
		}})
		test.ErrorIs(t, err, audit.ErrInvalidTablePrefix)
	})
}

// mustAuditRecorder builds a Recorder over the supplied clock.
func mustAuditRecorder(t *testing.T, c *stubClock) audit.Recorder {
	t.Helper()

	recorder, err := audit.NewRecorder(dialect.SQLite, audit.WithRecorderClock(c))
	must.NoError(t, err)

	return recorder
}
