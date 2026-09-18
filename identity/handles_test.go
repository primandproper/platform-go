package identity

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runHandleFoldingSuite covers the one spelling a username and an email address
// are stored, compared and looked up in.
//
// It is a suite of its own rather than a case in each of the five interfaces it
// touches, because the property it pins is the one thing none of them can state
// alone: that the answer is the same on all three dialects. MySQL's default
// collation folds these columns and the other two compare bytes, so before the
// fold every case below passed on one server and failed on another — which is
// why it runs inside runStoreSuite rather than beside it.
func runHandleFoldingSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a registration is stored in one spelling", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mixed := newUser("ada")
		mixed.Username = "Ada"
		mixed.EmailAddress = "Ada@Example.com"

		registered := seedUser(t, env, store, mixed)

		// The row answers with what the columns hold: the folded handle, the
		// spelling the registration submitted beside it, and an address with
		// no display companion at all.
		test.EqOp(t, "ada", registered.Username)
		test.EqOp(t, "Ada", registered.UsernameDisplay)
		test.EqOp(t, "ada@example.com", registered.EmailAddress)

		read, err := store.GetUser(t.Context(), env.reader(), testScope, registered.ID)
		must.NoError(t, err)
		test.EqOp(t, "ada", read.Username)
		test.EqOp(t, "Ada", read.UsernameDisplay)
		test.EqOp(t, "ada@example.com", read.EmailAddress)

		// And every read of a user carries it, not just the keyed one: the
		// page projection is where a field added to the row and forgotten in
		// one of the nine converters would show.
		page, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "ada", nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, "Ada", page.Data[0].UsernameDisplay)
	})

	t.Run("a display spelling naming another handle is refused", func(t *testing.T) {
		t.Parallel()

		// The two are one handle in two cases, so a display that folds to some
		// other handle is a caller writing back a value they did not finish
		// changing — refused rather than corrected, as a mismatched scope is.
		store := env.newStore(t)

		wrong := newUser("ada")
		wrong.UsernameDisplay = "Grace"
		must.ErrorIs(t, env.createUserErr(t, store, testScope, wrong), ErrUsernameDisplayMismatch)

		ada := seedUser(t, env, store, newUser("ada"))

		stale := *ada
		stale.Username = "grace"
		must.ErrorIs(t, env.updateUserErr(t, store, testScope, &stale), ErrUsernameDisplayMismatch)
	})

	t.Run("a profile save moves the spelling without moving the handle", func(t *testing.T) {
		t.Parallel()

		// What the display column is for: the person capitalised their own
		// name, and the directory is keyed on the handle it was keyed on
		// before. The service is the caller here because ProfileUpdate carries
		// one username field for the two columns, and which of them a save
		// counts as a change is its reading.
		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")
		test.EqOp(t, "ada", registration.User.UsernameDisplay)

		saved, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{Username: pointer.To("Ada")})
		must.NoError(t, err)
		test.EqOp(t, "ada", saved.Username)
		test.EqOp(t, "Ada", saved.UsernameDisplay)
		test.Eq(t, []string{"username"}, hooks.changed)

		// Submitted again unedited, the same form writes nothing: the
		// comparison behind "which fields moved" folds the handle and compares
		// the spelling as it stands.
		again, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{Username: pointer.To("Ada")})
		must.NoError(t, err)
		test.EqOp(t, "Ada", again.UsernameDisplay)
		test.EqOp(t, 1, hooks.ran("profile"))
	})

	t.Run("a handle differing only in case is taken", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		byUsername := newUser("ada")
		byUsername.Username = "ADA"
		byUsername.EmailAddress = "different@example.com"
		must.ErrorIs(t, env.createUserErr(t, store, testScope, byUsername), ErrUsernameTaken)

		byEmail := newUser("grace")
		byEmail.EmailAddress = "Ada@Example.com"
		must.ErrorIs(t, env.createUserErr(t, store, testScope, byEmail), ErrEmailAddressTaken)
	})

	t.Run("a handle differing by an accent is a different handle", func(t *testing.T) {
		t.Parallel()

		// The other half of "one directory, one answer", and the half a Go fold
		// cannot reach. FoldHandle lowers case; it does not strip accents, and
		// deliberately — renee and renée are two people, and a fold that
		// collapsed them would hand one of them the other's account on all
		// three dialects rather than on one.
		//
		// So the convergence goes the other way: the column is collated
		// utf8mb4_bin on MySQL, which is the byte-exact comparison Postgres and
		// SQLite already do. Without it, MariaDB's default is accent-insensitive
		// and these two registrations are one taken handle there and two users
		// everywhere else — the same divergence the fold exists to remove,
		// arrived at through the one door the fold does not close.
		store := env.newStore(t)
		seedUser(t, env, store, newUser("renee"))

		accented := newUser("renee")
		accented.Username = "renée"
		accented.EmailAddress = "renée@example.com"

		second := seedUser(t, env, store, accented)
		test.EqOp(t, "renée", second.Username)

		// And each handle reaches its own user rather than whichever row the
		// server's collation decided was close enough.
		plain, err := store.GetUserByUsername(t.Context(), env.reader(), testScope, "Renee")
		must.NoError(t, err)
		test.EqOp(t, "renee", plain.Username)

		withAccent, err := store.GetUserByUsername(t.Context(), env.reader(), testScope, "RENÉE")
		must.NoError(t, err)
		test.EqOp(t, "renée", withAccent.Username)
		test.NotEqOp(t, plain.ID, withAccent.ID)
	})

	t.Run("the sign-in reads find a user by any casing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		ada := seedUser(t, env, store, newUser("ada"))

		byUsername, err := store.GetUserByUsername(t.Context(), env.reader(), testScope, "AdA")
		must.NoError(t, err)
		test.EqOp(t, ada.ID, byUsername.ID)

		byEmail, err := store.GetUserByEmailAddress(t.Context(), env.reader(), testScope, "ADA@EXAMPLE.COM")
		must.NoError(t, err)
		test.EqOp(t, ada.ID, byEmail.ID)
	})

	t.Run("a registration in one casing is reached by another", func(t *testing.T) {
		t.Parallel()

		// The acceptance the issue behind this suite asked for, in one
		// sequence: register mixed, sign in lower, and have the directory
		// agree with itself whichever server is underneath.
		store := env.newStore(t)

		mixed := newUser("grace")
		mixed.Username = "Grace"
		mixed.EmailAddress = "Grace@Example.com"
		registered := seedUser(t, env, store, mixed)

		byUsername, err := store.GetUserByUsername(t.Context(), env.reader(), testScope, "grace")
		must.NoError(t, err)
		test.EqOp(t, registered.ID, byUsername.ID)

		byEmail, err := store.GetUserByEmailAddress(t.Context(), env.reader(), testScope, "grace@example.com")
		must.NoError(t, err)
		test.EqOp(t, registered.ID, byEmail.ID)
	})

	t.Run("re-casing a handle is not a change to it", func(t *testing.T) {
		t.Parallel()

		// Not a collision with the row's own value, and not an address move —
		// so the proof the address carried survives a profile save that only
		// shouted it.
		store := env.newStore(t)
		ada := seedUser(t, env, store, newUser("ada"))

		must.NoError(t, env.setUserEmailAddressVerificationToken(t, store, testScope, ada.ID, "tok-fold"))
		must.NoError(t, env.markUserEmailAddressVerified(t, store, testScope, ada.ID, "tok-fold"))

		verified, err := store.GetUser(t.Context(), env.reader(), testScope, ada.ID)
		must.NoError(t, err)
		must.NotNil(t, verified.EmailAddressVerifiedAt)

		shouted := *verified
		shouted.Username = "ADA"
		shouted.EmailAddress = "ADA@EXAMPLE.COM"

		saved, err := env.updateUser(t, store, testScope, &shouted)
		must.NoError(t, err)
		test.EqOp(t, "ada", saved.Username)
		test.EqOp(t, "ada@example.com", saved.EmailAddress)
		test.NotNil(t, saved.EmailAddressVerifiedAt)

		// The display column is the field's and not the Username argument's,
		// so this caller — who shouted the handle and left the spelling alone
		// — re-spelled nothing. A store caller who means to move it says so.
		test.EqOp(t, "ada", saved.UsernameDisplay)

		respelled := *saved
		respelled.UsernameDisplay = "ADA"

		shown, err := env.updateUser(t, store, testScope, &respelled)
		must.NoError(t, err)
		test.EqOp(t, "ada", shown.Username)
		test.EqOp(t, "ADA", shown.UsernameDisplay)
	})

	t.Run("a profile save cannot take another user's handle by re-casing it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))
		grace := seedUser(t, env, store, newUser("grace"))

		byUsername := *grace
		byUsername.Username = "Ada"
		byUsername.UsernameDisplay = "Ada"
		must.ErrorIs(t, env.updateUserErr(t, store, testScope, &byUsername), ErrUsernameTaken)

		byEmail := *grace
		byEmail.EmailAddress = "ADA@example.com"
		must.ErrorIs(t, env.updateUserErr(t, store, testScope, &byEmail), ErrEmailAddressTaken)
	})

	t.Run("the username search folds its prefix", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		page, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "AD", nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, "ada", page.Data[0].Username)
	})

	t.Run("an invitation is addressed in one spelling", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		owner := seedUser(t, env, store, newUser("ada"))
		account := seedAccountFor(t, env, store, owner, "Acme", "account_admin")

		sent, err := env.createInvitation(t, store, testScope,
			newInvitation(owner, account.ID, "Brian@Example.com", "tok-fold", baseTime.Add(time.Hour)))
		must.NoError(t, err)
		test.EqOp(t, "brian@example.com", sent.ToEmail)

		// Reachable by the spelling the sender used and by the one the
		// recipient will register with, which are the same row.
		for _, address := range []string{"Brian@Example.com", "brian@example.com", "BRIAN@EXAMPLE.COM"} {
			received, listErr := store.ListInvitationsForEmailAddress(
				t.Context(), env.reader(), testScope, address, InvitationPending, nil)
			must.NoError(t, listErr, must.Sprintf("listing for %q", address))
			must.SliceLen(t, 1, received.Data)
			test.EqOp(t, sent.ID, received.Data[0].ID)
		}
	})
}

// TestFoldHandle pins the fold itself, which is the half of the behavior above
// that does not need a database: the same input renders the same way here
// whatever is underneath, which is the whole reason it is not a collation.
func TestFoldHandle(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		handle string
		folded string
	}{
		"already folded":   {handle: "ada", folded: "ada"},
		"a shouted handle": {handle: "ADA", folded: "ada"},
		"a mixed address":  {handle: "Ada@Example.com", folded: "ada@example.com"},
		"empty":            {handle: "", folded: ""},
		"non-ASCII":        {handle: "ÅDA@example.com", folded: "åda@example.com"},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.folded, FoldHandle(tc.handle))
		})
	}
}
