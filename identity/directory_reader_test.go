package identity

import (
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runDirectoryReaderSuite covers the reads: users, accounts, and the
// memberships between them, every one of them scoped.
func runDirectoryReaderSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("hides a user from another directory", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		user := seedUser(t, env, store, newUser("ada"))

		_, err := store.GetUser(t.Context(), env.reader(), otherScope, user.ID)
		must.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("refuses an unset scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.GetUser(t.Context(), env.reader(), tenancy.Scope{}, "whatever")
		must.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	t.Run("pages the directory, redacted", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Written with ascending ids, because the page is ordered by id — the
		// cursor names a position in an order, and the order is the statement's.
		var created []*User
		for i, name := range []string{"ada", "brian", "carol", "dennis"} {
			user := newUser(name)
			user.ID = fmt.Sprintf("u_%02d", i)
			created = append(created, seedUser(t, env, store, user))
		}

		neighbor := newUser("eve")
		neighbor.Scope = otherScope
		seedUser(t, env, store, neighbor)

		page, err := store.ListUsers(t.Context(), env.reader(), testScope, &filtering.QueryFilter{
			MaxResponseSize: pointer.To(uint16(2)),
		})
		must.NoError(t, err)
		must.SliceLen(t, 2, page.Data)
		test.EqOp(t, "ada", page.Data[0].Username)
		test.EqOp(t, "brian", page.Data[1].Username)

		// A page is the read most likely to reach a response body.
		for _, user := range page.Data {
			test.EqOp(t, "", user.HashedPassword)
			test.EqOp(t, "", user.TwoFactorSecret)
		}

		// Both counts ride on the rows of the one statement, so the first page
		// already knows how many there are.
		filtered, total, known := page.Counts()
		test.True(t, known)
		test.EqOp(t, uint64(4), total)
		test.EqOp(t, uint64(4), filtered)

		next, err := store.ListUsers(t.Context(), env.reader(), testScope, &filtering.QueryFilter{
			MaxResponseSize: pointer.To(uint16(2)),
			Cursor:          pointer.To(created[1].ID),
		})
		must.NoError(t, err)
		must.SliceLen(t, 2, next.Data)
		test.EqOp(t, "carol", next.Data[0].Username)

		// The neighbor's directory is in neither count.
		filtered, total, known = next.Counts()
		test.True(t, known)
		test.EqOp(t, uint64(4), total)
		test.EqOp(t, uint64(4), filtered)
	})

	// sortBy is the parameter that parsed, validated and normalized for a long
	// time while every list answered oldest-first regardless. It is answered by
	// a second statement rather than by a bound argument — see
	// filtering.QueryFilter.SortsDescending — so the assertion that matters is
	// that the walk is a walk: newest first, every row once, and the counts
	// describing the same collection the ascending page described.
	t.Run("pages the directory newest first when the filter says so", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		var created []*User
		for i, name := range []string{"ada", "brian", "carol", "dennis"} {
			user := newUser(name)
			user.ID = fmt.Sprintf("u_%02d", i)
			created = append(created, seedUser(t, env, store, user))
		}

		neighbor := newUser("eve")
		neighbor.Scope = otherScope
		seedUser(t, env, store, neighbor)

		descending := func(cursor *string) *filtering.QueryFilter {
			return &filtering.QueryFilter{
				SortBy:          filtering.SortDescending,
				MaxResponseSize: pointer.To(uint16(2)),
				Cursor:          cursor,
			}
		}

		// The first page has no cursor, which is the case the descending walk
		// has no sentinel for: it coalesces to the row's own key rather than to
		// a string that would be above every id in one collation and below some
		// of them in another.
		first, err := store.ListUsers(t.Context(), env.reader(), testScope, descending(nil))
		must.NoError(t, err)
		must.SliceLen(t, 2, first.Data)
		test.EqOp(t, "dennis", first.Data[0].Username)
		test.EqOp(t, "carol", first.Data[1].Username)

		// The counts describe the collection rather than the page, so they are
		// the ascending page's counts and the neighbor's directory is in
		// neither.
		filtered, total, known := first.Counts()
		must.True(t, known)
		test.EqOp(t, uint64(4), filtered)
		test.EqOp(t, uint64(4), total)

		// And the page is still redacted, because that is the store's rule
		// rather than the statement's.
		for _, user := range first.Data {
			test.EqOp(t, "", user.HashedPassword)
			test.EqOp(t, "", user.TwoFactorSecret)
		}

		second, err := store.ListUsers(t.Context(), env.reader(), testScope, descending(pointer.To(first.Cursor)))
		must.NoError(t, err)
		must.SliceLen(t, 2, second.Data)
		test.EqOp(t, "brian", second.Data[0].Username)
		test.EqOp(t, "ada", second.Data[1].Username)

		// The walk ends rather than repeating its last page, which is what a
		// cursor comparison pointing the wrong way would do.
		last, err := store.ListUsers(t.Context(), env.reader(), testScope, descending(pointer.To(second.Cursor)))
		must.NoError(t, err)
		test.SliceEmpty(t, last.Data)

		// The ascending page over the same filter is the same four users the
		// other way round: the window, the archived toggle and the scope are
		// one text on both statements.
		ascending, err := store.ListUsers(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		must.SliceLen(t, 4, ascending.Data)

		for i, user := range ascending.Data {
			test.EqOp(t, created[i].ID, user.ID)
		}
	})

	t.Run("answers the other paged reads newest first too", func(t *testing.T) {
		t.Parallel()

		// Every paged read this store serves takes the same filter, so a
		// direction honored by the directory and dropped by the roster would be
		// the same silent wrong order in a different response body.
		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))

		first := seedAccountFor(t, env, store, owner, "First")
		second := seedAccountFor(t, env, store, owner, "Second")

		for _, name := range []string{"brian", "carol"} {
			seedUserInto(t, env, store, newUser(name), first.ID)
		}

		newestFirst := &filtering.QueryFilter{SortBy: filtering.SortDescending}

		accounts, err := store.ListAccounts(t.Context(), env.reader(), testScope, newestFirst)
		must.NoError(t, err)
		must.SliceLen(t, 2, accounts.Data)
		test.EqOp(t, second.ID, accounts.Data[0].ID)
		test.EqOp(t, first.ID, accounts.Data[1].ID)

		mine, err := store.ListAccountsForUser(t.Context(), env.reader(), testScope, owner.ID, newestFirst)
		must.NoError(t, err)
		must.SliceLen(t, 2, mine.Data)
		test.EqOp(t, second.ID, mine.Data[0].ID)
		test.EqOp(t, first.ID, mine.Data[1].ID)

		roster, err := store.ListAccountMembers(t.Context(), env.reader(), testScope, first.ID, newestFirst)
		must.NoError(t, err)
		must.SliceLen(t, 3, roster.Data)

		ascendingRoster, err := store.ListAccountMembers(t.Context(), env.reader(), testScope, first.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 3, ascendingRoster.Data)

		// The roster is a page of memberships with the member attached, so the
		// order reversed is the membership ids reversed — and the joined user
		// still comes back beside each one.
		for i, member := range roster.Data {
			test.EqOp(t, ascendingRoster.Data[len(ascendingRoster.Data)-1-i].ID, member.ID)
			must.NotNil(t, member.User)
			test.EqOp(t, "", member.User.HashedPassword)
		}
	})

	t.Run("searches a username prefix in the direction the filter names", func(t *testing.T) {
		t.Parallel()

		// The search's order is the searched column's rather than the id's, so
		// what a direction reverses here is the alphabet. A search that ignored
		// the filter would look identical to one that honored it on the first
		// page of a one-page result, which is why this one pages.
		store := env.newStore(t)
		for _, name := range []string{"ada", "adam", "adele", "brian"} {
			seedUser(t, env, store, newUser(name))
		}

		page, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "ad",
			&filtering.QueryFilter{SortBy: filtering.SortDescending, MaxResponseSize: pointer.To(uint16(2))})
		must.NoError(t, err)
		must.SliceLen(t, 2, page.Data)
		test.EqOp(t, "adele", page.Data[0].Username)
		test.EqOp(t, "adam", page.Data[1].Username)

		// The count is its own statement and does not depend on the direction.
		_, total, known := page.Counts()
		must.True(t, known)
		test.EqOp(t, uint64(3), total)

		// The cursor is the username, so the next page resumes below it.
		next, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "ad",
			&filtering.QueryFilter{
				SortBy:          filtering.SortDescending,
				MaxResponseSize: pointer.To(uint16(2)),
				Cursor:          pointer.To(page.Cursor),
			})
		must.NoError(t, err)
		must.SliceLen(t, 1, next.Data)
		test.EqOp(t, "ada", next.Data[0].Username)
	})

	// The window the hand-written page could not express. It is the reason the
	// list signatures moved to a filter rather than a cursor and a limit.
	t.Run("pages the directory through the created window", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		early := seedUser(t, env, store, newUser("ada"))

		// A whole second, not a millisecond: SQLite has no timestamp type, so
		// its created_at holds the text CURRENT_TIMESTAMP wrote and compares
		// lexicographically at second granularity — a sub-second cutoff there
		// is the same string as the row's own stamp and excludes it.
		cutoff := early.CreatedAt.Add(time.Second)

		before, err := store.ListUsers(t.Context(), env.reader(), testScope, &filtering.QueryFilter{
			CreatedBefore: pointer.To(cutoff),
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, before.Data)

		after, err := store.ListUsers(t.Context(), env.reader(), testScope, &filtering.QueryFilter{
			CreatedAfter: pointer.To(cutoff),
		})
		must.NoError(t, err)
		test.SliceEmpty(t, after.Data)

		// An empty page carries no counts, because the counts ride on the rows
		// — and reporting the resulting zero would be indistinguishable from
		// "nothing matched".
		_, _, known := after.Counts()
		test.False(t, known)
	})

	t.Run("searches by username prefix", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		for _, name := range []string{"ada", "adam", "brian", "a%wildcard"} {
			seedUser(t, env, store, newUser(name))
		}

		page, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "ad", nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, page.Data)

		// The escape is what keeps the typed % a character: without it the
		// pattern widens past the prefix and takes ada and adam with it, which
		// reads as a working search returning too much rather than as a bug.
		wildcard, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "a%", nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, wildcard.Data)
		test.EqOp(t, "a%wildcard", wildcard.Data[0].Username)
	})

	t.Run("pages a username search by the username", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		for _, name := range []string{"ada", "adam", "adele", "brian"} {
			seedUser(t, env, store, newUser(name))
		}

		// Archived after it was written, so what the search leaves out is a
		// user the prefix matched rather than one it never did.
		gone := seedUser(t, env, store, newUser("adrian"))
		must.NoError(t, env.archiveUser(t, store, testScope, gone.ID))

		first, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "ad",
			&filtering.QueryFilter{MaxResponseSize: pointer.To(uint16(2))})
		must.NoError(t, err)
		must.SliceLen(t, 2, first.Data)
		test.EqOp(t, "ada", first.Data[0].Username)
		test.EqOp(t, "adam", first.Data[1].Username)

		// The count is its own statement rather than one riding on the rows, so
		// it describes everything the prefix matched rather than what is left
		// after the cursor — and a partial page still reports it.
		_, total, known := first.Counts()
		must.True(t, known)
		test.EqOp(t, uint64(3), total)

		// The cursor is the username, because that is what the statement
		// ordered by. A cursor naming a position in an order the query does not
		// use is a page that skips rows and repeats others.
		test.EqOp(t, "adam", first.Cursor)

		second, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "ad",
			&filtering.QueryFilter{MaxResponseSize: pointer.To(uint16(2)), Cursor: pointer.To(first.Cursor)})
		must.NoError(t, err)
		must.SliceLen(t, 1, second.Data)
		test.EqOp(t, "adele", second.Data[0].Username)

		_, totalAgain, known := second.Counts()
		must.True(t, known)
		test.EqOp(t, total, totalAgain)
	})

	t.Run("reads a batch by ID", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		ada := seedUser(t, env, store, newUser("ada"))
		brian := seedUser(t, env, store, newUser("brian"))

		neighbor := newUser("eve")
		neighbor.Scope = otherScope
		seedUser(t, env, store, neighbor)

		users, err := store.ListUsersByIDs(t.Context(), env.reader(), testScope,
			[]string{ada.ID, brian.ID, neighbor.ID, identifiers.New()})
		must.NoError(t, err)

		// The neighbor and the unknown ID are skipped rather than failing the
		// batch: the caller is hydrating references, and one missing author
		// should not empty the page.
		must.SliceLen(t, 2, users)

		for _, user := range users {
			test.EqOp(t, "", user.HashedPassword)
		}

		empty, err := store.ListUsersByIDs(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, empty)
	})

	t.Run("a batch read hydrates references to archived users", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		ada := seedUser(t, env, store, newUser("ada"))
		gone := seedUser(t, env, store, newUser("brian"))

		must.NoError(t, env.archiveUser(t, store, testScope, gone.ID))

		// The batch read is what a caller hydrates "created by" references
		// through, so a soft-deleted author comes back rather than being
		// silently dropped — which would turn "created by a departed
		// colleague" into "created by nobody". Every other read of a user
		// excludes them, so this is the one place the statement is rendered
		// from a column list without archived_at in it.
		users, err := store.ListUsersByIDs(t.Context(), env.reader(), testScope, []string{ada.ID, gone.ID})
		must.NoError(t, err)
		must.SliceLen(t, 2, users)

		byID := map[string]*User{}
		for _, user := range users {
			byID[user.ID] = user
		}

		must.MapContainsKey(t, byID, gone.ID)
		test.NotNil(t, byID[gone.ID].ArchivedAt)
		test.Nil(t, byID[ada.ID].ArchivedAt)
	})

	t.Run("hides an account from another directory", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		_, err := store.GetAccount(t.Context(), env.reader(), otherScope, account.ID)
		must.ErrorIs(t, err, ErrAccountNotFound)
	})

	t.Run("pages accounts and a user's own", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		member := seedUser(t, env, store, newUser("brian"))

		first := seedAccountFor(t, env, store, owner, "First")
		second := seedAccountFor(t, env, store, owner, "Second")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			return store.CreateMembership(t.Context(), tx, testScope, &Membership{
				BelongsToUser:    member.ID,
				BelongsToAccount: second.ID,
				Roles:            []string{"account_member"},
			})
		}))

		all, err := store.ListAccounts(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, all.Data)

		mine, err := store.ListAccountsForUser(t.Context(), env.reader(), testScope, member.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, mine.Data)
		test.EqOp(t, second.ID, mine.Data[0].ID)

		theirs, err := store.ListAccountsForUser(t.Context(), env.reader(), testScope, owner.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, theirs.Data)

		_ = first

		page, err := store.ListAccounts(t.Context(), env.reader(), testScope, &filtering.QueryFilter{
			MaxResponseSize: pointer.To(uint16(1)),
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
	})

	t.Run("pages a roster with redacted users", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme")

		for _, name := range []string{"brian", "carol", "dennis"} {
			seedUserInto(t, env, store, newUser(name), account.ID)
		}

		roster, err := store.ListAccountMembers(t.Context(), env.reader(), testScope, account.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 4, roster.Data)

		for _, member := range roster.Data {
			must.NotNil(t, member.User)
			test.EqOp(t, "", member.User.HashedPassword)
			test.EqOp(t, "", member.User.TwoFactorSecret)
			test.SliceNotEmpty(t, member.Roles)
			test.EqOp(t, member.BelongsToUser, member.User.ID)
		}
	})
}
