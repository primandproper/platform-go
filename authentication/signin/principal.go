package signin

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// IssueOption adjusts one call to [Service.IssueForPrincipal]: which
// credential its caller proved, how many factors it proved, and which door it
// comes through.
type IssueOption func(*issueRequest)

// issueRequest is what the options on one principal door resolve to.
type issueRequest struct {
	kind           CredentialKind
	totpCode       string
	administrative bool
	multiFactor    bool
}

// WithCredentialKind names the credential the caller proved, and the
// [Authentication] the hooks are handed carries that name as its
// CredentialKind rather than [CredentialKindPrincipal].
//
// The name is the consumer's to choose — a passkey sign-in is
// CredentialKind("passkey") without this package having heard of passkeys —
// and it is recorded as given. An empty one is [ErrEmptyCredentialKind], and it
// is refused rather than read as CredentialKindPrincipal: a caller with no name
// to give leaves this option off, so an empty kind here is a name that got lost
// on its way in, and a hook recording it as something else would be recording
// the loss.
func WithCredentialKind(kind CredentialKind) IssueOption {
	return func(r *issueRequest) {
		r.kind = kind
	}
}

// Administrative sends the call through the administrative door, which stands
// to the ordinary one as [Service.AdminLoginForToken] stands to
// [Service.LoginForToken]: the subject must hold one of the service roles
// [WithAdminServiceRoles] named, and the token and any refresh token carry the
// administrative lifetimes and [ClaimAdministrative].
//
// A service that named no administrative roles has no administrative door here
// either, and every call is [ErrAdminLoginDisabled]. Both refusals are recorded
// through [Hooks.AfterFailedSignIn] with Administrative set.
//
// The second factor [Service.AdminLoginForToken] insists on is insisted on
// here as a credential that was two factors on its own: without [MultiFactor]
// the call is [ErrMultiFactorRequired], a TOTP code or none. A passkey an
// authenticator did not verify the person for is a key that was present, and
// an operator's door is the one door where possession alone is not an answer.
func Administrative() IssueOption {
	return func(r *issueRequest) {
		r.administrative = true
	}
}

// MultiFactor says the credential the caller proved was two factors on its
// own, and waives the second-factor rule for it.
//
// A passkey asserted with user verification is the case it exists for: the
// device is something the person has and the PIN or biometric that unlocked
// it is something they know or are, so asking for a TOTP code as well would
// be asking the consumer's strongest credential to be the weakest one's
// companion. A passkey asserted without user verification is not that case —
// authentication/passkeys reports which one a login was, as
// Login.UserVerified — and neither is anything else that proved possession
// alone.
//
// It is a claim the caller makes rather than one this package checks, like
// everything else a principal door is handed, and it is off unless named:
// a caller that forgot it gets a second-factor prompt, where one that forgot
// the opposite would have signed somebody in on a key tap.
func MultiFactor() IssueOption {
	return func(r *issueRequest) {
		r.multiFactor = true
	}
}

// WithTOTPCode hands the door the second factor a single-factor credential is
// asked for: a TOTP code, or one of the user's recovery codes, exactly as
// [Credentials.TOTPCode] is read. It is ignored beside [MultiFactor].
func WithTOTPCode(code string) IssueOption {
	return func(r *issueRequest) {
		r.totpCode = code
	}
}

// IssueForPrincipal mints a sign-in for a subject another credential has
// already proven — a passkey assertion, a device grant — and proves nothing
// itself.
//
// It is [Service.LoginForToken] with the password taken out and nothing else:
// the same standing check, the same second-factor rule, the same principal read, the same claims, lifetimes and
// family, and the same transaction holding the refresh token,
// [Hooks.AfterAuthenticate] and [Hooks.AfterIssueToken] in that order. A
// consumer whose people sign in with a passkey gets the token a password
// sign-in gets rather than re-deriving one, and a copy of the mint that could
// drift from this one on the lifetimes or the claims is the thing this door
// exists to make unnecessary.
//
// # What the caller is vouching for
//
// Everything a credential would have proven. This method takes a user ID, not
// a credential, so whoever calls it has decided who is signing in: it belongs
// behind the consumer's own verification of the credential they accept, and
// never behind a transport a client can reach with an identifier of its
// choosing. That is why no transport in this module exposes it.
//
// # What it still decides
//
// Whether the subject may sign in at all. The user is read on the reader and a
// status that admits no sign-in is refused with the status sentinel
// [Service.LoginForToken] gives, recorded through [Hooks.AfterFailedSignIn]
// with the user's ID and no handle — a proven credential still does not sign
// in a suspended user, and a credential that proved somebody who may not sign
// in is the event that hook exists for. A user the directory does not hold is
// the directory's answer, passed through: it is a caller that proved somebody
// who does not exist, not an attempt at an account.
//
// The account the token is for is activeAccountID, resolved by the directory
// exactly as [Credentials.ActiveAccountID] is.
//
// And whether the credential needs a second factor beside it. The
// second-factor rule applies — the one [Service.LoginForToken] applies after a
// password, read from [WithTOTPCode] — unless the caller says with
// [MultiFactor] that the credential was two factors on its own, which a
// passkey is only when the authenticator verified the person. A user holding
// a proven second factor who signed in with a key tap and no code is
// [ErrSecondFactorRequired]; a wrong code is [ErrInvalidCredentials]; and a
// recovery code standing in for the second factor is spent and stamps
// [CredentialKindRecoveryCode], all as they are after a password. The
// administrative door takes no code at all and requires MultiFactor instead;
// see [Administrative].
//
// # The options
//
// [WithCredentialKind] names the credential the caller proved, [MultiFactor]
// and [WithTOTPCode] settle the second factor, and [Administrative] sends the
// call through the administrative door. With none, the sign-in is an ordinary
// single-factor one stamped [CredentialKindPrincipal].
func (s *Service) IssueForPrincipal(
	ctx context.Context,
	scope tenancy.Scope,
	userID, activeAccountID string,
	opts ...IssueOption,
) (*SignIn, error) {
	request := &issueRequest{kind: CredentialKindPrincipal}
	for _, opt := range opts {
		opt(request)
	}

	return s.issueForPrincipal(ctx, scope, request, userID, activeAccountID)
}

// issueForPrincipal mints for a principal somebody else proved, stamping the
// kind its caller named, through whichever door the options chose.
func (s *Service) issueForPrincipal(
	ctx context.Context,
	scope tenancy.Scope,
	request *issueRequest,
	userID, activeAccountID string,
) (signIn *SignIn, err error) {
	administrative := request.administrative

	name := opIssueForPrincipal
	if administrative {
		name = opAdminIssueForPrincipal
	}

	ctx, op, done := s.begin(ctx, name,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(adminKey, administrative),
	)
	defer func() { done(err) }()

	if err = scope.Validate(); err != nil {
		return nil, op.Error(err, "checking the scope a sign-in was issued in")
	}

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "issuing a sign-in for a proven principal")
	}

	if request.kind == "" {
		return nil, op.Error(ErrEmptyCredentialKind, "issuing a sign-in for a proven principal")
	}

	op.Set(userIDKey, userID)

	// The user row rather than the principal first, for the reason Service.prove
	// reads it first: the status refusals are told apart here, where the row and
	// its explanation are, and identity's principal read would collapse all
	// three into one.
	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return nil, op.Error(err, "reading the user a sign-in was issued for")
	}

	attempt := &FailedSignIn{UserID: user.ID, Administrative: administrative}

	if !user.AccountStatus.AdmitsSignIn() {
		return nil, s.refuse(ctx, op, scope, attempt, statusRefusal(user), "admitting a sign-in")
	}

	if administrative {
		if err = s.verifyAdministrator(user); err != nil {
			return nil, s.refuse(ctx, op, scope, attempt, err, "admitting an administrative sign-in")
		}
	}

	proven := &proof{attempt: attempt}

	op.SpanOnly(multiFactorKey, request.multiFactor)

	// After the role check, as the second factor comes after it on the password
	// doors: what a single-factor credential is refused with says the
	// credential was good.
	if !request.multiFactor {
		if administrative {
			return nil, s.refuse(ctx, op, scope, attempt, ErrMultiFactorRequired, "admitting an administrative sign-in")
		}

		usedRecoveryCode, verifyErr := s.verifySecondFactor(ctx, s.client.Reader(), scope, user, request.totpCode, false)
		if verifyErr != nil {
			return nil, s.refuse(ctx, op, scope, attempt, verifyErr, "verifying a second factor")
		}

		if usedRecoveryCode {
			proven.recoveryCode = request.totpCode
		}
	}

	principal, err := s.directory.GetPrincipal(ctx, s.client.Reader(), scope, user.ID, activeAccountID)
	if err != nil {
		return nil, op.Error(err, "resolving the principal for a sign-in")
	}

	op.Set(accountIDKey, principal.ActiveAccountID)

	proven.principal = principal

	// The login this sign-in begins, minted here for Service.login's reason.
	familyID := identifiers.New()
	op.Set(familyKey, familyID)

	if signIn, err = s.mintToken(ctx, principal, familyID, administrative); err != nil {
		return nil, op.Error(err, "issuing a token")
	}

	// A recovery code outranks the credential beside it here as it does beside
	// a password: signing in without the enrolled authenticator is the event an
	// audit trail most needs to see.
	kind := request.kind
	if proven.recoveryCode != "" {
		kind = CredentialKindRecoveryCode
	}

	auth := &Authentication{Principal: principal, CredentialKind: kind, Administrative: administrative}

	// Service.login's transaction, the recovery code that stood in for the
	// second factor spent first where one did.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if txErr := s.spendProvenRecoveryCode(ctx, tx, scope, proven); txErr != nil {
			return txErr
		}

		if txErr := s.mintRefreshToken(ctx, tx, scope, signIn, familyID, time.Time{}); txErr != nil {
			return txErr
		}

		if hookErr := s.hooks.AfterAuthenticate(ctx, tx, scope, auth); hookErr != nil {
			return hookErr
		}

		return s.hooks.AfterIssueToken(ctx, tx, scope, signIn)
	}); err != nil {
		return nil, s.settle(ctx, op, scope, proven, err, "recording a sign-in")
	}

	return signIn, nil
}
