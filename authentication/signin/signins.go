package signin

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
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
// It carries the family and nothing that could be presented. What a screen
// needs beyond it — a device name, a browser, where the request came from — is
// the consumer's to record or not, and [Hooks.AfterIssueToken] is where: it runs
// inside every mint with the family on the SignIn it is handed, so a consumer
// keying its own device table on FamilyID joins it to this.
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

	// Administrative reports whether the login came through
	// [Service.AdminLoginForToken].
	Administrative bool `json:"administrative"`
}

// SignInListingStore is a [RefreshTokenStore] that can answer "which logins
// does this person have?" and end one of them on that person's behalf.
//
// It is what [Service.ListSignIns] and [Service.EndSignIn] need, and it is a
// second interface for the reason [IdempotentRefreshTokenStore] is:
// RefreshTokenStore is exported and meant to be implemented outside this
// module, and a method added to it would stop every such implementation
// compiling. This one embeds it and is type-asserted for at runtime, and a
// store that does not implement it keeps every door it had — the two doors
// that need it answer [ErrSignInListingNotSupported].
// [github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens]
// implements it.
type SignInListingStore interface {
	RefreshTokenStore

	// ListActiveSignIns answers one entry per live login a subject holds, most
	// recently refreshed first, and no more than limit of them.
	//
	// Live is the exchange's own reading: a login is listed while it has a
	// refresh token that Redeem would still accept, and not once it has been
	// revoked or has lapsed — whether or not anything has collected its rows.
	//
	// It is a read, so it takes the wider executor: a caller holding
	// Client.Reader() and a caller inside a transaction both call it, and the
	// second sees that transaction's own writes.
	ListActiveSignIns(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		subjectID string,
		limit uint16,
	) ([]*ActiveSignIn, error)

	// RevokeFamilyForSubject ends one login, named by its family, only if it is
	// the named subject's, and reports how many tokens it withdrew.
	//
	// The subject is what makes it safe to hand a signed-in caller: a family
	// identifier is not a secret, so a door ending a family by identifier alone
	// would let whoever learned one end somebody else's login. A family that is
	// not the subject's, one that does not exist and one already ended are all
	// zero and no error, and are not told apart.
	RevokeFamilyForSubject(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		subjectID string,
		familyID string,
	) (int64, error)
}

// ListSignIns answers the live logins one person holds, most recently
// refreshed first, for a screen that shows them where they are signed in.
//
// It is the self-service read and the administrative one both, and which it is
// depends on where userID came from. authentication/signin/grpc's ListSignIns
// takes it off the caller, so a person sees their own logins and nobody
// else's; an operator's surface takes it from a request and stands its own
// authorization in front of the call. That split is the one
// [Service.RevokeRefreshTokensForSubject] already has with SignOutEverywhere,
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
// [ErrRefreshTokensNotConfigured], and one whose store does not implement
// [SignInListingStore] is [ErrSignInListingNotSupported].
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

	store, err := s.signInListing()
	if err != nil {
		return nil, op.Error(err, "listing a subject's sign-ins")
	}

	if err = scope.Validate(); err != nil {
		return nil, op.Error(err, "checking the scope a subject's sign-ins were listed in")
	}

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "reading the subject whose sign-ins are listed")
	}

	signIns, err = store.ListActiveSignIns(ctx, s.client.Reader(), scope, userID, signInListLimit(limit))
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
// oracle for which family identifiers are live.
//
// Ending the family the caller is signed in through is allowed and is a
// sign-out. What it does not do is stop an access token already in somebody's
// hands — see [Service.RevokeRefreshTokenFamily], whose documentation applies
// here unchanged — so the login ends within one access-token lifetime rather
// than at once.
//
// The same two refusals as [Service.ListSignIns] apply when the service or its
// store cannot do this at all.
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

	store, err := s.signInListing()
	if err != nil {
		return 0, op.Error(err, "ending a sign-in")
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

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error

		revoked, txErr = store.RevokeFamilyForSubject(ctx, tx, scope, userID, familyID)

		return txErr
	}); err != nil {
		return 0, op.Error(err, "ending a sign-in")
	}

	return revoked, nil
}

// signInListing resolves the store the two sign-in listing doors run on, or
// the refusal that says why there is none.
//
// The two refusals are different wiring failures and are named apart: no store
// at all is a service that mints no refresh tokens, and a store without the
// interface is one that mints them and cannot enumerate them.
func (s *Service) signInListing() (SignInListingStore, error) {
	if s.refreshTokens == nil {
		return nil, ErrRefreshTokensNotConfigured
	}

	store, ok := s.refreshTokens.(SignInListingStore)
	if !ok {
		return nil, ErrSignInListingNotSupported
	}

	return store, nil
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
