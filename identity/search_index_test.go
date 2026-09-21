package identity

import (
	"slices"
	"testing"

	"github.com/primandproper/primitives-go/v2/database"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runSearchIndexSuite covers the bookkeeping a search sync keeps against the
// directory: the backstop walk, and the stamp that records what an index took.
func runSearchIndexSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("walks every live user in byte order, paged", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		want := []string{}
		for _, name := range []string{"ada", "brin", "curie", "dijkstra"} {
			user := seedUser(t, env, store, newUser(name))
			want = append(want, user.ID)
		}

		slices.Sort(want)

		var (
			got    []string
			cursor string
		)

		for range 10 {
			page, err := store.ScanUsersForReindex(t.Context(), env.reader(), cursor, 2)
			must.NoError(t, err)

			if len(page) == 0 {
				break
			}

			test.LessEq(t, 2, len(page), test.Sprint("a page larger than the size asked for"))

			got = append(got, page...)
			cursor = page[len(page)-1]
		}

		test.Eq(t, want, got)
	})

	// The stamp is not a filter on the scan, which is the property the scan's
	// documentation argues for: a backstop that skipped stamped rows would
	// trust bookkeeping written by the thing it exists to check.
	t.Run("a stamped user is still walked", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			stamped, err := store.MarkUsersAsIndexed(t.Context(), tx, []string{user.ID})
			test.EqOp(t, int64(1), stamped)

			return err
		}))

		page, err := store.ScanUsersForReindex(t.Context(), env.reader(), "", 50)
		must.NoError(t, err)
		test.SliceContains(t, page, user.ID)
	})

	t.Run("an archived user is not walked", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := store.ArchiveUser(t.Context(), tx, testScope, user.ID)

			return err
		}))

		page, err := store.ScanUsersForReindex(t.Context(), env.reader(), "", 50)
		must.NoError(t, err)
		test.SliceNotContains(t, page, user.ID)
	})

	// An empty flush is the ordinary state of a buffer nothing wrote to before
	// its interval elapsed. UPDATE ... IN () is not legal on every dialect, so
	// answering it without a statement is correctness rather than thrift.
	t.Run("an empty stamp runs no statement and is not an error", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			stamped, err := store.MarkUsersAsIndexed(t.Context(), tx, nil)
			test.EqOp(t, int64(0), stamped)

			return err
		}))
	})

	// The count is what moved, not what was named: a set naming rows erased
	// since the index accepted them stamps fewer, and that is not an error.
	t.Run("stamping an id that is gone is not an error", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			stamped, err := store.MarkUsersAsIndexed(t.Context(), tx, []string{user.ID, "never_existed"})
			test.EqOp(t, int64(1), stamped)

			return err
		}))
	})
}
