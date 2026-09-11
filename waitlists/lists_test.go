package waitlists

import (
	"testing"
	"time"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runListSuite is every assertion about the catalog half of the store.
func runListSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("CreateList", func(T *testing.T) {
		T.Run("stores the list and stamps the database's creation time", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, openList("Launch"))

			test.NotEqOp(t, "", created.ID)
			test.EqOp(t, testScope, created.Scope)
			test.EqOp(t, "Launch", created.Name)
			test.False(t, created.CreatedAt.IsZero())
			test.Nil(t, created.LastUpdatedAt)
			test.Nil(t, created.ArchivedAt)

			read, err := store.GetList(t.Context(), env.reader(), testScope, created.ID)
			must.NoError(t, err)
			test.EqOp(t, created.ID, read.ID)
			test.EqOp(t, created.ClosesAt.UTC(), read.ClosesAt)
		})

		T.Run("refuses a list with nothing to identify or close it", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			_, err := env.createList(t, store, testScope, nil)
			test.ErrorIs(t, err, ErrNilList)

			_, err = env.createList(t, store, testScope, &List{ClosesAt: testNow})
			test.ErrorIs(t, err, ErrEmptyListName)

			// There is no default for the closing time — see ErrEmptyClosesAt.
			_, err = env.createList(t, store, testScope, &List{Name: "Launch"})
			test.ErrorIs(t, err, ErrEmptyClosesAt)
		})

		T.Run("refuses an unset scope", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			_, err := env.createList(t, store, tenancy.Scope{}, openList("Launch"))
			test.Error(t, err)
		})

		T.Run("refuses a list that names another scope than the write", func(t *testing.T) {
			t.Parallel()

			// The argument is what the statement binds, so a list carrying a
			// different scope is a caller writing one tenant's list into
			// another. It is refused rather than corrected — see Store.
			store := env.newStore(t)

			elsewhere := openList("Launch")
			elsewhere.Scope = otherScope

			_, err := env.createList(t, store, testScope, elsewhere)
			test.ErrorIs(t, err, ErrScopeMismatch)

			page, err := store.ListLists(t.Context(), env.reader(), testScope, nil)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)
		})

		T.Run("a list that names no scope adopts the write's", func(t *testing.T) {
			t.Parallel()

			// The other half of the same ruling, and the one that keeps a caller
			// assembling a fresh list from spelling the scope twice.
			// tenancy.Scope tells its zero value apart from Global(), so "unset"
			// here is unset rather than the global scope written shortly.
			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, openList("Launch"))
			test.EqOp(t, testScope, created.Scope)

			read, err := store.GetList(t.Context(), env.reader(), testScope, created.ID)
			must.NoError(t, err)
			test.EqOp(t, testScope, read.Scope)
		})
	})

	t.Run("GetList", func(T *testing.T) {
		T.Run("does not cross scopes", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, openList("Launch"))

			_, err := store.GetList(t.Context(), env.reader(), otherScope, created.ID)
			test.ErrorIs(t, err, ErrListNotFound)
		})

		T.Run("reports a missing list rather than an empty one", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			_, err := store.GetList(t.Context(), env.reader(), testScope, "nope")
			test.ErrorIs(t, err, ErrListNotFound)

			_, err = store.GetList(t.Context(), env.reader(), testScope, "")
			test.ErrorIs(t, err, platformerrors.ErrInvalidIDProvided)
		})
	})

	t.Run("ListLists", func(T *testing.T) {
		T.Run("pages the scope's catalog in both directions", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			first := mustCreateList(t, env, store, testScope, openList("first"))
			second := mustCreateList(t, env, store, testScope, openList("second"))
			mustCreateList(t, env, store, otherScope, openList("theirs"))

			page, err := store.ListLists(t.Context(), env.reader(), testScope, nil)
			must.NoError(t, err)
			must.SliceLen(t, 2, page.Data)
			test.EqOp(t, first.ID, page.Data[0].ID)
			test.EqOp(t, second.ID, page.Data[1].ID)

			descending := filtering.DefaultQueryFilter()
			descending.SortBy = filtering.SortDescending

			page, err = store.ListLists(t.Context(), env.reader(), testScope, descending)
			must.NoError(t, err)
			must.SliceLen(t, 2, page.Data)
			test.EqOp(t, second.ID, page.Data[0].ID)
		})

		T.Run("leaves archived lists out unless asked", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, openList("Launch"))
			must.NoError(t, env.archiveList(t, store, testScope, created.ID))

			page, err := store.ListLists(t.Context(), env.reader(), testScope, nil)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)

			withArchived := filtering.DefaultQueryFilter()
			withArchived.IncludeArchived = pointer.To(true)

			page, err = store.ListLists(t.Context(), env.reader(), testScope, withArchived)
			must.NoError(t, err)
			must.SliceLen(t, 1, page.Data)
			test.NotNil(t, page.Data[0].ArchivedAt)
		})
	})

	t.Run("ListOpenLists", func(T *testing.T) {
		T.Run("answers on the store's clock, not the server's", func(t *testing.T) {
			t.Parallel()

			c := newStubClock()
			store := env.newStore(t, WithClock(c))

			open := mustCreateList(t, env, store, testScope, openList("open"))
			mustCreateList(t, env, store, testScope, closedList("closed"))

			page, err := store.ListOpenLists(t.Context(), env.reader(), testScope, nil)
			must.NoError(t, err)
			must.SliceLen(t, 1, page.Data)
			test.EqOp(t, open.ID, page.Data[0].ID)

			// The whole reason the horizon is bound rather than read off
			// CURRENT_TIMESTAMP: moving the store's clock past the closing time
			// takes the list off this page, and List.OpenAt agrees.
			c.advance(31 * 24 * time.Hour)

			page, err = store.ListOpenLists(t.Context(), env.reader(), testScope, nil)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)

			read, err := store.GetList(t.Context(), env.reader(), testScope, open.ID)
			must.NoError(t, err)
			test.False(t, read.OpenAt(c.Now()))
		})

		T.Run("pages descending too", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			mustCreateList(t, env, store, testScope, openList("first"))
			second := mustCreateList(t, env, store, testScope, openList("second"))

			descending := filtering.DefaultQueryFilter()
			descending.SortBy = filtering.SortDescending

			page, err := store.ListOpenLists(t.Context(), env.reader(), testScope, descending)
			must.NoError(t, err)
			must.SliceLen(t, 2, page.Data)
			test.EqOp(t, second.ID, page.Data[0].ID)
		})

		T.Run("an archived list is closed whatever its closing time says", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, openList("Launch"))
			must.NoError(t, env.archiveList(t, store, testScope, created.ID))

			page, err := store.ListOpenLists(t.Context(), env.reader(), testScope, nil)
			must.NoError(t, err)
			test.SliceEmpty(t, page.Data)
		})
	})

	t.Run("UpdateList", func(T *testing.T) {
		T.Run("rewrites the name, description and closing time", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, openList("Launch"))

			created.Name = "Beta"
			created.Description = "now with fewer bugs"
			created.ClosesAt = testNow.Add(90 * 24 * time.Hour)

			must.NoError(t, env.updateList(t, store, testScope, created))

			read, err := store.GetList(t.Context(), env.reader(), testScope, created.ID)
			must.NoError(t, err)
			test.EqOp(t, "Beta", read.Name)
			test.EqOp(t, "now with fewer bugs", read.Description)
			test.EqOp(t, created.ClosesAt.UTC(), read.ClosesAt)
			test.NotNil(t, read.LastUpdatedAt)
		})

		T.Run("brings a closed list back by moving its horizon", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, closedList("Launch"))

			created.ClosesAt = testNow.Add(time.Hour)
			must.NoError(t, env.updateList(t, store, testScope, created))

			page, err := store.ListOpenLists(t.Context(), env.reader(), testScope, nil)
			must.NoError(t, err)
			must.SliceLen(t, 1, page.Data)
		})

		T.Run("refuses what it cannot address", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, openList("Launch"))

			test.ErrorIs(t, env.updateList(t, store, testScope, nil), ErrNilList)
			test.ErrorIs(t, env.updateList(t, store, testScope, &List{ClosesAt: testNow, Name: "x"}),
				platformerrors.ErrInvalidIDProvided)

			// A list that names one tenant and a write that names another is a
			// mix-up rather than a thing to guess at — see Store.
			test.ErrorIs(t, env.updateList(t, store, otherScope, created), ErrScopeMismatch)

			// And with the entity saying nothing about whose it is, the scope
			// predicate on the statement is what refuses it, which is the whole
			// of what that predicate is for.
			unclaimed := *created
			unclaimed.Scope = tenancy.Scope{}
			test.ErrorIs(t, env.updateList(t, store, otherScope, &unclaimed), ErrListNotFound)

			must.NoError(t, env.archiveList(t, store, testScope, created.ID))
			test.ErrorIs(t, env.updateList(t, store, testScope, created), ErrListNotFound)
		})
	})

	t.Run("ArchiveList", func(T *testing.T) {
		T.Run("is idempotent and scoped", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			created := mustCreateList(t, env, store, testScope, openList("Launch"))

			test.ErrorIs(t, env.archiveList(t, store, otherScope, created.ID), ErrListNotFound)

			must.NoError(t, env.archiveList(t, store, testScope, created.ID))

			// A second archive reports the row was not there to archive rather
			// than restamping the moment it was retired.
			test.ErrorIs(t, env.archiveList(t, store, testScope, created.ID), ErrListNotFound)
		})

		T.Run("leaves the signups against it readable", func(t *testing.T) {
			t.Parallel()

			store := env.newStore(t)

			list := mustCreateList(t, env, store, testScope, openList("Launch"))
			signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

			must.NoError(t, env.archiveList(t, store, testScope, list.ID))

			// Archiving is not erasure: the queue is still there to be worked
			// through, and only the list has left the catalog.
			read, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
			must.NoError(t, err)
			test.EqOp(t, signup.ID, read.ID)
		})
	})
}
