package issuereports

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore_SQLite runs the behavioral suite against SQLite, which is the
// engine every developer has. TestSQLStore_RealServers runs the identical suite
// against Postgres and MySQL — see containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is everything this store promises, run against whichever
// database it is handed.
//
// It is one function rather than a file of top-level tests because the three
// engines have to be held to the same behavior: the placeholder rendering, the
// archived predicates and the :execrows count every guarded write reads as its
// answer are spelled three ways, and a suite that ran only against SQLite would
// prove the one spelling SQLite accepts.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("writes", func(t *testing.T) {
		t.Parallel()

		runWriteSuite(t, env)
	})

	t.Run("lifecycle", func(t *testing.T) {
		t.Parallel()

		runLifecycleSuite(t, env)
	})

	t.Run("reads", func(t *testing.T) {
		t.Parallel()

		runReadSuite(t, env)
	})

	t.Run("erasure", func(t *testing.T) {
		t.Parallel()

		runErasureSuite(t, env)
	})

	t.Run("transactions", func(t *testing.T) {
		t.Parallel()

		runTransactionSuite(t, env)
	})
}

func runWriteSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a created report comes back with what the database assigned", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		r := newReport(testReporter, "bug", "the button does nothing")

		created, err := env.create(t, store, testScope, r)
		must.NoError(t, err)
		must.NotNil(t, created)

		// The id is minted here and the creation time is the database's, read
		// back rather than left as a zero time a caller would serialize as a
		// date in the year one.
		test.NotEqOp(t, "", created.ID)
		test.False(t, created.CreatedAt.IsZero())
		test.EqOp(t, StatusOpen, created.Status)
		test.Nil(t, created.ClosedAt)
		test.EqOp(t, testScope, created.Scope)

		// And the value the caller handed over is untouched: everything the
		// write settled is on what it answered with, so there is one place to
		// read it from.
		test.EqOp(t, "", r.ID)
		test.True(t, r.CreatedAt.IsZero())
		test.EqOp(t, Status(""), r.Status)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, created.ID, read.ID)
		test.EqOp(t, "bug", read.Kind)
		test.EqOp(t, "the button does nothing", read.Details)
		test.EqOp(t, "recipes", read.SubjectType)
		test.EqOp(t, "recipe_1", read.SubjectID)
		test.EqOp(t, StatusOpen, read.Status)
		test.EqOp(t, "", read.Resolution)
	})

	t.Run("a caller-supplied id is kept", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		r := newReport(testReporter, "bug", "details")
		r.ID = "report_of_my_own"

		created, err := env.create(t, store, testScope, r)
		must.NoError(t, err)
		test.EqOp(t, "report_of_my_own", created.ID)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, "report_of_my_own")
		must.NoError(t, err)
		test.EqOp(t, "report_of_my_own", read.ID)
	})

	t.Run("a report is born open and cannot be created in any other status", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		r := newReport(testReporter, "bug", "details")
		r.Status = StatusResolved

		err := env.createErr(t, store, testScope, r)
		must.ErrorIs(t, err, ErrInvalidStatusTransition)
	})

	t.Run("a resolution supplied at creation is dropped", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		closed := baseTime
		r := newReport(testReporter, "bug", "details")
		r.Resolution = "already fixed"
		r.ClosedAt = &closed

		created, err := env.create(t, store, testScope, r)
		must.NoError(t, err)
		test.EqOp(t, "", created.Resolution)
		test.Nil(t, created.ClosedAt)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, "", read.Resolution)
		test.Nil(t, read.ClosedAt)
	})

	t.Run("a report with nothing in it is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for name, mutate := range map[string]func(*Report){
			"no reporter": func(r *Report) { r.Reporter = "" },
			"no kind":     func(r *Report) { r.Kind = "" },
			"no details":  func(r *Report) { r.Details = "" },
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				r := newReport(testReporter, "bug", "details")
				mutate(r)

				test.Error(t, env.createErr(t, store, testScope, r))
			})
		}
	})

	t.Run("a nil report and an unset scope are refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.ErrorIs(t, env.createErr(t, store, testScope, nil), ErrNilReport)
		must.ErrorIs(t, env.updateErr(t, store, testScope, nil), ErrNilReport)

		// The scope the write binds is the argument's, so the unset one that has
		// to be refused is the argument. tenancy.Scope answers for that itself:
		// an unset scope is a driver error rather than a wider write.
		must.ErrorIs(t,
			env.createErr(t, store, tenancy.Scope{}, newReport(testReporter, "bug", "details")),
			tenancy.ErrNoScope)
		must.ErrorIs(t,
			env.updateErr(t, store, tenancy.Scope{}, newReport(testReporter, "bug", "details")),
			tenancy.ErrNoScope)
	})

	t.Run("an update revises what the reporter said", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "the button does nothing"))

		r.Kind = "typo"
		r.Details = "the label is misspelled"
		r.SubjectType = "labels"
		r.SubjectID = "label_9"

		revised, err := env.update(t, store, testScope, r)
		must.NoError(t, err)
		must.NotNil(t, revised)

		// The row the write left, not the one the caller assembled:
		// last_updated_at is the database's, and a value built from the argument
		// would say the row was last touched at the epoch.
		test.EqOp(t, "typo", revised.Kind)
		test.EqOp(t, "the label is misspelled", revised.Details)
		test.EqOp(t, "labels", revised.SubjectType)
		test.EqOp(t, "label_9", revised.SubjectID)
		must.NotNil(t, revised.LastUpdatedAt)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.NoError(t, err)
		test.EqOp(t, "typo", read.Kind)
		test.EqOp(t, "the label is misspelled", read.Details)
		test.EqOp(t, "labels", read.SubjectType)
		test.EqOp(t, "label_9", read.SubjectID)
		must.NotNil(t, read.LastUpdatedAt)
	})

	t.Run("an update cannot move the status", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		// The value carries a status the caller changed, and the statement's SET
		// list does not name the column — so the row does not move. The row the
		// write answers with is what makes that visible rather than assumed: it
		// carries the status the table holds, not the one the argument named.
		r.Status = StatusResolved

		revised, err := env.update(t, store, testScope, r)
		must.NoError(t, err)
		test.EqOp(t, StatusOpen, revised.Status)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusOpen, read.Status)
	})

	t.Run("a write cannot reach another scope's report", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		trespass := *r
		trespass.Scope = otherScope
		trespass.Details = "rewritten by somebody else"

		must.ErrorIs(t, env.updateErr(t, store, otherScope, &trespass), ErrReportNotFound)
		must.ErrorIs(t, env.archiveErr(t, store, otherScope, r.ID), ErrReportNotFound)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.NoError(t, err)
		test.EqOp(t, "details", read.Details)
	})

	t.Run("archiving takes a report out of the queue, once", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		hidden, err := env.archive(t, store, testScope, r.ID)
		must.NoError(t, err)
		must.NotNil(t, hidden)

		// The row the archive answers with is the one no read here can reach
		// afterwards: it carries the stamp the write set and the words somebody
		// filed, which is what a moderator's entry has to name.
		test.EqOp(t, r.ID, hidden.ID)
		test.EqOp(t, "details", hidden.Details)
		must.NotNil(t, hidden.ArchivedAt)

		_, err = store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.ErrorIs(t, err, ErrReportNotFound)

		// A second archive addresses a report that is no longer in the queue.
		must.ErrorIs(t, env.archiveErr(t, store, testScope, r.ID), ErrReportNotFound)
	})

	t.Run("an archived report is still readable through the filter", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.archive(t, store, testScope, r.ID)
		must.NoError(t, err)

		filter := filtering.DefaultQueryFilter()
		filter.IncludeArchived = pointer.To(true)

		page, err := store.ListReports(t.Context(), env.reader(), testScope, filter)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		must.NotNil(t, page.Data[0].ArchivedAt)
	})

	t.Run("a report that names another scope than the write is refused", func(t *testing.T) {
		t.Parallel()

		// The ruling the port settled: the argument is what the statement binds,
		// so a Report carrying a different scope is a caller filing one tenant's
		// report into another. It is refused rather than corrected, the same way
		// a report arriving already resolved is refused rather than reset.
		store := env.newStore(t)

		elsewhere := newReport(testReporter, "bug", "filed by somebody next door")
		elsewhere.Scope = otherScope

		must.ErrorIs(t, env.createErr(t, store, testScope, elsewhere), ErrScopeMismatch)

		page, err := store.ListReportsByReporter(t.Context(), env.reader(), testScope, testReporter, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)

		// And the same on the revision, where the stale value is the likelier
		// way in: a report read in one scope and written back in another.
		mine := filed(t, env, store, newReport(testReporter, "bug", "details"))
		mine.Scope = otherScope

		must.ErrorIs(t, env.updateErr(t, store, testScope, mine), ErrScopeMismatch)
	})

	t.Run("a report that names no scope adopts the write's", func(t *testing.T) {
		t.Parallel()

		// The other half of the same ruling, and the one that keeps a caller
		// assembling a fresh report from spelling the scope twice. tenancy.Scope
		// tells its zero value apart from Global(), so "unset" here is unset
		// rather than the global scope written shortly.
		store := env.newStore(t)

		fresh := newReport(testReporter, "bug", "no scope of its own")
		fresh.Scope = tenancy.Scope{}

		created, err := env.create(t, store, testScope, fresh)
		must.NoError(t, err)
		test.EqOp(t, testScope, created.Scope)

		// The adoption lands on the row and on what the write answered with,
		// never on the caller's value: an argument that named no scope still
		// names none afterwards.
		test.EqOp(t, tenancy.Scope{}, fresh.Scope)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, testScope, read.Scope)
	})
}

func runLifecycleSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a report moves through the lifecycle and stamps when it stopped", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		store := env.newStore(t, WithClock(c))
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		acknowledged, err := env.transition(t, store, testScope, r.ID,
			StatusOpen, StatusAcknowledged, "looking at it")
		must.NoError(t, err)
		test.EqOp(t, StatusAcknowledged, acknowledged.Status)
		// Not terminal, so nothing is stamped and the note is not kept: a report
		// somebody has picked up has no resolution yet.
		test.Nil(t, acknowledged.ClosedAt)
		test.EqOp(t, "", acknowledged.Resolution)

		c.advance(90 * time.Minute)

		resolved, err := env.transition(t, store, testScope, r.ID,
			StatusAcknowledged, StatusResolved, "fixed in 1.4")
		must.NoError(t, err)
		test.EqOp(t, StatusResolved, resolved.Status)
		test.EqOp(t, "fixed in 1.4", resolved.Resolution)
		must.NotNil(t, resolved.ClosedAt)
		test.True(t, resolved.Closed())
		test.EqOp(t, baseTime.Add(90*time.Minute), resolved.ClosedAt.UTC())
	})

	t.Run("reopening clears the stamp and the reason it no longer holds", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t, WithClock(newStubClock()))
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.transition(t, store, testScope, r.ID,
			StatusOpen, StatusDeclined, "working as intended")
		must.NoError(t, err)

		reopened, err := env.transition(t, store, testScope, r.ID,
			StatusDeclined, StatusOpen, "the reporter says otherwise")
		must.NoError(t, err)
		test.EqOp(t, StatusOpen, reopened.Status)
		test.Nil(t, reopened.ClosedAt)
		test.EqOp(t, "", reopened.Resolution)
	})

	t.Run("the second of two triagers loses the race", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.transition(t, store, testScope, r.ID,
			StatusOpen, StatusResolved, "fixed")
		must.NoError(t, err)

		// The second triager is holding the row as they read it: still open.
		_, err = env.transition(t, store, testScope, r.ID,
			StatusOpen, StatusDeclined, "duplicate")
		must.ErrorIs(t, err, ErrStatusConflict)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusResolved, read.Status)
		test.EqOp(t, "fixed", read.Resolution)
	})

	t.Run("a transition of a report that is not there says so", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := env.transition(t, store, testScope, "nonesuch",
			StatusOpen, StatusResolved, "fixed")
		must.ErrorIs(t, err, ErrReportNotFound)
	})

	t.Run("a transition in another scope cannot reach the row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.transition(t, store, otherScope, r.ID,
			StatusOpen, StatusResolved, "fixed")
		must.ErrorIs(t, err, ErrReportNotFound)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusOpen, read.Status)
	})

	t.Run("a move the lifecycle does not admit is refused before the write", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.transition(t, store, testScope, r.ID,
			StatusResolved, StatusAcknowledged, "")
		must.ErrorIs(t, err, ErrInvalidStatusTransition)

		_, err = env.transition(t, store, testScope, r.ID,
			StatusOpen, StatusOpen, "")
		must.ErrorIs(t, err, ErrInvalidStatusTransition)

		_, err = env.transition(t, store, testScope, r.ID,
			StatusOpen, Status("closed"), "")
		must.ErrorIs(t, err, ErrUnknownStatus)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusOpen, read.Status)
	})

	t.Run("an archived report has left the lifecycle", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := env.archive(t, store, testScope, r.ID)
		must.NoError(t, err)

		_, err = env.transition(t, store, testScope, r.ID,
			StatusOpen, StatusResolved, "fixed")
		must.ErrorIs(t, err, ErrReportNotFound)
	})
}

func runReadSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a read cannot see another scope's reports", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		_, err := store.GetReport(t.Context(), env.reader(), otherScope, r.ID)
		must.ErrorIs(t, err, ErrReportNotFound)

		page, err := store.ListReports(t.Context(), env.reader(), otherScope, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)
	})

	t.Run("the triage queue is one status at a time and counts the whole of it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for i := range 3 {
			r := filed(t, env, store, newReport(testReporter, "bug", "details"))
			if i == 0 {
				_, err := env.transition(t, store, testScope, r.ID,
					StatusOpen, StatusResolved, "fixed")
				must.NoError(t, err)
			}
		}

		open, err := store.ListReportsByStatus(t.Context(), env.reader(), testScope, StatusOpen, nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, open.Data)
		test.EqOp(t, uint64(2), open.FilteredCount)

		resolved, err := store.ListReportsByStatus(t.Context(), env.reader(), testScope, StatusResolved, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, resolved.Data)
	})

	t.Run("a queue nobody spelled right is refused rather than answered empty", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.ListReportsByStatus(t.Context(), env.reader(), testScope, Status("triaged"), nil)
		must.ErrorIs(t, err, ErrUnknownStatus)
	})

	t.Run("a reporter's own reports are theirs alone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mine := filed(t, env, store, newReport(testReporter, "bug", "mine"))
		filed(t, env, store, newReport(otherReporter, "bug", "theirs"))

		page, err := store.ListReportsByReporter(t.Context(), env.reader(), testScope, testReporter, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, mine.ID, page.Data[0].ID)

		_, err = store.ListReportsByReporter(t.Context(), env.reader(), testScope, "", nil)
		must.ErrorIs(t, err, ErrEmptyReporter)
	})

	t.Run("subject reads narrow from a kind of thing to one of them", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := filed(t, env, store, newReport(testReporter, "bug", "about recipe 1"))

		second := newReport(testReporter, "bug", "about recipe 2")
		second.SubjectID = "recipe_2"
		filed(t, env, store, second)

		elsewhere := newReport(testReporter, "bug", "about a label")
		elsewhere.SubjectType = "labels"
		elsewhere.SubjectID = "label_1"
		filed(t, env, store, elsewhere)

		byType, err := store.ListReportsBySubjectType(t.Context(), env.reader(), testScope, "recipes", nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, byType.Data)

		bySubject, err := store.ListReportsForSubject(t.Context(), env.reader(), testScope, "recipes", "recipe_1", nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, bySubject.Data)
		test.EqOp(t, first.ID, bySubject.Data[0].ID)
	})

	t.Run("a descending page is the ascending one reversed", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		ids := make([]string, 0, 3)
		for i := range 3 {
			r := newReport(testReporter, "bug", "details")
			r.ID = string(rune('a'+i)) + "_report"
			filed(t, env, store, r)
			ids = append(ids, r.ID)
		}

		ascending, err := store.ListReports(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		must.SliceLen(t, 3, ascending.Data)
		test.EqOp(t, ids[0], ascending.Data[0].ID)

		filter := filtering.DefaultQueryFilter()
		filter.SortBy = filtering.SortDescending

		descending, err := store.ListReports(t.Context(), env.reader(), testScope, filter)
		must.NoError(t, err)
		must.SliceLen(t, 3, descending.Data)
		test.EqOp(t, ids[2], descending.Data[0].ID)
	})

	t.Run("a page walks its cursor to the end", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for i := range 3 {
			r := newReport(testReporter, "bug", "details")
			r.ID = string(rune('a'+i)) + "_report"
			filed(t, env, store, r)
		}

		filter := filtering.DefaultQueryFilter()
		filter.MaxResponseSize = pointer.To(uint16(2))

		first, err := store.ListReports(t.Context(), env.reader(), testScope, filter)
		must.NoError(t, err)
		must.SliceLen(t, 2, first.Data)
		test.EqOp(t, uint64(3), first.FilteredCount)

		filter.SetCursor(&first.Cursor)

		second, err := store.ListReports(t.Context(), env.reader(), testScope, filter)
		must.NoError(t, err)
		test.SliceLen(t, 1, second.Data)
	})

	t.Run("a scopeless read is a refusal rather than a wider one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		// The zero Scope is the absence of a decision, not the global one.
		blank := tenancy.Scope{}

		_, err := store.GetReport(t.Context(), env.reader(), blank, "whatever")
		test.Error(t, err)

		_, err = store.ListReports(t.Context(), env.reader(), blank, nil)
		test.Error(t, err)

		_, err = store.ListReportsByStatus(t.Context(), env.reader(), blank, StatusOpen, nil)
		test.Error(t, err)

		_, err = store.ListReportsByReporter(t.Context(), env.reader(), blank, testReporter, nil)
		test.Error(t, err)

		_, err = store.ListReportsBySubjectType(t.Context(), env.reader(), blank, "recipes", nil)
		test.Error(t, err)

		_, err = store.ListReportsForSubject(t.Context(), env.reader(), blank, "recipes", "recipe_1", nil)
		test.Error(t, err)
	})
}

func runErasureSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("an erasure destroys one reporter's reports and nobody else's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		filed(t, env, store, newReport(testReporter, "bug", "mine"))

		// Archived and still theirs: an erasure has to reach what a soft delete
		// hid, or a subject's data survives their own request.
		archived := filed(t, env, store, newReport(testReporter, "bug", "archived but still mine"))

		_, archiveErr := env.archive(t, store, testScope, archived.ID)
		must.NoError(t, archiveErr)

		theirs := filed(t, env, store, newReport(otherReporter, "bug", "theirs"))

		deleted, err := env.erase(t, store, testScope, testReporter)
		must.NoError(t, err)
		test.EqOp(t, int64(2), deleted)

		mine, err := store.ListReportsByReporter(t.Context(), env.reader(), testScope, testReporter, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, mine.Data)

		survivor, err := store.GetReport(t.Context(), env.reader(), testScope, theirs.ID)
		must.NoError(t, err)
		test.EqOp(t, otherReporter, survivor.Reporter)
	})

	t.Run("an erasure cannot reach another scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		r := filed(t, env, store, newReport(testReporter, "bug", "details"))

		deleted, err := env.erase(t, store, otherScope, testReporter)
		must.NoError(t, err)
		test.EqOp(t, int64(0), deleted)

		_, err = store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.NoError(t, err)
	})

	t.Run("a subject who filed nothing is not a failure", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		deleted, err := env.erase(t, store, testScope, "never_filed_anything")
		must.NoError(t, err)
		test.EqOp(t, int64(0), deleted)
	})

	t.Run("an erasure naming nobody or no scope is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := env.erase(t, store, testScope, "")
		must.ErrorIs(t, err, ErrEmptyReporter)

		_, err = env.erase(t, store, tenancy.Scope{}, testReporter)
		must.ErrorIs(t, err, tenancy.ErrNoScope)
	})
}

// runTransactionSuite is the commit boundary, which is the whole of what this
// store's signatures buy its caller.
//
// Every write takes the caller's transaction and every read takes an executor, so
// what is under test here is not that the statements work — the other four suites
// cover that — but which side of a commit each of them lands on, and what a read
// handed the transaction can see. Those are the questions a store that opened its
// own transaction answered for its caller, and answered wrong.
func runTransactionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a write and a read inside one transaction observe each other", func(t *testing.T) {
		t.Parallel()

		// The property the reads were widened for, and the one no auto-committing
		// write could express: inside the transaction the report is there, and
		// from outside it is not there yet. A read narrowed to the client's
		// reader would have been reading a database that does not hold the row
		// its own caller just wrote.
		store := env.newStore(t)

		var created *Report

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			filing, err := store.CreateReport(t.Context(), tx, testScope,
				newReport(testReporter, "bug", "filed and read on one executor"))
			if err != nil {
				return err
			}

			created = filing

			read, err := store.GetReport(t.Context(), tx, testScope, created.ID)
			if err != nil {
				return err
			}

			test.EqOp(t, "filed and read on one executor", read.Details)

			queue, err := store.ListReportsByStatus(t.Context(), tx, testScope, StatusOpen, nil)
			if err != nil {
				return err
			}

			test.SliceLen(t, 1, queue.Data)

			// And the same read, on the client, cannot see it: the transaction
			// has not committed, so this is the other half of the same fact
			// rather than a second one.
			outside, err := store.ListReportsByStatus(t.Context(), env.reader(), testScope, StatusOpen, nil)
			if err != nil {
				return err
			}

			test.SliceEmpty(t, outside.Data)

			return nil
		}))

		// After the commit both executors agree, which is what makes the reading
		// above about visibility rather than about two different rows.
		read, err := store.GetReport(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, created.ID, read.ID)
	})

	t.Run("the four writes commit with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		store := env.newStore(t, WithClock(c))

		edited := filed(t, env, store, newReport(testReporter, "bug", "before the edit"))
		decided := filed(t, env, store, newReport(testReporter, "bug", "to be resolved"))
		doomed := filed(t, env, store, newReport(testReporter, "bug", "on the way out"))

		var created, revised, moved, hidden *Report

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			if created, err = store.CreateReport(t.Context(), tx, testScope,
				newReport(testReporter, "bug", "written inside")); err != nil {
				return err
			}

			edited.Details = "after the edit"
			if revised, err = store.UpdateReport(t.Context(), tx, testScope, edited); err != nil {
				return err
			}

			if moved, err = store.TransitionReport(t.Context(), tx, testScope, decided.ID,
				StatusOpen, StatusResolved, "fixed in 1.4"); err != nil {
				return err
			}

			hidden, err = store.ArchiveReport(t.Context(), tx, testScope, doomed.ID)

			return err
		}))

		// All four read their row back through the caller's executor, so each
		// answers with the row this transaction wrote rather than with a value
		// waiting on a commit. That is what a caller's audit entry describes,
		// which is why they return the report rather than only an error.
		must.NotNil(t, created)
		test.NotEqOp(t, "", created.ID)
		test.False(t, created.CreatedAt.IsZero())
		test.EqOp(t, StatusOpen, created.Status)

		must.NotNil(t, revised)
		test.EqOp(t, "after the edit", revised.Details)
		must.NotNil(t, revised.LastUpdatedAt)

		must.NotNil(t, moved)
		test.EqOp(t, StatusResolved, moved.Status)
		test.EqOp(t, "fixed in 1.4", moved.Resolution)
		must.NotNil(t, moved.ClosedAt)
		test.EqOp(t, baseTime, moved.ClosedAt.UTC())

		// The archive's row is the one that could not have been read afterwards
		// at all: every keyed read here filters it out, which is what archiving
		// means.
		must.NotNil(t, hidden)
		test.EqOp(t, doomed.ID, hidden.ID)
		test.EqOp(t, "on the way out", hidden.Details)
		must.NotNil(t, hidden.ArchivedAt)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, "written inside", read.Details)

		read, err = store.GetReport(t.Context(), env.reader(), testScope, edited.ID)
		must.NoError(t, err)
		test.EqOp(t, "after the edit", read.Details)

		read, err = store.GetReport(t.Context(), env.reader(), testScope, decided.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusResolved, read.Status)
		test.EqOp(t, "fixed in 1.4", read.Resolution)

		_, err = store.GetReport(t.Context(), env.reader(), testScope, doomed.ID)
		must.ErrorIs(t, err, ErrReportNotFound)
	})

	t.Run("a rolled back transaction takes all four writes with it", func(t *testing.T) {
		t.Parallel()

		// This is the whole point of the signature, seen from the side that
		// matters: the consumer's companion write fails, and the report goes back
		// with it rather than surviving in a transaction it was never part of.
		store := env.newStore(t)

		edited := filed(t, env, store, newReport(testReporter, "bug", "the original"))
		decided := filed(t, env, store, newReport(testReporter, "bug", "still open"))
		doomed := filed(t, env, store, newReport(testReporter, "bug", "still here"))

		var created *Report

		err := env.inTx(t, func(tx database.Tx) error {
			var txErr error
			if created, txErr = store.CreateReport(t.Context(), tx, testScope,
				newReport(testReporter, "bug", "never committed")); txErr != nil {
				return txErr
			}

			edited.Details = "the edit"
			if _, txErr = store.UpdateReport(t.Context(), tx, testScope, edited); txErr != nil {
				return txErr
			}

			if _, txErr = store.TransitionReport(t.Context(), tx, testScope, decided.ID,
				StatusOpen, StatusDeclined, "duplicate"); txErr != nil {
				return txErr
			}

			if _, txErr = store.ArchiveReport(t.Context(), tx, testScope, doomed.ID); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The id was minted onto the value the write answered with, and that
		// value still holds it: what rolled back is the row, not the answer the
		// write gave before the companion failed.
		must.NotNil(t, created)
		test.NotEqOp(t, "", created.ID)

		_, err = store.GetReport(t.Context(), env.reader(), testScope, created.ID)
		must.ErrorIs(t, err, ErrReportNotFound)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, edited.ID)
		must.NoError(t, err)
		test.EqOp(t, "the original", read.Details)

		read, err = store.GetReport(t.Context(), env.reader(), testScope, decided.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusOpen, read.Status)
		test.EqOp(t, "", read.Resolution)
		test.Nil(t, read.ClosedAt)

		read, err = store.GetReport(t.Context(), env.reader(), testScope, doomed.ID)
		must.NoError(t, err)
		test.EqOp(t, "still here", read.Details)
	})

	t.Run("an erasure rolls back with the rest of a subject's footprint", func(t *testing.T) {
		t.Parallel()

		// The erasure's half of the same fact. dataprivacy runs one transaction
		// across every registered eraser, so a later one failing must leave this
		// package's rows where they were rather than half-erased.
		store := env.newStore(t)

		r := filed(t, env, store, newReport(testReporter, "bug", "still here afterwards"))

		err := env.inTx(t, func(tx database.Tx) error {
			deleted, txErr := store.DeleteReportsByReporter(t.Context(), tx, testScope, testReporter)
			if txErr != nil {
				return txErr
			}

			test.EqOp(t, int64(1), deleted)

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, r.ID)
		must.NoError(t, err)
		test.EqOp(t, "still here afterwards", read.Details)
	})

	t.Run("a transition finds a report filed in the same transaction", func(t *testing.T) {
		t.Parallel()

		// The guard and the read-back run on the caller's executor, so a report
		// filed and decided in one transaction resolves instead of reporting the
		// report absent.
		store := env.newStore(t, WithClock(newStubClock()))

		var filing, moved *Report

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var err error
			if filing, err = store.CreateReport(t.Context(), tx, testScope,
				newReport(testReporter, "bug", "filed and decided at once")); err != nil {
				return err
			}

			moved, err = store.TransitionReport(t.Context(), tx, testScope, filing.ID,
				StatusOpen, StatusDeclined, "working as intended")

			return err
		}))

		must.NotNil(t, moved)
		test.EqOp(t, filing.ID, moved.ID)
		test.EqOp(t, StatusDeclined, moved.Status)
		test.EqOp(t, "working as intended", moved.Resolution)
		must.NotNil(t, moved.ClosedAt)

		read, err := store.GetReport(t.Context(), env.reader(), testScope, filing.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusDeclined, read.Status)
	})

	t.Run("every method refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// Every one of the eleven, not a representative one. There is no
		// connection of the store's own to fall back to, so a method that did
		// anything but refuse would be reaching for something that is not there.
		store := env.newStore(t)

		_, err := store.CreateReport(t.Context(), nil, testScope,
			newReport(testReporter, "bug", "details"))
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.UpdateReport(t.Context(), nil, testScope,
			newReport(testReporter, "bug", "details"))
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ArchiveReport(t.Context(), nil, testScope, "report_1")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.TransitionReport(t.Context(), nil, testScope, "report_1",
			StatusOpen, StatusResolved, "fixed")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.DeleteReportsByReporter(t.Context(), nil, testScope, testReporter)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetReport(t.Context(), nil, testScope, "report_1")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListReports(t.Context(), nil, testScope, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListReportsByStatus(t.Context(), nil, testScope, StatusOpen, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListReportsByReporter(t.Context(), nil, testScope, testReporter, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListReportsBySubjectType(t.Context(), nil, testScope, "recipes", nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListReportsForSubject(t.Context(), nil, testScope, "recipes", "recipe_1", nil)
		must.ErrorIs(t, err, ErrNilExecutor)
	})

	t.Run("a refused write inside a transaction leaves the transaction usable", func(t *testing.T) {
		t.Parallel()

		// Every check the writes make runs before any statement they would send,
		// so a refusal is the store declining rather than the database aborting.
		// A caller that inspects one and carries on has a transaction to carry on
		// in, which is what lets these be collected here and asserted outside.
		// None of them reaches a statement that errors: a refused move never runs
		// one, and a guard that matches nothing is a count rather than a failure.
		store := env.newStore(t)

		// Decided outside the transaction, so the triager inside it is holding a
		// stale view: the one miss the guard exists to catch.
		contested := filed(t, env, store, newReport(testReporter, "bug", "already decided"))
		_, err := env.transition(t, store, testScope, contested.ID,
			StatusOpen, StatusResolved, "fixed")
		must.NoError(t, err)

		elsewhere := newReport(testReporter, "bug", "details")
		elsewhere.Scope = otherScope

		bornResolved := newReport(testReporter, "bug", "details")
		bornResolved.Status = StatusResolved

		var (
			nilCreate, mismatchedCreate, resolvedCreate   error
			nilUpdate, emptyDetails, missingUpdate        error
			inadmissible, unknown, missingMove, staleMove error
			missingArchive                                error
		)

		var survivor *Report

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			// The row each refusal answers with, checked as it is collected.
			// A write that refused hands back nothing, which is the other half
			// of the rule these signatures carry: the report comes back only
			// alongside a nil error, so a caller who checked the error has
			// nothing to guard against.
			var refused *Report

			refused, nilCreate = store.CreateReport(t.Context(), tx, testScope, nil)
			test.Nil(t, refused)

			refused, mismatchedCreate = store.CreateReport(t.Context(), tx, testScope, elsewhere)
			test.Nil(t, refused)

			refused, resolvedCreate = store.CreateReport(t.Context(), tx, testScope, bornResolved)
			test.Nil(t, refused)

			refused, nilUpdate = store.UpdateReport(t.Context(), tx, testScope, nil)
			test.Nil(t, refused)

			silent := newReport(testReporter, "bug", "")
			silent.ID = "report_never_written"
			refused, emptyDetails = store.UpdateReport(t.Context(), tx, testScope, silent)
			test.Nil(t, refused)

			absent := newReport(testReporter, "bug", "an edit to nothing")
			absent.ID = "report_never_written"
			refused, missingUpdate = store.UpdateReport(t.Context(), tx, testScope, absent)
			test.Nil(t, refused)

			refused, inadmissible = store.TransitionReport(t.Context(), tx, testScope, contested.ID,
				StatusResolved, StatusAcknowledged, "")
			test.Nil(t, refused)

			refused, unknown = store.TransitionReport(t.Context(), tx, testScope, contested.ID,
				StatusOpen, Status("closed"), "")
			test.Nil(t, refused)

			refused, missingMove = store.TransitionReport(t.Context(), tx, testScope, "report_never_written",
				StatusOpen, StatusResolved, "fixed")
			test.Nil(t, refused)

			refused, staleMove = store.TransitionReport(t.Context(), tx, testScope, contested.ID,
				StatusOpen, StatusDeclined, "duplicate")
			test.Nil(t, refused)

			refused, missingArchive = store.ArchiveReport(t.Context(), tx, testScope, "report_never_written")
			test.Nil(t, refused)

			var createErr error
			survivor, createErr = store.CreateReport(t.Context(), tx, testScope,
				newReport(testReporter, "bug", "filed after all the refusals"))

			return createErr
		}))

		must.ErrorIs(t, nilCreate, ErrNilReport)
		must.ErrorIs(t, mismatchedCreate, ErrScopeMismatch)
		must.ErrorIs(t, resolvedCreate, ErrInvalidStatusTransition)
		must.ErrorIs(t, nilUpdate, ErrNilReport)
		must.ErrorIs(t, emptyDetails, ErrEmptyDetails)
		must.ErrorIs(t, missingUpdate, ErrReportNotFound)
		must.ErrorIs(t, inadmissible, ErrInvalidStatusTransition)
		must.ErrorIs(t, unknown, ErrUnknownStatus)
		must.ErrorIs(t, missingMove, ErrReportNotFound)
		must.ErrorIs(t, staleMove, ErrStatusConflict)
		must.ErrorIs(t, missingArchive, ErrReportNotFound)

		// The stale triager wrote nothing, and the first decision stands.
		read, err := store.GetReport(t.Context(), env.reader(), testScope, contested.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusResolved, read.Status)
		test.EqOp(t, "fixed", read.Resolution)

		// And the write after all of them committed, which is what "usable"
		// means here.
		must.NotNil(t, survivor)

		read, err = store.GetReport(t.Context(), env.reader(), testScope, survivor.ID)
		must.NoError(t, err)
		test.EqOp(t, "filed after all the refusals", read.Details)
	})
}

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a prefix that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(env.client, WithTablePrefix("no-hyphens-allowed"))
		test.Error(t, err)
	})

	T.Run("reports the prefix it was built with", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, WithTablePrefix("ir"))
		must.NoError(t, err)
		test.EqOp(t, "ir", store.TablePrefix())

		unprefixed, err := NewSQLStore(env.client)
		must.NoError(t, err)
		test.EqOp(t, DefaultTablePrefix, unprefixed.TablePrefix())
	})

	T.Run("ignores a nil option and a nil clock", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, nil, WithClock(nil))
		must.NoError(t, err)
		must.NotNil(t, store)
		test.False(t, store.now().IsZero())
	})
}

func TestIssuereportsdbDialect(T *testing.T) {
	T.Parallel()

	T.Run("maps every dialect this module supports", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			mapped, err := issuereportsdbDialect(d)
			must.NoError(t, err, must.Sprintf("dialect %s", d))
			test.NotEqOp(t, "", string(mapped))
		}
	})

	T.Run("names a dialect the querier was not generated for", func(t *testing.T) {
		t.Parallel()

		_, err := issuereportsdbDialect(dialect.Dialect("cassandra"))
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}
