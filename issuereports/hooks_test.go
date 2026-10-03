package issuereports

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHook is a hook refusing the write it was called from.
var errHook = platformerrors.New("the hook failed")

// hookCall is one hook invocation, as the recording hooks saw it.
type hookCall struct {
	before   *Report
	report   *Report
	name     string
	reporter string
	scope    tenancy.Scope
	deleted  int64
}

// recordingHooks remembers every call, and fails the one named by failOn.
//
// It implements Hooks outright rather than embedding NoopHooks, so that a method
// added to Hooks later fails this file's build until it is recorded here too,
// rather than arriving as a no-op the suite never asserts on.
type recordingHooks struct {
	failOn string
	calls  []hookCall
}

var _ Hooks = (*recordingHooks)(nil)

func (h *recordingHooks) record(call *hookCall) error {
	h.calls = append(h.calls, *call)

	if call.name == h.failOn {
		return errHook
	}

	return nil
}

func (h *recordingHooks) AfterCreateReport(_ context.Context, _ database.Tx, scope tenancy.Scope, report *Report) error {
	return h.record(&hookCall{name: "AfterCreateReport", scope: scope, report: report})
}

func (h *recordingHooks) AfterUpdateReport(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Report) error {
	return h.record(&hookCall{name: "AfterUpdateReport", scope: scope, report: after, before: before})
}

func (h *recordingHooks) AfterTransitionReport(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Report) error {
	return h.record(&hookCall{name: "AfterTransitionReport", scope: scope, report: after, before: before})
}

func (h *recordingHooks) AfterArchiveReport(_ context.Context, _ database.Tx, scope tenancy.Scope, report *Report) error {
	return h.record(&hookCall{name: "AfterArchiveReport", scope: scope, report: report})
}

func (h *recordingHooks) AfterDeleteReportsByReporter(
	_ context.Context,
	_ database.Tx,
	scope tenancy.Scope,
	reporter string,
	deleted int64,
) error {
	return h.record(&hookCall{name: "AfterDeleteReportsByReporter", scope: scope, reporter: reporter, deleted: deleted})
}

// last is the most recent call, failing the test when there was none.
func (h *recordingHooks) last(tb testing.TB) hookCall {
	tb.Helper()

	must.SliceNotEmpty(tb, h.calls)

	return h.calls[len(h.calls)-1]
}

// runHooksSuite is every assertion about the hooks a write runs: that each write
// calls its own, with the row it answers with, that a write which overwrites a
// row is handed the row it overwrote, and that a hook's refusal is the write's.
func runHooksSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("every write calls its hook with the row it answers with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		created, err := env.create(t, store, testScope, newReport(testReporter, "bug", "the button does nothing"))
		must.NoError(t, err)
		call := hooks.last(t)
		test.EqOp(t, "AfterCreateReport", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, created, call.report)
		test.EqOp(t, StatusOpen, call.report.Status)
		test.False(t, call.report.CreatedAt.IsZero())

		revision := *created
		revision.Details = "the button does nothing, twice"
		revised, err := env.update(t, store, testScope, &revision)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterUpdateReport", call.name)
		test.EqOp(t, revised, call.report)
		test.EqOp(t, "the button does nothing, twice", call.report.Details)
		must.NotNil(t, call.before)
		test.EqOp(t, created.ID, call.before.ID)
		test.EqOp(t, "the button does nothing", call.before.Details)

		resolved, err := env.transition(t, store, testScope, created.ID, StatusOpen, StatusResolved, "fixed")
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterTransitionReport", call.name)
		test.EqOp(t, resolved, call.report)
		test.EqOp(t, StatusResolved, call.report.Status)
		test.EqOp(t, "fixed", call.report.Resolution)
		test.NotNil(t, call.report.ClosedAt)
		must.NotNil(t, call.before)
		test.EqOp(t, StatusOpen, call.before.Status)
		test.Nil(t, call.before.ClosedAt)

		archived, err := env.archive(t, store, testScope, created.ID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveReport", call.name)
		test.EqOp(t, archived, call.report)
		test.NotNil(t, call.report.ArchivedAt)

		deleted, err := env.erase(t, store, testScope, testReporter)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterDeleteReportsByReporter", call.name)
		test.EqOp(t, testReporter, call.reporter)
		test.EqOp(t, deleted, call.deleted)
		test.EqOp(t, int64(1), call.deleted)

		test.SliceLen(t, 5, hooks.calls)
	})

	t.Run("a reopen's hook is handed the note and the stamp it cleared", func(t *testing.T) {
		t.Parallel()

		// The reason a transition gets a before row: the status it left is the one
		// the caller named, and the resolution it threw away is named nowhere.
		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		report := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.transition(t, store, testScope, report.ID, StatusOpen, StatusDeclined, "works as intended")
		must.NoError(t, err)

		_, err = env.transition(t, store, testScope, report.ID, StatusDeclined, StatusOpen, "")
		must.NoError(t, err)

		call := hooks.last(t)
		test.EqOp(t, "AfterTransitionReport", call.name)
		test.EqOp(t, StatusOpen, call.report.Status)
		test.EqOp(t, "", call.report.Resolution)
		test.Nil(t, call.report.ClosedAt)
		must.NotNil(t, call.before)
		test.EqOp(t, StatusDeclined, call.before.Status)
		test.EqOp(t, "works as intended", call.before.Resolution)
		test.NotNil(t, call.before.ClosedAt)
	})

	t.Run("an erasure's hook is called with the count, zero included", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		filed(t, env, store, newReport(testReporter, "bug", "details"))

		for _, want := range []int64{1, 0} {
			_, err := env.erase(t, store, testScope, testReporter)
			must.NoError(t, err)

			call := hooks.last(t)
			test.EqOp(t, "AfterDeleteReportsByReporter", call.name)
			test.EqOp(t, testReporter, call.reporter)
			test.EqOp(t, want, call.deleted)
		}
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		report := filed(t, env, store, newReport(testReporter, "bug", "details"))
		before := len(hooks.calls)

		// The guard refuses: the report is open, not acknowledged.
		_, err := env.transition(t, store, testScope, report.ID, StatusAcknowledged, StatusResolved, "fixed")
		must.ErrorIs(t, err, ErrStatusConflict)

		// The lifecycle refuses before the statement runs.
		_, err = env.transition(t, store, testScope, report.ID, StatusOpen, StatusOpen, "")
		must.ErrorIs(t, err, ErrInvalidStatusTransition)

		// Absent reports are refused, on every write that names one.
		missing := *report
		missing.ID = "no_such_report"
		must.ErrorIs(t, env.updateErr(t, store, testScope, &missing), ErrReportNotFound)

		_, err = env.transition(t, store, testScope, "no_such_report", StatusOpen, StatusResolved, "fixed")
		must.ErrorIs(t, err, ErrReportNotFound)

		must.ErrorIs(t, env.archiveErr(t, store, testScope, "no_such_report"), ErrReportNotFound)

		must.ErrorIs(t, env.createErr(t, store, testScope, newReport("", "bug", "details")), ErrEmptyReporter)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the write's, and the write rolls back with it", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterCreateReport"}
		store := env.newStore(t, WithHooks(hooks))

		r := newReport(testReporter, "bug", "details")
		r.ID = "refused_by_the_hook"

		created, err := env.create(t, store, testScope, r)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, created)

		_, err = store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.ErrorIs(t, err, ErrReportNotFound)
	})

	t.Run("an update's hook error is the update's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterUpdateReport"}
		store := env.newStore(t, WithHooks(hooks))

		report := filed(t, env, store, newReport(testReporter, "bug", "details"))

		revision := *report
		revision.Details = "rewritten"
		revised, err := env.update(t, store, testScope, &revision)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, revised)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, report.ID)
		must.NoError(t, err)
		test.EqOp(t, "details", read.Details)
	})

	t.Run("a transition's hook error is the transition's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterTransitionReport"}
		store := env.newStore(t, WithHooks(hooks))

		report := filed(t, env, store, newReport(testReporter, "bug", "details"))

		moved, err := env.transition(t, store, testScope, report.ID, StatusOpen, StatusResolved, "fixed")
		must.ErrorIs(t, err, errHook)
		test.Nil(t, moved)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, report.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusOpen, read.Status)
		test.EqOp(t, "", read.Resolution)
	})

	t.Run("an archive's hook error is the archive's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterArchiveReport"}
		store := env.newStore(t, WithHooks(hooks))

		report := filed(t, env, store, newReport(testReporter, "bug", "details"))

		archived, err := env.archive(t, store, testScope, report.ID)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, archived)

		_, err = store.GetReport(t.Context(), env.reader(), testScope, report.ID)
		must.NoError(t, err)
	})

	t.Run("an erasure's hook error is the erasure's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterDeleteReportsByReporter"}
		store := env.newStore(t, WithHooks(hooks))

		report := filed(t, env, store, newReport(testReporter, "bug", "details"))

		deleted, err := env.erase(t, store, testScope, testReporter)
		must.ErrorIs(t, err, errHook)
		test.EqOp(t, int64(0), deleted)

		_, err = store.GetReport(t.Context(), env.reader(), testScope, report.ID)
		must.NoError(t, err)
	})

	t.Run("nil hooks are no hooks", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t, WithHooks(nil))

		filed(t, env, store, newReport(testReporter, "bug", "details"))
	})
}
