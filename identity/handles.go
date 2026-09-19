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
// The spelling a person submitted is not lost: a user who names no
// User.DisplayName adopts it there. That is where the column came from, and it
// is no longer what the column is for — a display name is free-form and the
// adoption is only its default. An email address gets no companion at all —
// see that field.
func FoldHandle(handle string) string { return strings.ToLower(handle) }

// checkUsernameWhitespace refuses a username that begins or ends with
// whitespace, which is the one spelling of a handle FoldHandle does not settle
// and the columns' collation answers differently per dialect.
//
// MariaDB's utf8mb4_bin — the collation these columns carry to settle the
// accent half — is still PAD SPACE, so "ada  " compares equal to "ada" there
// and UNIQUE (scope, username) refuses it, where Postgres and SQLite compare
// the trailing bytes and store a second user. The remaining divergence is
// therefore a registration that succeeds on one engine and collides on
// another, which is the failure the fold was written to remove.
//
// It refuses rather than trimming, and the asymmetry is the reason: a
// rejection is visible at the call site and a silent correction is not, and
// this package already refuses a write whose halves disagree twice over. It is
// also not a charset allowlist. An allowlist would close accents, homoglyphs
// and zero-width characters too, and would decide that "renée" is not a handle
// — a larger ruling about what a handle is, with its own argument to make.
//
// Whitespace is unicode.IsSpace's reading of it, through strings.TrimSpace, so
// a tab, a newline and a non-breaking space are each refused. Interior
// whitespace is untouched: "ada lovelace" is a handle this directory stores,
// and it is the same handle on all three dialects.
//
// Emptiness is validation.Required's to report, so that a missing username and
// a padded one do not both come back as the same refusal.
//
// It is a function rather than one of the validation rules beside
// emailAddressRule because ozzo's validation.Errors is a map with no Unwrap: a
// sentinel returned from a validation.By reaches a caller as an entry in that
// map rather than as something errors.Is can find, and a sentinel the mappers
// claim has to survive the trip to a transport.
func checkUsernameWhitespace(username string) error {
	if username == "" || strings.TrimSpace(username) == username {
		return nil
	}

	return platformerrors.Wrapf(ErrUsernameWhitespace, "username %q", username)
}

// foldUserHandles settles the three columns a user write does not store
// verbatim: the folded username the directory is keyed on, the folded address,
// and the display name, which is folded by nothing and may be adopted from the
// first.
//
// The display name is the caller's, and the only reading taken of it is the one
// adoptScope takes of a scope that names nothing: it adopts what the write
// submitted. It adopts the *pre-fold* spelling, which is the whole of the
// adoption's value — a registration as "Ada" is shown as "Ada" — and it adopts
// it only when the caller named none. A caller who named one is naming a
// display name rather than a spelling of the handle, so there is nothing here
// to agree or disagree with: "Renée" beside the handle renee is what this
// column is for.
//
// The bound is the one rule the column has, and it is checked here rather than
// in validateProfile because this is where the stored value is settled: the
// name a write stores may be one nobody named, and a rule upstream of the
// adoption would bound what a caller sent while leaving what the column
// receives unbounded. See MaxDisplayNameLength.
func foldUserHandles(u *User) error {
	display := u.DisplayName
	if display == "" {
		display = u.Username
	}

	if len(display) > MaxDisplayNameLength {
		return platformerrors.Wrapf(ErrDisplayNameTooLong,
			"display name is %d bytes, over the %d-byte limit", len(display), MaxDisplayNameLength)
	}

	u.Username = FoldHandle(u.Username)
	u.EmailAddress = FoldHandle(u.EmailAddress)
	u.DisplayName = display

	return nil
}

// displayedName is what a read hands back in User.DisplayName: the stored name,
// or the folded handle where a row has none.
//
// A row has none only if it was written before the directory had the column, so
// the fallback is what keeps this field's "never empty on a read" promise true
// of a consumer who has not backfilled — and the handle is a name the person
// answers to, merely not one they chose.
func displayedName(username, display string) string {
	if display == "" {
		return username
	}

	return display
}
