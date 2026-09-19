package identity

import (
	"strings"
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
		// pre-fold spelling adopted as the display name beside it, and an
		// address with no companion at all.
		test.EqOp(t, "ada", registered.Username)
		test.EqOp(t, "Ada", registered.DisplayName)
		test.EqOp(t, "ada@example.com", registered.EmailAddress)

		read, err := store.GetUser(t.Context(), env.reader(), testScope, registered.ID)
		must.NoError(t, err)
		test.EqOp(t, "ada", read.Username)
		test.EqOp(t, "Ada", read.DisplayName)
		test.EqOp(t, "ada@example.com", read.EmailAddress)

		// And every read of a user carries it, not just the keyed one: the
		// page projection is where a field added to the row and forgotten in
		// one of the nine converters would show.
		page, err := store.SearchUsersByUsername(t.Context(), env.reader(), testScope, "ada", nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, "Ada", page.Data[0].DisplayName)
	})

	t.Run("a display name is not a spelling of the handle", func(t *testing.T) {
		t.Parallel()

		// The whole of the column's rule, which is that it has none. A name is
		// decoration rather than a credential: nothing is keyed on it, nothing
		// is looked up by it, and nothing compares it — so the accented name a
		// person actually answers to, a word their handle does not contain,
		// and a name with no letters in it at all are three display names
		// rather than three mistakes.
		store := env.newStore(t)

		// Registered under one of them, and the handle is renee throughout:
		// what changes below is what the person is shown as, and it is never a
		// spelling of what they sign in with.
		renee := newUser("renee")
		renee.DisplayName = "Renée"

		written := seedUser(t, env, store, renee)
		test.EqOp(t, "renee", written.Username)
		test.EqOp(t, "Renée", written.DisplayName)

		for _, name := range []string{"Fart", "🎉🎉", "Renée"} {
			renamed := *written
			renamed.DisplayName = name

			saved, err := env.updateUser(t, store, testScope, &renamed)
			must.NoError(t, err, must.Sprintf("display name %q", name))
			test.EqOp(t, "renee", saved.Username)
			test.EqOp(t, name, saved.DisplayName)

			read, err := store.GetUser(t.Context(), env.reader(), testScope, written.ID)
			must.NoError(t, err)
			test.EqOp(t, name, read.DisplayName)
		}
	})

	t.Run("a display name survives a rename of the handle", func(t *testing.T) {
		t.Parallel()

		// The argument the old invariant made for itself, inverted. A display
		// spelling went stale on a rename only because it was defined as a
		// spelling of the handle; a name was never one, so renee becoming
		// renee2 leaves Renée being shown.
		store := env.newStore(t)

		user := newUser("renee")
		user.DisplayName = "Renée"
		written := seedUser(t, env, store, user)

		renamed := *written
		renamed.Username = "renee2"

		saved, err := env.updateUser(t, store, testScope, &renamed)
		must.NoError(t, err)
		test.EqOp(t, "renee2", saved.Username)
		test.EqOp(t, "Renée", saved.DisplayName)
	})

	t.Run("a display name is refused only for being too long", func(t *testing.T) {
		t.Parallel()

		// The one rule, and it is the column's width rather than anything
		// about names: MySQL truncates a value too long for its column where
		// the other two store it whole, so a bound checked in Go is what keeps
		// the three dialects answering alike. See MaxDisplayNameLength.
		store := env.newStore(t)

		tooLong := newUser("ada")
		tooLong.DisplayName = strings.Repeat("a", MaxDisplayNameLength+1)
		must.ErrorIs(t, env.createUserErr(t, store, testScope, tooLong), ErrDisplayNameTooLong)

		atTheLimit := newUser("grace")
		atTheLimit.DisplayName = strings.Repeat("g", MaxDisplayNameLength)

		written := seedUser(t, env, store, atTheLimit)
		test.EqOp(t, atTheLimit.DisplayName, written.DisplayName)

		overOnSave := *written
		overOnSave.DisplayName += "g"
		must.ErrorIs(t, env.updateUserErr(t, store, testScope, &overOnSave), ErrDisplayNameTooLong)
	})

	t.Run("a profile save moves the handle and leaves the name alone", func(t *testing.T) {
		t.Parallel()

		// ProfileUpdate carries a handle and not a name. What the form submits
		// is a spelling of the username, the column receives its fold, and the
		// display name is the person's own — so re-capitalising a handle is no
		// longer a change to anything, and a real rename moves one column.
		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")
		test.EqOp(t, "ada", registration.User.DisplayName)

		saved, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{Username: pointer.To("Ada")})
		must.NoError(t, err)
		test.EqOp(t, "ada", saved.Username)
		test.EqOp(t, "ada", saved.DisplayName)
		test.SliceEmpty(t, hooks.changed)
		test.EqOp(t, 0, hooks.ran("profile"))

		renamed, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{Username: pointer.To("Ada2")})
		must.NoError(t, err)
		test.EqOp(t, "ada2", renamed.Username)
		test.EqOp(t, "ada", renamed.DisplayName)
		test.Eq(t, []string{"username"}, hooks.changed)
	})

	t.Run("a profile save moves the name on its own", func(t *testing.T) {
		t.Parallel()

		// The other half of the same form, and the reason the two fields are
		// two: the person is shown differently without their handle moving,
		// and the handle moves without changing what they are shown as. A
		// request naming both moves both, which is a request that said so
		// rather than one field doing duty for two.
		hooks := &recordingHooks{}
		service, _ := env.newService(t, hooks)

		registration := registerAda(t, service, "ada")

		shown, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{DisplayName: pointer.To("Renée")})
		must.NoError(t, err)
		test.EqOp(t, "ada", shown.Username)
		test.EqOp(t, "Renée", shown.DisplayName)
		test.Eq(t, []string{"displayName"}, hooks.changed)

		// Unedited, it writes nothing: the comparison is on the name as it
		// stands, because nothing folds this column. The hook not running
		// again is the assertion, rather than the change set being empty —
		// nothing clears that between saves, so a save that writes nothing
		// leaves the previous one's list where it was.
		test.EqOp(t, 1, hooks.ran("profile"))

		again, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{DisplayName: pointer.To("Renée")})
		must.NoError(t, err)
		test.EqOp(t, "Renée", again.DisplayName)
		test.EqOp(t, 1, hooks.ran("profile"))

		both, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{Username: pointer.To("Renee2"), DisplayName: pointer.To("Renée H.")})
		must.NoError(t, err)
		test.EqOp(t, "renee2", both.Username)
		test.EqOp(t, "Renée H.", both.DisplayName)
		test.Eq(t, []string{"username", "displayName"}, hooks.changed)
	})

	t.Run("a cleared display name reads back as the handle", func(t *testing.T) {
		t.Parallel()

		// Present-and-empty clears, as it does for the names beside it — but
		// this column's read is never blank, so what a cleared one comes back
		// as is the handle. That is the write adopting it, which is the same
		// branch a registration naming no name takes.
		service, _ := env.newService(t, &recordingHooks{})

		registration := registerAda(t, service, "ada")

		named, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{DisplayName: pointer.To("Renée")})
		must.NoError(t, err)
		test.EqOp(t, "Renée", named.DisplayName)

		cleared, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{DisplayName: pointer.To("")})
		must.NoError(t, err)
		test.EqOp(t, "ada", cleared.DisplayName)
		test.EqOp(t, "ada", cleared.Username)
	})

	t.Run("a display name over the bound is refused through the service too", func(t *testing.T) {
		t.Parallel()

		// The bound lives on the write rather than on this type, so the
		// refusal a store caller gets is the refusal a form gets — one sentinel
		// for the one rule, whichever door the name arrived through.
		service, _ := env.newService(t, &recordingHooks{})

		registration := registerAda(t, service, "ada")

		_, err := service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{DisplayName: pointer.To(strings.Repeat("r", MaxDisplayNameLength+1))})
		must.ErrorIs(t, err, ErrDisplayNameTooLong)
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

	t.Run("a padded handle is refused rather than stored or collided with", func(t *testing.T) {
		t.Parallel()

		// The third thing a collation decides, and the one the fold cannot
		// reach. MariaDB's utf8mb4_bin is still PAD SPACE, so the second
		// registration below is a collision there — ErrUsernameTaken — and a
		// second user on Postgres and SQLite, which is exactly the "one
		// directory, three answers" this suite exists to rule out. Remove
		// checkUsernameWhitespace and this case fails two different ways
		// depending on which server is underneath, which is the point.
		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))

		for _, username := range []string{"ada ", "ada  ", " ada", "\tada", "ada\u00a0"} {
			padded := newUser("padded")
			padded.Username = username

			must.ErrorIs(t, env.createUserErr(t, store, testScope, padded), ErrUsernameWhitespace,
				must.Sprintf("registering %q", username))
		}

		// And the refusal is the write's, not the collision's: a padded handle
		// nobody else holds is refused for being padded rather than accepted
		// because it is free.
		unclaimed := newUser("grace")
		unclaimed.Username = "grace "
		must.ErrorIs(t, env.createUserErr(t, store, testScope, unclaimed), ErrUsernameWhitespace)

		// A profile save is held to the same rule, which is the whole of what
		// sharing validateProfile buys: a handle the directory would not
		// register is not one it will rename somebody to.
		grace := seedUser(t, env, store, newUser("grace"))

		renamed := *grace
		renamed.Username = " ada"
		must.ErrorIs(t, env.updateUserErr(t, store, testScope, &renamed), ErrUsernameWhitespace)

		// The row is untouched, so the refusal cost the user nothing.
		read, err := store.GetUser(t.Context(), env.reader(), testScope, grace.ID)
		must.NoError(t, err)
		test.EqOp(t, "grace", read.Username)
	})

	t.Run("a padded handle is refused through the service too", func(t *testing.T) {
		t.Parallel()

		// Both doors, through the one rule. A registration and a profile save
		// are the two paths a handle arrives by, and each reaches
		// validateProfile — so the sentinel a form gets is the sentinel a store
		// caller gets.
		service, _ := env.newService(t, &recordingHooks{})

		padded := newUser("ada")
		padded.Username = "ada "

		_, err := service.Register(t.Context(), testScope, padded, newAccount("Ada's account", ""),
			[]string{"account_admin"})
		must.ErrorIs(t, err, ErrUsernameWhitespace)

		registration := registerAda(t, service, "ada")

		_, err = service.UpdateProfile(t.Context(), testScope, registration.User.ID,
			&ProfileUpdate{Username: pointer.To("ada ")})
		must.ErrorIs(t, err, ErrUsernameWhitespace)
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
		// so this caller — who shouted the handle and left the name alone —
		// renamed nothing. A store caller who means to move it says so.
		test.EqOp(t, "ada", saved.DisplayName)

		renamed := *saved
		renamed.DisplayName = "ADA"

		shown, err := env.updateUser(t, store, testScope, &renamed)
		must.NoError(t, err)
		test.EqOp(t, "ada", shown.Username)
		test.EqOp(t, "ADA", shown.DisplayName)
	})

	t.Run("a profile save cannot take another user's handle by re-casing it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		seedUser(t, env, store, newUser("ada"))
		grace := seedUser(t, env, store, newUser("grace"))

		byUsername := *grace
		byUsername.Username = "Ada"
		byUsername.DisplayName = "Ada"
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

		// The fold lowers case and does nothing else — it does not trim, which
		// is why a padded handle is refused by a rule rather than corrected
		// here. A fold that trimmed would store a value nobody sent, and every
		// caller who folds for a lookup of their own would have to trim too.
		"padded": {handle: " ADA ", folded: " ada "},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.folded, FoldHandle(tc.handle))
		})
	}
}
