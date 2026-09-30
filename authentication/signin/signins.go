package signin

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

const (
	// DefaultSignInListLimit is how many live logins [Service.ListSignIns]
	// answers with when it is asked for none in particular.
	DefaultSignInListLimit uint16 = 50

	// MaxSignInListLimit is the most [Service.ListSignIns] answers with,
	// whatever it is asked for. A larger request is answered with this many
	// rather than refused, the way a page size is everywhere else in this
	// module: the listing is most recently refreshed first, so what the ceiling
	// leaves out is what has been idle longest.
	MaxSignInListLimit uint16 = 250
)

// ActiveSignIn is one live login, as a "where you're signed in" screen shows
// it: when it began, when it last refreshed, when it lapses if it stops, which
// account it is for, and which door it came through.
//
// It carries the family, how the login happened, and nothing about the device.
// What a screen needs beyond it — a device name, a browser, where the request
// came from — is the consumer's to record or not, and [Hooks.AfterIssueToken] is
// where: it runs inside every mint with the family on the SignIn it is handed,
// so a consumer keying its own device table on FamilyID joins it to this.
// authentication/signin/grpc's WithSignInAnnotator is how what it recorded
// reaches the listing RPCs' answer without a list RPC of the consumer's own.
type ActiveSignIn struct {
	_ struct{} `json:"-"`

	// SignedInAt is when the login began: a credential was proven, and every
	// refresh since has inherited this instant.
	SignedInAt time.Time `json:"signedInAt"`

	// LastRefreshedAt is when the login's current refresh token was minted,
	// which is the last time its holder exchanged one — or SignedInAt, for a
	// login that never has.
	LastRefreshedAt time.Time `json:"lastRefreshedAt"`

	// ExpiresAt is when the current refresh token stops being exchangeable, and
	// so when the login ends if nobody refreshes it before then.
	ExpiresAt time.Time `json:"expiresAt"`

	// FamilyID names the login. It is the same value the access tokens it mints
	// carry as their "sid" claim — see ClaimFamilyID — and the one
	// [Service.EndSignIn] ends.
	FamilyID string `json:"familyID"`

	// ActiveAccountID is the account the login's tokens are for.
	ActiveAccountID string `json:"activeAccountID"`

	// ActorID is the operator acting as this person through this login — one
	// [Service.IssueImpersonationToken] began — and empty for a login of their
	// own. A "where you're signed in" screen shows it, so a person can see
	// that somebody else is signed in as them, and end it.
	ActorID string `json:"actorID,omitempty"`

	// CredentialKind is how the login happened: what proved the sign-in that
	// began it, as its door stamped [Authentication.CredentialKind]. A refresh
	// does not change it. It is recorded by this package rather than left to a
	// consumer's hook, because it is a fact the service knows at the moment of
	// sign-in rather than something about the device — and empty only for a
	// login whose store recorded none.
	CredentialKind CredentialKind `json:"credentialKind,omitempty"`

	// Administrative reports whether the login came through
	// [Service.AdminLoginForToken].
	Administrative bool `json:"administrative"`
}

// ListSignIns answers the live logins one person holds, most recently
// refreshed first, for a screen that shows them where they are signed in.
//
// It is the self-service read and the administrative one both, and which it is
// depends on where userID came from. authentication/signin/grpc's ListSignIns
// takes it off the caller, so a person sees their own logins and nobody
// else's; its ListSignInsForUser takes it from the request, on a service of
// its own behind a permission the deployment grants. That split is the one
// [Service.RevokeRefreshTokensForSubject] already has with [Service.SignOutEverywhere],
// and this package holds no grant for the second half because it decides
// nothing about who may act for whom.
//
// A limit of zero is [DefaultSignInListLimit], and one past
// [MaxSignInListLimit] is that ceiling. A user who has never signed in, or
// whose logins have all ended, is an empty list and no error.
//
// It reads on Client.Reader(), so a login minted a moment ago on the primary
// may be missing from a lagging replica's answer. That is the direction that is
// safe to be wrong in: the login is not ended by being unlisted, and the next
// read shows it.
//
// A service built without [WithRefreshTokenStore] is
// [ErrRefreshTokensNotConfigured].
func (s *Service) ListSignIns(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	limit uint16,
) (signIns []*ActiveSignIn, err error) {
	ctx, op, done := s.begin(ctx, opListSignIns,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if s.refreshTokens == nil {
		return nil, op.Error(ErrRefreshTokensNotConfigured, "listing a subject's sign-ins")
	}

	if err = scope.Validate(); err != nil {
		return nil, op.Error(err, "checking the scope a subject's sign-ins were listed in")
	}

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "reading the subject whose sign-ins are listed")
	}

	signIns, err = s.refreshTokens.ListActiveSignIns(ctx, s.client.Reader(), scope, userID, signInListLimit(limit))
	if err != nil {
		return nil, op.Error(err, "listing a subject's sign-ins")
	}

	return signIns, nil
}

// EndSignIn ends one of a person's logins, named by its family, and reports how
// many refresh tokens it withdrew.
//
// It is [Service.RevokeRefreshTokenFamily] confined to one subject, and the
// confinement is what lets a signed-in caller reach it: a family identifier is
// on every issued token and is not a secret, so ending a login by identifier
// alone is an operator's act, while ending one of your own is a sign-out. A
// family that is not userID's — guessed, borrowed, or somebody else's — is
// zero and no error, as are one that never existed and one already ended, and
// the three are not told apart: a door that refused only the first would be an
// oracle for which family identifiers are live. A lapsed login is a fourth, and
// is zero the same way. None of the four runs [Hooks.AfterRevokeSignIns], which
// is told [RevocationEndSignIn] only when a login actually ended — a hook that
// ran on every call would be the oracle the answer refuses to be.
//
// Ending the family the caller is signed in through is allowed and is a
// sign-out. What it does not do is stop an access token already in somebody's
// hands — see [Service.RevokeRefreshTokenFamily], whose documentation applies
// here unchanged — so the login ends within one access-token lifetime rather
// than at once, unless the consumer's interceptor asks [Service.CheckSignIn].
//
// A service built without [WithRefreshTokenStore] is
// [ErrRefreshTokensNotConfigured], as [Service.ListSignIns] is.
func (s *Service) EndSignIn(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	familyID string,
) (revoked int64, err error) {
	ctx, op, done := s.begin(ctx, opEndSignIn,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
		observability.WithValue(familyKey, familyID),
	)
	defer func() { done(err) }()

	if s.refreshTokens == nil {
		return 0, op.Error(ErrRefreshTokensNotConfigured, "ending a sign-in")
	}

	if err = scope.Validate(); err != nil {
		return 0, op.Error(err, "checking the scope a sign-in was ended in")
	}

	if userID == "" {
		return 0, op.Error(ErrEmptyUserID, "reading the subject whose sign-in is ended")
	}

	if familyID == "" {
		return 0, op.Error(ErrEmptyFamilyID, "reading the sign-in to end")
	}

	if revoked, err = s.endSignIns(ctx, scope, SignInSelector{SubjectID: userID, FamilyID: familyID},
		RevocationEndSignIn, userID); err != nil {
		return 0, op.Error(err, "ending a sign-in")
	}

	return revoked, nil
}

// EndOtherSignIns ends every one of a person's logins but keepFamilyID, and
// reports the families it ended.
//
// It is "sign out my other devices", and it is a door of its own rather than
// [Service.ListSignIns] followed by [Service.EndSignIn] for each entry but one,
// because that loop decides which logins are "other" before it ends them: a
// login made between the list and the last end survives a request whose point
// was that it should not. Here the logins are locked and revoked in one
// transaction, as every other door's are — see [RefreshTokenStore.EndSignIns] —
// and the one kept is never locked at all.
//
// keepFamilyID is the login the request came through, and an empty one is
// [ErrSignInNotIdentified] rather than an instruction to keep nothing. A caller
// who cannot say which login it is has asked for something this door cannot do
// safely, and the one reading it could give — every login ends — is
// [Service.SignOutEverywhere], which the caller can ask for by name. A
// keepFamilyID that is not userID's spares nothing, since there is nothing of
// theirs it names; that is the direction a sign-out should fail in.
//
// It reports families rather than a token count, one per login that was live
// when it ended, and [Hooks.AfterRevokeSignIns] is told the same families as
// [RevocationEndOtherSignIns] with the person as the actor, so whatever records
// a sign-out records one per device. A person with no other login is an empty
// answer, no error, and no hook. What it does not do on its own is stop an
// access token already issued to one of those logins — see
// [Service.RevokeRefreshTokenFamily].
//
// A service built without [WithRefreshTokenStore] is
// [ErrRefreshTokensNotConfigured], as [Service.ListSignIns] is.
func (s *Service) EndOtherSignIns(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	keepFamilyID string,
) (ended []string, err error) {
	ctx, op, done := s.begin(ctx, opEndOtherSignIns,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
		observability.WithValue(familyKey, keepFamilyID),
	)
	defer func() { done(err) }()

	if s.refreshTokens == nil {
		return nil, op.Error(ErrRefreshTokensNotConfigured, "ending a subject's other sign-ins")
	}

	if err = scope.Validate(); err != nil {
		return nil, op.Error(err, "checking the scope a subject's other sign-ins were ended in")
	}

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "reading the subject whose other sign-ins are ended")
	}

	if keepFamilyID == "" {
		return nil, op.Error(ErrSignInNotIdentified, "reading the sign-in to keep")
	}

	endedSignIns, err := s.endSignInsReporting(ctx, scope,
		SignInSelector{SubjectID: userID, ExceptFamilyID: keepFamilyID}, RevocationEndOtherSignIns, userID)
	if err != nil {
		return nil, op.Error(err, "ending a subject's other sign-ins")
	}

	ended = make([]string, 0, len(endedSignIns))
	for _, signIn := range endedSignIns {
		ended = append(ended, signIn.FamilyID)
	}

	return ended, nil
}

// CheckSignIn answers whether an access token's login is still going, and is
// the per-request check a consumer's interceptor may make: nil for a login that
// is, [ErrSignInEnded] for one that is not, and — on a service built with
// [WithSupersededTokenRefusal] — [ErrSignInSuperseded] for an access token the
// login has since replaced.
//
// familyID and tokenID are the token's own claims, [ClaimFamilyID] and its
// "jti". tokenID is read only by a service that refuses superseded tokens,
// which refuses an empty one with [ErrEmptyTokenID]; one that does not compares
// nothing and passes whatever it is given through unread.
//
// # What it buys, and what it costs
//
// An access token is a signed statement that stands until it expires, which is
// why a sign-out, [Service.EndSignIn] and a detected reuse take effect within
// one access-token lifetime rather than at once: they end the family, and
// nothing reads the family while its access token is being presented. This is
// that read. An interceptor that makes it turns every one of those into an
// access token refused on its next request — the promise a "sign out that
// device" button makes — at the price of one indexed read per request. A cache
// in front of it is the consumer's, and so is how stale a cached answer may be:
// whatever it holds is how long an ended login keeps working.
//
// It reads on the write pool rather than a replica, which is the half of that
// price worth naming. A refresh writes the family's current token and the very
// next request presents the access token it minted; a replica that has not yet
// seen the write would answer with the token before it, so a lagging read
// would refuse a fresh token as superseded, and would keep an ended login
// working for as long as it lagged. Either is the check failing at the one
// thing it is for.
//
// # What it does not decide
//
// Anything about the person. A banned user's login is still a login, and the
// directory's principal read is what refuses them; this answers about the
// token's login and nothing else, and makes no call on the directory.
//
// A service built without [WithRefreshTokenStore] holds no logins to read and
// is [ErrRefreshTokensNotConfigured].
func (s *Service) CheckSignIn(
	ctx context.Context,
	scope tenancy.Scope,
	familyID string,
	tokenID string,
) (err error) {
	ctx, op, done := s.begin(ctx, opCheckSignIn,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(familyKey, familyID),
	)
	defer func() { done(err) }()

	if s.refreshTokens == nil {
		return op.Error(ErrRefreshTokensNotConfigured, "checking a sign-in")
	}

	if err = scope.Validate(); err != nil {
		return op.Error(err, "checking the scope a sign-in was checked in")
	}

	if familyID == "" {
		return op.Error(ErrEmptyFamilyID, "reading the sign-in to check")
	}

	if s.refuseSuperseded && tokenID == "" {
		return op.Error(ErrEmptyTokenID, "reading the access token to check")
	}

	current, err := s.refreshTokens.LiveToken(ctx, s.client.Writer(), scope, familyID)
	if err != nil {
		return op.Error(err, "checking a sign-in")
	}

	if s.refuseSuperseded && current.AccessTokenID != tokenID {
		return op.Error(ErrSignInSuperseded, "checking a sign-in")
	}

	return nil
}

// signInListLimit resolves the limit a listing runs with: the default for
// none, the ceiling for too many, and what was asked for otherwise.
func signInListLimit(limit uint16) uint16 {
	switch {
	case limit == 0:
		return DefaultSignInListLimit
	case limit > MaxSignInListLimit:
		return MaxSignInListLimit
	default:
		return limit
	}
}
