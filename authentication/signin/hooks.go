package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/database"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/tenancy"
)

// FailedSignIn is a sign-in that proved nothing, as much of it as this package
// is willing to hand on.
//
// It carries no password, and it never will. What it carries is what a lockout
// counter, a rate limiter and an audit trail each need: the handle that was
// tried, the user it named if it named one, why the attempt was refused, and
// which door it was.
type FailedSignIn struct {
	_ struct{} `json:"-"`

	// Reason is the sentinel the caller is about to be given —
	// ErrInvalidCredentials for the four collapsed refusals, and one of the
	// status or second-factor sentinels otherwise.
	//
	// It is the reason as this package knows it, which is more than the caller
	// is told: a hook can tell a wrong password from an unknown handle, and the
	// response cannot. That asymmetry is the point of recording it here.
	Reason error `json:"-"`

	// Handle is what the attempt named — the username or the email address, as
	// submitted. It is the key a lockout counter counts against, and it is
	// present even when it names nobody, which is the case such a counter most
	// needs to see.
	Handle string `json:"handle"`

	// UserID is who the handle resolved to, or empty when it resolved to
	// nobody.
	UserID string `json:"userID"`

	// Administrative reports whether this was AdminLoginForToken rather than
	// LoginForToken. A failed administrative sign-in is a different event from a
	// failed ordinary one and usually wants a different alert.
	Administrative bool `json:"administrative"`
}

// Authentication is a proven credential and nothing more: who proved it, and
// which door they came through.
//
// It is the event all four doors share. [Service.Authenticate] produces one and
// stops; [Service.LoginForToken] produces one and then mints a token for it. So
// this carries no token, no "jti" and no expiry — those belong to the second
// event, and they are on [SignIn].
//
// Administrative is a field rather than something a hook infers from the
// principal, for the reason FailedSignIn.Administrative already gives: an
// administrative authentication is a different event from an ordinary one and
// usually wants a different alert. Nothing on a Principal says which door it
// came through.
//
// It is a struct rather than two arguments so that a fact added here later is
// additive for an implementer, which a parameter would not be.
type Authentication struct {
	_ struct{} `json:"-"`

	// Principal is who authenticated — the user, redacted, their memberships,
	// and the account this was resolved against.
	//
	// It is the same value SignIn.Principal carries, and it is here for the same
	// reason: a consumer recording who signed in should not have to read them
	// again.
	Principal *identity.Principal `json:"principal"`

	// Administrative reports whether this came through the administrative door —
	// AdminAuthenticate or AdminLoginForToken — rather than the ordinary one.
	Administrative bool `json:"administrative"`
}

// Hooks is what a consumer commits alongside a sign-in, inside the transaction
// the operation opens.
//
// The transaction is the whole point, and it is the same bargain
// identity.Hooks makes: a consumer's record of what happened and the thing that
// happened are one fact, so a hook writes on the operation's database.Tx and
// returning an error rolls the operation back. For a sign-in that means no
// token is returned — which is the correct direction, because a service that
// cannot record a sign-in should not be issuing one.
//
// # The two events, and the one transaction
//
// Proving a password and issuing a credential are two events, and this
// interface used to have one hook for both. AfterAuthenticate is the first and
// runs for all four doors; AfterIssueToken is the second and runs only where a
// token was actually minted.
//
// A door that mints runs both, in that order, in one transaction — so a
// consumer's two rows are one commit and the second may reference the first.
// Two transactions would be worse than untidy: the first could commit while the
// second rolled back, leaving an authentication with no token recorded against
// it, which is exactly what the token-less door legitimately writes. Telling
// those two apart is the whole reason the hook was split, so the split must not
// be what makes them indistinguishable.
//
// What that costs is worth stating: a hook runs with a write transaction held
// open, on the path a person is waiting on. Work that is slow, that talks to a
// network, or that can fail for reasons the sign-in should survive belongs
// behind an outbox row the hook writes. Sending the "new sign-in from a new
// device" email from AfterAuthenticate makes sign-in fail when the mail
// provider is down.
//
// Nothing expensive happens inside that transaction otherwise. The password
// hash comparison, the second-factor check and the token minting are all done
// by the time a hook is called — see the package documentation.
//
// Every method is "After": the operation has already decided by the time one
// runs, and none of them is a veto on policy. A hook is where "record this"
// goes and not where "allow this" goes; rate limiting, lockout and IP
// reputation are decisions a consumer makes in front of this service, with what
// AfterFailedSignIn gave them last time.
//
// It is one interface rather than a function type per operation so that a
// consumer's audit layer is one type. Embed NoopHooks and override what
// matters; a method added here later then does not break the embedder.
type Hooks interface {
	// AfterAuthenticate is called with a proven credential, inside the
	// transaction the operation opened.
	//
	// It runs for every door — Authenticate, AdminAuthenticate, LoginForToken
	// and AdminLoginForToken — which is what makes it the hook an access log
	// hangs off. "Somebody proved a password" is the event that happened; "a
	// token came out" is a second one, and a log that recorded only the second
	// would not show the authorization server's login step at all.
	//
	// It sees no credential, which is what makes it the safe one to record from.
	//
	// An error rolls back whichever operation called it: an Authenticate caller
	// is refused, and a LoginForToken caller is given no token.
	AfterAuthenticate(ctx context.Context, tx database.Tx, scope tenancy.Scope, auth *Authentication) error

	// AfterIssueToken is called with the completed sign-in, its token already
	// minted, in the same transaction as the AfterAuthenticate that ran just
	// before it.
	//
	// The SignIn is the value the caller is about to receive, token included —
	// this is the one hook in the module that sees a live credential, because
	// recording that a token with a given ID was issued is exactly what a
	// consumer revoking one later needs. Record SignIn.TokenID, not
	// SignIn.Token.
	//
	// It does not run for Authenticate, and that is not an omission: there is no
	// token there. A revocation list, a session table or a "jti" index that
	// gained a row for a credential nobody holds would gain a row that can only
	// age out.
	AfterIssueToken(ctx context.Context, tx database.Tx, scope tenancy.Scope, signIn *SignIn) error

	// AfterFailedSignIn is called with an attempt that proved nothing, inside a
	// transaction of its own.
	//
	// It runs for the credential refusals — an unknown handle, a wrong
	// password, a missing or wrong code, a status that admits no sign-in, an
	// administrative door the caller is not admitted through. It does not run
	// for a request this package refused before it looked anything up (an empty
	// form) or for a failure after the credentials were accepted (a directory
	// that would not answer), because neither is an attempt at somebody's
	// account.
	//
	// An error from it does not rescue the sign-in, which was already refused.
	// It is joined to the refusal, so errors.Is against the sentinel still
	// matches and the consumer's failure is not silently dropped.
	AfterFailedSignIn(ctx context.Context, tx database.Tx, scope tenancy.Scope, attempt *FailedSignIn) error

	// AfterUpdatePassword is called with the user whose password changed, as
	// they stood before the write and redacted, in the transaction that wrote
	// the new hash.
	//
	// Before rather than after, because the only column the write moves is one a
	// redacted user does not carry. What a consumer records here is that the
	// credential moved and when; the value it moved to is not theirs to keep.
	AfterUpdatePassword(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error

	// AfterRefreshTOTPSecret is called with the user who was issued a new
	// second-factor secret, redacted, in the transaction that stored it.
	//
	// The secret is deliberately not here. It is in flight to exactly one
	// person, and a hook that recorded it would put a live second factor in
	// whatever the hook writes to.
	//
	// The secret it replaced is gone and the new one is unproven, so between
	// this and AfterVerifyTOTPSecret the user holds no second factor at all —
	// which is the window a consumer alerting on "second factor removed" cares
	// about.
	AfterRefreshTOTPSecret(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error

	// AfterVerifyTOTPSecret is called with the user who proved possession of the
	// secret they hold, redacted, in the transaction that marked it verified.
	AfterVerifyTOTPSecret(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *identity.User) error
}

// NoopHooks is the Hooks a service runs when a consumer configures none, and the
// type to embed in one that overrides some.
//
// Embedding it rather than implementing the interface is what makes a method
// added here later additive: an embedder gains a no-op rather than a compile
// failure.
type NoopHooks struct{}

var _ Hooks = NoopHooks{}

// AfterAuthenticate does nothing.
func (NoopHooks) AfterAuthenticate(context.Context, database.Tx, tenancy.Scope, *Authentication) error {
	return nil
}

// AfterIssueToken does nothing.
func (NoopHooks) AfterIssueToken(context.Context, database.Tx, tenancy.Scope, *SignIn) error {
	return nil
}

// AfterFailedSignIn does nothing.
func (NoopHooks) AfterFailedSignIn(context.Context, database.Tx, tenancy.Scope, *FailedSignIn) error {
	return nil
}

// AfterUpdatePassword does nothing.
func (NoopHooks) AfterUpdatePassword(context.Context, database.Tx, tenancy.Scope, *identity.User) error {
	return nil
}

// AfterRefreshTOTPSecret does nothing.
func (NoopHooks) AfterRefreshTOTPSecret(context.Context, database.Tx, tenancy.Scope, *identity.User) error {
	return nil
}

// AfterVerifyTOTPSecret does nothing.
func (NoopHooks) AfterVerifyTOTPSecret(context.Context, database.Tx, tenancy.Scope, *identity.User) error {
	return nil
}
