package phonecodes

import (
	"regexp"
	"time"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// MaxSubjectLength is the longest subject identifier this store accepts, in
// bytes. It is the MySQL column's width, enforced before a write reaches any
// server: strict mode refuses an over-long value there, and the other two
// engines would store it whole, so the same request would succeed on two
// dialects and fail on the third.
const MaxSubjectLength = 255

// e164 is a phone number in E.164: a plus sign and at most fifteen digits, the
// first of which is not zero.
//
// It is a shape check and nothing more. Whether the number exists, whether it
// takes texts and whether its country code is one a deployment serves are the
// SMS provider's to discover; what this store needs is that one person's
// number is spelled one way, so that the unique key on it means "one number".
var e164 = regexp.MustCompile(`^\+[1-9]\d{1,14}$`)

// IssueRequest is what a caller asks a code to be issued for.
type IssueRequest struct {
	_ struct{} `json:"-" yaml:"-"`

	// SubjectID is the consumer's own identifier for the person. It is opaque
	// here — the person is not a user, and this store reads no directory.
	SubjectID string

	// PhoneNumber is where the code is going, in E.164. It is stored as given,
	// so normalize it before issuing: a code issued to "+15555550100" is not
	// redeemable against "+1 555 555 0100", and the second is refused as a
	// malformed number before it gets that far.
	PhoneNumber string

	// MaxAttempts is how many wrong codes this one survives. Zero takes the
	// store's, which is DefaultMaxAttempts unless WithMaxAttempts said
	// otherwise. It may tighten the store's limit and never loosen it: a
	// negative count, or one above the store's, is ErrInvalidMaxAttempts.
	MaxAttempts int
}

// Code is one issued code as the store reads it back. It never carries the code
// itself or its digest.
type Code struct {
	_ struct{} `json:"-" yaml:"-"`

	// IssuedAt is when the code was issued.
	IssuedAt time.Time

	// ExpiresAt is when the code stops being redeemable.
	ExpiresAt time.Time

	// PurgeAfter is when the sweeper may remove the row.
	PurgeAfter time.Time

	// RedeemedAt is when the code was spent, and nil until it is.
	RedeemedAt *time.Time

	// RevokedAt is when the code was withdrawn with the rest of its subject's,
	// and nil until it is.
	RevokedAt *time.Time

	// ID names this issuance. Every issue mints a new one, so it changes when a
	// number's code is replaced even though the row's key does not.
	ID string

	// SubjectID is the consumer's identifier for the person.
	SubjectID string

	// PhoneNumber is where the code was texted.
	PhoneNumber string

	// Scope is whose directory the code was issued in.
	Scope tenancy.Scope

	// Attempts is how many wrong codes have been counted against this one.
	Attempts int

	// MaxAttempts is the count at which the code is dead.
	MaxAttempts int
}

// Issuance is what Store.Issue hands back: the stored code, the plaintext to
// text, and the code the issue replaced.
type Issuance struct {
	_ struct{} `json:"-" yaml:"-"`

	// Code is the row as written.
	Code *Code

	// Previous is the code this issue replaced — whatever the number held, in
	// any state — and nil when the number held none.
	//
	// It is the issue-rate brake, and the only one this package supplies. A
	// consumer that wants a cooldown reads Previous.IssuedAt and, when it is
	// too recent, returns an error out of the transaction instead of texting:
	// the rollback puts the previous code back as it was.
	Previous *Code

	// Plaintext is the code to text, and the only place it ever appears. The
	// store keeps a digest of it and cannot give it back.
	Plaintext string
}
