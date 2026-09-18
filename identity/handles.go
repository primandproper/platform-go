package identity

import (
	"strings"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// FoldHandle renders a username or an email address as the one spelling this
// directory stores it under and every lookup binds.
//
// It exists because that spelling was otherwise the dialect's to choose. MySQL
// renders these columns as VARCHAR under the server's default collation, which
// compares case-insensitively, so UNIQUE (scope, username) refused a second
// "Ada" beside "ada" and WHERE email_address = ? signed in the registration the
// form had not spelled. Postgres and SQLite compare TEXT byte for byte and did
// neither: there, Ada@example.com and ada@example.com are two people. One
// directory's worth of Go, three answers to "is this handle taken" — which is
// the one thing the three-dialect promise says cannot happen.
//
// So the fold happens here, where Go performs it identically whatever is
// underneath, and the column holds the folded spelling alone. What MySQL's
// collation folds afterwards is a value already folded, so it changes nothing;
// what Postgres and SQLite compare byte for byte was folded before it was
// bound. The collation stops being load-bearing rather than being argued with,
// which is also what makes the property survive a server whose default
// collation is changed underneath a running directory.
//
// It is exported because the fold is not this package's private business: a
// caller that looks a handle up through some other index, and every flow in
// authentication/signin that decides whether two handles are the same one,
// has to fold the way the column was folded. This is the "extract what can be
// got wrong twice" case exactly — a second copy of a normalisation is a copy
// that can disagree with the rows.
//
// The spelling a person submitted is not lost: User.UsernameDisplay keeps it,
// beside the handle rather than instead of it. An email address gets no such
// companion — see that field.
func FoldHandle(handle string) string { return strings.ToLower(handle) }

// foldUserHandles settles the three handle columns a user write assigns: the
// folded username the directory is keyed on, the spelling it is shown in, and
// the folded address.
//
// The display spelling is the caller's, and the two readings are the ones
// adoptScope already takes of a scope: one that names nothing adopts what the
// write submitted, and one that names a different handle is refused rather than
// corrected. There is no third reading, because a display that folds to some
// other handle is not a spelling of this one — it is the handle from before a
// rename, written back by a caller who changed one field of a value they read.
func foldUserHandles(u *User) error {
	display := u.UsernameDisplay
	if display == "" {
		display = u.Username
	}

	u.Username = FoldHandle(u.Username)
	u.EmailAddress = FoldHandle(u.EmailAddress)

	if FoldHandle(display) != u.Username {
		return platformerrors.Wrapf(ErrUsernameDisplayMismatch,
			"display %q is not a spelling of username %q", display, u.Username)
	}

	u.UsernameDisplay = display

	return nil
}

// displayedUsername is what a read hands back in User.UsernameDisplay: the
// stored spelling, or the folded handle where a row has none.
//
// A row has none only if it was written before the directory had the column, so
// the fallback is what keeps this field's "never empty on a read" promise true
// of a consumer who has not backfilled — and a folded handle is a correct
// spelling of the handle, merely not the one anybody typed.
func displayedUsername(username, display string) string {
	if display == "" {
		return username
	}

	return display
}
