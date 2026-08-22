package retention

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/jobs"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSweeperConfig(T *testing.T) {
	T.Parallel()

	T.Run("EnsureDefaults fills every unset knob", func(t *testing.T) {
		t.Parallel()

		cfg := &SweeperConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultBatchSize, cfg.BatchSize)
		test.EqOp(t, DefaultMaxBatches, cfg.MaxBatches)
		test.EqOp(t, DefaultBacklogCeiling, cfg.BacklogCeiling)

		// Zero survives: no pause is a legitimate setting for a sweep against a
		// database nothing else is using, and defaulting it would make that
		// unexpressible.
		test.EqOp(t, time.Duration(0), cfg.BatchPause)
	})

	T.Run("EnsureDefaults keeps what was set", func(t *testing.T) {
		t.Parallel()

		cfg := &SweeperConfig{BatchSize: 7, MaxBatches: 9, BacklogCeiling: 11, BatchPause: time.Second}
		cfg.EnsureDefaults()

		test.EqOp(t, 7, cfg.BatchSize)
		test.EqOp(t, 9, cfg.MaxBatches)
		test.EqOp(t, 11, cfg.BacklogCeiling)
		test.EqOp(t, time.Second, cfg.BatchPause)
	})

	T.Run("a negative pause is replaced rather than accepted", func(t *testing.T) {
		t.Parallel()

		cfg := &SweeperConfig{BatchPause: -time.Second}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultBatchPause, cfg.BatchPause)
	})

	T.Run("validates the filled config", func(t *testing.T) {
		t.Parallel()

		cfg := &SweeperConfig{}
		cfg.EnsureDefaults()
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an unfilled config", func(t *testing.T) {
		t.Parallel()

		// Defaults are applied before validation everywhere in this module, so
		// this is only reachable by validating by hand — which is exactly when
		// a zero batch size should be refused rather than read as "no bound".
		test.Error(t, (&SweeperConfig{}).ValidateWithContext(t.Context()))
	})
}

func TestNewSweeper(T *testing.T) {
	T.Parallel()

	policies := []Policy{{Name: "widgets", Target: Table{Name: widgetsTable, Column: "created_at"}, Age: time.Hour}}

	T.Run("builds from a zero config", func(t *testing.T) {
		t.Parallel()

		sweeper, err := NewSweeper(t.Context(), &SweeperConfig{}, newTestClient(t), policies)
		must.NoError(t, err)
		test.EqOp(t, DefaultBatchSize, sweeper.cfg.BatchSize)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewSweeper(t.Context(), nil, newTestClient(t), policies)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSweeper(t.Context(), &SweeperConfig{}, nil, policies)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("rejects an empty policy set", func(t *testing.T) {
		t.Parallel()

		// A sweeper with nothing to sweep is indistinguishable at runtime from
		// one whose policies all work.
		_, err := NewSweeper(t.Context(), &SweeperConfig{}, newTestClient(t), nil)
		test.ErrorIs(t, err, ErrNoPolicies)
	})

	T.Run("one bad policy fails the whole construction", func(t *testing.T) {
		t.Parallel()

		_, err := NewSweeper(t.Context(), &SweeperConfig{}, newTestClient(t), []Policy{
			policies[0],
			{Name: "typo", Target: Table{Name: "not an identifier", Column: "created_at"}},
		})
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})

	T.Run("rejects two policies sharing a name", func(t *testing.T) {
		t.Parallel()

		_, err := NewSweeper(t.Context(), &SweeperConfig{}, newTestClient(t), []Policy{policies[0], policies[0]})
		test.ErrorIs(t, err, ErrDuplicatePolicy)
	})

	T.Run("copies the caller's slice", func(t *testing.T) {
		t.Parallel()

		given := []Policy{{Name: "widgets", Target: Table{Name: widgetsTable, Column: "created_at"}}}

		sweeper, err := NewSweeper(t.Context(), &SweeperConfig{}, newTestClient(t), given)
		must.NoError(t, err)

		given[0].Disabled = true

		test.False(t, sweeper.Policies()[0].Disabled)
	})

	T.Run("defaults are applied before the config is validated", func(t *testing.T) {
		t.Parallel()

		// A knob left unset has a documented default, so it is not a validation
		// failure — validating first would turn the common case into one.
		sweeper, err := NewSweeper(t.Context(), &SweeperConfig{BatchPause: time.Second}, newTestClient(t), policies)
		must.NoError(t, err)
		test.EqOp(t, DefaultBacklogCeiling, sweeper.cfg.BacklogCeiling)
	})
}

func TestSweeper_Sweep(T *testing.T) {
	T.Parallel()

	T.Run("deletes what has aged past the policy and leaves the rest", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 5)
		insertWidgets(t, client, "fresh", baseTime.Add(-time.Hour), 3)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:   "widgets",
			Target: Table{Name: widgetsTable, Column: "created_at"},
			Age:    24 * time.Hour,
		}})

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(5), result.Removed)
		must.SliceLen(t, 1, result.Policies)
		test.EqOp(t, "widgets", result.Policies[0].Name)
		test.EqOp(t, widgetsTable, result.Policies[0].Target)
		test.True(t, result.Policies[0].Drained)
		test.EqOp(t, int64(0), result.Policies[0].Backlog)
		test.EqOp(t, int64(3), countWidgets(t, client))
	})

	T.Run("the cutoff moves with the clock", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "widget", baseTime.Add(-2*time.Hour), 4)

		sweeper, stub := newTestSweeper(t, client, []Policy{{
			Name:   "widgets",
			Target: Table{Name: widgetsTable, Column: "created_at"},
			Age:    24 * time.Hour,
		}})

		// Nothing is old enough yet, which is the assertion: expiry is a
		// function of the injected clock and not of the wall.
		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), result.Removed)
		test.EqOp(t, int64(4), countWidgets(t, client))

		stub.advance(48 * time.Hour)

		result, err = sweeper.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(4), result.Removed)
		test.EqOp(t, int64(0), countWidgets(t, client))
	})

	T.Run("drains a backlog in bounded batches, pausing between them", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 7)

		sweeper, stub := newTestSweeper(t, client, []Policy{{
			Name:      "widgets",
			Target:    Table{Name: widgetsTable, Column: "created_at"},
			Age:       24 * time.Hour,
			BatchSize: 3,
		}}, func(s *Sweeper) { s.cfg.BatchPause = 250 * time.Millisecond })

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(7), result.Removed)
		// Three batches: 3, 3, then 1 — the short one is how the sweeper learns
		// the target has drained without paying a query to be told so.
		test.EqOp(t, 3, result.Policies[0].Batches)
		test.True(t, result.Policies[0].Drained)

		// Paused between batches and not after the last: the pause protects the
		// database from the next batch, and there isn't one.
		test.Eq(t, []time.Duration{250 * time.Millisecond, 250 * time.Millisecond}, stub.pauses())
	})

	T.Run("stops at the batch cap and reports the backlog it left", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 10)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:       "widgets",
			Target:     Table{Name: widgetsTable, Column: "created_at"},
			Age:        24 * time.Hour,
			BatchSize:  2,
			MaxBatches: 3,
		}})

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.EqOp(t, int64(6), result.Removed)
		test.EqOp(t, 3, result.Policies[0].Batches)
		test.False(t, result.Policies[0].Drained)
		test.EqOp(t, int64(4), result.Policies[0].Backlog)
		test.EqOp(t, int64(4), countWidgets(t, client))
	})

	T.Run("a disabled policy is not run and not reported", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 4)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:     "widgets",
			Target:   Table{Name: widgetsTable, Column: "created_at"},
			Age:      24 * time.Hour,
			Disabled: true,
		}})

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		test.SliceEmpty(t, result.Policies)
		test.EqOp(t, int64(4), countWidgets(t, client))
	})

	T.Run("a failing policy does not stop the others", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 2)

		sweeper, _ := newTestSweeper(t, client, []Policy{
			{Name: "missing-table", Target: Table{Name: "absent", Column: "created_at"}},
			{Name: "widgets", Target: Table{Name: widgetsTable, Column: "created_at"}, Age: 24 * time.Hour},
		})

		result, err := sweeper.Sweep(t.Context())
		test.Error(t, err)

		// The second policy still ran: an unreachable table is not a reason to
		// skip an unrelated one behind it.
		must.SliceLen(t, 2, result.Policies)
		test.EqOp(t, int64(2), result.Removed)
		test.EqOp(t, int64(0), countWidgets(t, client))
	})

	T.Run("reports rows removed before a mid-sweep failure", func(t *testing.T) {
		t.Parallel()

		target := &stubTarget{name: "flaky"}
		target.sweepFunc = func(limit int) (int64, error) {
			if target.sweepCalls == 1 {
				return int64(limit), nil
			}

			return 0, platformerrors.New("the table went away")
		}

		sweeper, _ := newTestSweeper(t, newTestClient(t), []Policy{
			{Name: "flaky", Target: target, BatchSize: 4},
		})

		result, err := sweeper.Sweep(t.Context())
		test.Error(t, err)

		// The four rows the first batch deleted are gone whether or not the
		// caller is told about them.
		must.SliceLen(t, 1, result.Policies)
		test.EqOp(t, int64(4), result.Policies[0].Removed)
		test.EqOp(t, 1, result.Policies[0].Batches)
		test.False(t, result.Policies[0].Drained)
	})

	T.Run("samples the backlog even when the sweep failed", func(t *testing.T) {
		t.Parallel()

		target := &stubTarget{
			name:        "flaky",
			sweepFunc:   func(int) (int64, error) { return 0, platformerrors.New("locked") },
			backlogFunc: func(int) (int64, error) { return 900, nil },
		}

		sweeper, _ := newTestSweeper(t, newTestClient(t), []Policy{{Name: "flaky", Target: target}})

		result, err := sweeper.Sweep(t.Context())
		test.Error(t, err)

		// A policy that just failed is precisely the one whose backlog somebody
		// needs to see.
		test.EqOp(t, 1, target.backlogCalls)
		test.EqOp(t, int64(900), result.Policies[0].Backlog)
	})

	T.Run("a failing backlog reading does not fail the sweep", func(t *testing.T) {
		t.Parallel()

		target := &stubTarget{
			name:        "widgets",
			sweepFunc:   func(int) (int64, error) { return 2, nil },
			backlogFunc: func(int) (int64, error) { return 0, platformerrors.New("statement timeout") },
		}

		sweeper, _ := newTestSweeper(t, newTestClient(t), []Policy{{Name: "widgets", Target: target, BatchSize: 5}})

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		// The reading is about the sweep, not part of it. Failing a sweep that
		// deleted rows because the count after it timed out reports the wrong
		// thing.
		test.EqOp(t, int64(2), result.Removed)
		test.EqOp(t, int64(0), result.Policies[0].Backlog)
	})

	T.Run("the backlog ceiling reaches the target", func(t *testing.T) {
		t.Parallel()

		var seen int

		target := &stubTarget{
			name:        "widgets",
			backlogFunc: func(ceiling int) (int64, error) { seen = ceiling; return 0, nil },
		}

		sweeper, err := NewSweeper(t.Context(), &SweeperConfig{BacklogCeiling: 42}, newTestClient(t),
			[]Policy{{Name: "widgets", Target: target}}, WithSweeperClock(newStubClock()))
		must.NoError(t, err)

		_, err = sweeper.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, 42, seen)
	})

	T.Run("policies run in registration order", func(t *testing.T) {
		t.Parallel()

		var order []string

		newRecordingTarget := func(name string) *stubTarget {
			return &stubTarget{
				name:      name,
				sweepFunc: func(int) (int64, error) { order = append(order, name); return 0, nil },
			}
		}

		sweeper, _ := newTestSweeper(t, newTestClient(t), []Policy{
			{Name: "children", Target: newRecordingTarget("children")},
			{Name: "parents", Target: newRecordingTarget("parents")},
		})

		_, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		// The only ordering control a caller has, and the reason a child table
		// can be swept before the parent whose key it references.
		test.Eq(t, []string{"children", "parents"}, order)
	})

	T.Run("a context that expires during the pause stops the policy", func(t *testing.T) {
		t.Parallel()

		target := &stubTarget{name: "widgets", sweepFunc: func(limit int) (int64, error) { return int64(limit), nil }}

		sweeper, stub := newTestSweeper(t, newTestClient(t),
			[]Policy{{Name: "widgets", Target: target, BatchSize: 1, MaxBatches: 50}})
		stub.pauseExpires = true

		_, err := sweeper.Sweep(t.Context())
		test.ErrorIs(t, err, context.DeadlineExceeded)

		// Without the check, a policy that never drains would run its full
		// batch cap after the scheduler's job timeout asked it to stop.
		test.EqOp(t, 1, target.sweepCalls)
	})

	T.Run("a cancelled context stops the sweep before it deletes anything", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 4)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:   "widgets",
			Target: Table{Name: widgetsTable, Column: "created_at"},
			Age:    24 * time.Hour,
		}})

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := sweeper.Sweep(ctx)
		test.ErrorIs(t, err, context.Canceled)
		test.EqOp(t, int64(4), countWidgets(t, client))
	})
}

func TestSweeper_audit(T *testing.T) {
	T.Parallel()

	newRecorder := func(t *testing.T) audit.Recorder {
		t.Helper()

		recorder, err := audit.NewRecorder(dialect.SQLite)
		must.NoError(t, err)

		return recorder
	}

	// readEntries returns the audit entries recorded against the retention
	// resource type.
	readEntries := func(t *testing.T, client database.Client) []*audit.Entry {
		t.Helper()

		reader, err := audit.NewReader(client)
		must.NoError(t, err)

		result, err := reader.List(t.Context(),
			&audit.Query{ResourceTypes: []string{AuditResourceType}},
			filtering.DefaultQueryFilter(),
		)
		must.NoError(t, err)

		return result.Data
	}

	T.Run("records what one policy removed", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 5)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:   "widgets",
			Target: Table{Name: widgetsTable, Column: "created_at"},
			Age:    24 * time.Hour,
			Basis:  "widgets are useless after a day",
			Scope:  "platform",
		}}, WithSweeperAuditRecorder(newRecorder(t)))

		_, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		entries := readEntries(t, client)
		must.SliceLen(t, 1, entries)

		entry := entries[0]
		test.EqOp(t, audit.EventDeleted, entry.EventType)
		test.EqOp(t, AuditResourceType, entry.ResourceType)
		test.EqOp(t, "widgets", entry.ResourceID)
		test.EqOp(t, "platform", entry.Scope)
		test.EqOp(t, DefaultAuditActorID, entry.Actor.ID)
		test.EqOp(t, audit.ActorSystem, entry.Actor.Type)
		test.EqOp(t, "5", entry.Metadata["rows_removed"])
		test.EqOp(t, widgetsTable, entry.Metadata["target"])
		test.EqOp(t, "24h0m0s", entry.Metadata["age"])
		test.EqOp(t, "true", entry.Metadata["drained"])

		// The basis is the difference between evidence of a deletion and
		// evidence of a policy.
		test.EqOp(t, "widgets are useless after a day", entry.Metadata["basis"])
	})

	T.Run("records nothing for a policy that removed nothing", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:   "widgets",
			Target: Table{Name: widgetsTable, Column: "created_at"},
			Age:    24 * time.Hour,
		}}, WithSweeperAuditRecorder(newRecorder(t)))

		_, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		// A nightly entry per policy saying zero would, within a year, be most
		// of what the audit log contains.
		test.SliceEmpty(t, readEntries(t, client))
	})

	T.Run("records the deletion even when a later batch failed", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		target := &stubTarget{name: "widgets"}
		target.sweepFunc = func(limit int) (int64, error) {
			if target.sweepCalls == 1 {
				return int64(limit), nil
			}

			return 0, platformerrors.New("locked")
		}

		sweeper, _ := newTestSweeper(t, client, []Policy{{Name: "widgets", Target: target, BatchSize: 3}},
			WithSweeperAuditRecorder(newRecorder(t)))

		_, err := sweeper.Sweep(t.Context())
		test.Error(t, err)

		entries := readEntries(t, client)
		must.SliceLen(t, 1, entries)
		test.EqOp(t, "3", entries[0].Metadata["rows_removed"])
		test.EqOp(t, "false", entries[0].Metadata["drained"])
	})

	T.Run("the actor can be named", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 1)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:   "widgets",
			Target: Table{Name: widgetsTable, Column: "created_at"},
			Age:    24 * time.Hour,
		}},
			WithSweeperAuditRecorder(newRecorder(t)),
			WithSweeperActor(audit.Actor{ID: "billing-worker", Type: audit.ActorService}),
		)

		_, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)

		entries := readEntries(t, client)
		must.SliceLen(t, 1, entries)
		test.EqOp(t, "billing-worker", entries[0].Actor.ID)
		test.EqOp(t, audit.ActorService, entries[0].Actor.Type)
	})

	T.Run("sweeps without a recorder", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 2)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:   "widgets",
			Target: Table{Name: widgetsTable, Column: "created_at"},
			Age:    24 * time.Hour,
		}})

		result, err := sweeper.Sweep(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(2), result.Removed)
	})
}

func TestSweeper_Job(T *testing.T) {
	T.Parallel()

	T.Run("renders a scheduled job that runs the sweep", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		insertWidgets(t, client, "stale", baseTime.Add(-48*time.Hour), 3)

		sweeper, _ := newTestSweeper(t, client, []Policy{{
			Name:   "widgets",
			Target: Table{Name: widgetsTable, Column: "created_at"},
			Age:    24 * time.Hour,
		}})

		job := sweeper.Job(jobs.MustCron("0 4 * * *"), 30*time.Minute)

		test.EqOp(t, DefaultSweepJobName, job.Name)
		test.EqOp(t, 30*time.Minute, job.LeaseTTL)
		must.NotNil(t, job.Schedule)

		must.NoError(t, job.Run(t.Context()))
		test.EqOp(t, int64(0), countWidgets(t, client))
	})

	T.Run("the job reports the sweep's failure", func(t *testing.T) {
		t.Parallel()

		sweeper, _ := newTestSweeper(t, newTestClient(t), []Policy{
			{Name: "missing-table", Target: Table{Name: "absent", Column: "created_at"}},
		})

		test.Error(t, sweeper.Job(jobs.MustCron("0 4 * * *"), time.Minute).Run(t.Context()))
	})
}

func TestSweeper_Policies(T *testing.T) {
	T.Parallel()

	T.Run("returns a copy the caller cannot use to disable a policy", func(t *testing.T) {
		t.Parallel()

		sweeper, _ := newTestSweeper(t, newTestClient(t), []Policy{
			{Name: "widgets", Target: Table{Name: widgetsTable, Column: "created_at"}},
		})

		reported := sweeper.Policies()
		must.SliceLen(t, 1, reported)
		reported[0].Disabled = true

		test.False(t, sweeper.Policies()[0].Disabled)
	})
}
