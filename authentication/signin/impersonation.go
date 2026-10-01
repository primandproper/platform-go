package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ImpersonationPolicy says whether an operator may act as a subject, and it is
// the whole of the deployment's half of impersonation.
//
// It is handed both users as the directory holds them, unredacted, and returns
// nil to admit the impersonation or an error to refuse it. The error is the
// refusal: it is recorded through [Hooks.AfterFailedSignIn] and returned to the
// caller wrapped, so a policy returning a sentinel of its own is matched by
// errors.Is against that sentinel.
//
// This package names no permission for it and never will. A deployment whose
// operators hold an "imitate.user" permission checks it here; one that forbids
// impersonating another operator checks the subject's service roles here; one
// that requires a support ticket reads the ticket from the context here. Each
// of those is a rule about the deployment's people, and the door below is the
// same whichever of them it asks.
//
// Nor does this package compare the two users' scopes. The operator is read
// in the scope the caller names for them and the subject in the subject's, so
// a deployment that keeps its staff in a directory of their own — or in
// tenancy.Global — can impersonate across that line, and one with several
// customer scopes can impersonate across those lines too. A deployment for
// which some of those crossings are wrong — an operator of one tenant acting as
// a customer of another — refuses them here, by comparing operator.Scope with
// subject.Scope; the door itself admits any pair the policy does.
type ImpersonationPolicy func(ctx context.Context, operator, subject *identity.User) error

// IssueImpersonationToken mints a token for subjectID that is really being used
// by operatorID — an operator seeing what a customer sees, or doing something on
// their behalf — and records that it did.
//
// # What the token says
//
// Its subject is the subject, and every surface in this module reads it as the
// subject's: the rows a request with it writes are filed under them, in their
// directory and against accountID, which the directory resolves exactly as
// [Service.IssueForPrincipal] resolves one. What it adds is [ClaimActor], naming
// the operator, which signin/grpc's extractor turns into callers.Delegated and
// the audit log records beside the subject, and [ClaimActorScope], naming the
// scope the operator is in, so the extractor can look the operator up again on
// every request where they actually live. The request is the subject's; the
// act is the operator's; the token says both, because one identity slot forces
// a deployment to say only one of them and the one it says is the lie.
//
// Whether the operator's own grants come with them is not decided here. See
// [ClaimsInput.ActorID].
//
// # Two scopes
//
// operatorScope is the operator's and scope is the subject's, and neither is
// read from the other: a deployment whose operators are staff in a directory of
// their own, or in tenancy.Global, names that scope for the operator and the
// customer's for the subject. A deployment whose operators share their
// customers' scope passes it twice. The caller already has the first, because
// it authenticated the operator before asking and their token names it. Whether
// a given pair of scopes may meet at all is [ImpersonationPolicy]'s to say.
//
// Everything the impersonation writes is the subject's and is filed in scope:
// the hooks run there, and the token's own scope claim is the subject's.
//
// # What it refuses
//
// Everything, until [WithImpersonationPolicy] names a policy: without one, every
// call is [ErrImpersonationDisabled]. With one, the operator and the subject must
// both be users whose status admits a sign-in — a suspended operator does not
// act as anybody, and a suspended subject is not signed in as by the back door —
// and then the policy is asked. Every refusal is recorded through
// [Hooks.AfterFailedSignIn] with the subject as UserID and the operator as
// ActorID, because a refused impersonation is an event an operator's reviewer
// wants to see at least as much as a granted one.
//
// # How it differs from a sign-in
//
// It is never administrative: the token carries none of the administrative
// door's standing, whatever the operator holds, since it is the subject's token
// and the subject did not come through that door.
//
// It lives [DefaultImpersonationTokenTTL] unless [WithImpersonationTokenTTL]
// says otherwise, and nothing can extend it. An impersonation should end when
// the operator stops, and a refresh token would turn fifteen minutes of support
// work into a thirty-day login.
//
// # The login it records
//
// On a service built with [WithRefreshTokenStore] it is still a login, and the
// store holds a row for it: one that expires with the access token, names the
// operator as [RefreshTokenRequest.ActorID], and whose secret is thrown away the
// moment it is minted, so nobody can ever exchange it. That row is what makes an
// impersonation a login like any other everywhere a login is read.
// [Service.CheckSignIn] finds it live, so a deployment that checks every request
// accepts the token; the subject's [Service.ListSignIns] shows it with the
// operator named on [ActiveSignIn.ActorID], so a person can see somebody is
// signed in as them; and [Service.EndSignIn], [Service.SignOutEverywhere] and an
// operator's revocation end it early, reported to [Hooks.AfterRevokeSignIns] as
// they report any other.
//
// # The record
//
// [Hooks.AfterAuthenticate] and [Hooks.AfterIssueToken] run in one transaction
// with the mint, as they do for every other door. The first is handed an
// [Authentication] stamped [CredentialKindImpersonation] with ActorID set, and
// is where a deployment writes the audit row for the impersonation itself; a
// hook that fails refuses the token, so there is no impersonation without its
// record.
//
// # Where it is exposed
//
// Nowhere in this module. It takes an operator ID, not an operator's
// credential, so its caller has already decided who the operator is: it belongs
// behind the deployment's own operator surface, which has authenticated them,
// and never behind a transport a client can reach naming an operator of its
// choosing. That is the reason IssueForPrincipal has no RPC either.
func (s *Service) IssueImpersonationToken(
	ctx context.Context,
	operatorScope tenancy.Scope,
	operatorID string,
	scope tenancy.Scope,
	subjectID, accountID string,
) (signIn *SignIn, err error) {
	ctx, op, done := s.begin(ctx, opIssueImpersonationToken,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(actorScopeKey, operatorScope.String()),
	)
	defer func() { done(err) }()

	if err = scope.Validate(); err != nil {
		return nil, op.Error(err, "checking the scope an impersonation was issued in")
	}

	if err = operatorScope.Validate(); err != nil {
		return nil, op.Error(err, "checking the scope an impersonation's operator is in")
	}

	if operatorID == "" || subjectID == "" {
		return nil, op.Error(ErrEmptyUserID, "issuing an impersonation")
	}

	if operatorID == subjectID {
		return nil, op.Error(ErrSelfImpersonation, "issuing an impersonation")
	}

	op.Set(userIDKey, subjectID)
	op.Set(actorKey, operatorID)

	attempt := &FailedSignIn{UserID: subjectID, ActorID: operatorID, ActorScope: operatorScope}

	if s.impersonationPolicy == nil {
		return nil, s.refuse(ctx, op, scope, attempt, ErrImpersonationDisabled, "admitting an impersonation")
	}

	operator, err := s.directory.GetUser(ctx, s.client.Reader(), operatorScope, operatorID)
	if err != nil {
		return nil, op.Error(err, "reading the operator of an impersonation")
	}

	if !operator.AccountStatus.AdmitsSignIn() {
		return nil, s.refuse(ctx, op, scope, attempt, statusRefusal(operator), "admitting an impersonation's operator")
	}

	subject, err := s.directory.GetUser(ctx, s.client.Reader(), scope, subjectID)
	if err != nil {
		return nil, op.Error(err, "reading the subject of an impersonation")
	}

	if !subject.AccountStatus.AdmitsSignIn() {
		return nil, s.refuse(ctx, op, scope, attempt, statusRefusal(subject), "admitting an impersonation's subject")
	}

	if policyErr := s.impersonationPolicy(ctx, operator, subject); policyErr != nil {
		return nil, s.refuse(ctx, op, scope, attempt,
			platformerrors.Wrap(policyErr, "the impersonation policy refused"), "admitting an impersonation")
	}

	principal, err := s.directory.GetPrincipal(ctx, s.client.Reader(), scope, subject.ID, accountID)
	if err != nil {
		return nil, op.Error(err, "resolving the principal for an impersonation")
	}

	op.Set(accountIDKey, principal.ActiveAccountID)

	// A family of its own, for Service.login's reason: the token names a login,
	// and this is a login of the operator's that nothing else continues.
	familyID := identifiers.New()
	op.Set(familyKey, familyID)

	if signIn, err = s.mint(ctx, &ClaimsInput{
		Principal:  principal,
		FamilyID:   familyID,
		ActorID:    operator.ID,
		ActorScope: operatorScope,
	}, s.impersonationTokenTTL); err != nil {
		return nil, op.Error(err, "issuing an impersonation token")
	}

	auth := &Authentication{
		Principal:      principal,
		CredentialKind: CredentialKindImpersonation,
		ActorID:        operator.ID,
		ActorScope:     operatorScope,
	}

	// Service.issueForPrincipal's transaction, with the login's row minted
	// unexchangeable in place of a refresh token.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if s.refreshTokens != nil {
			// The secret is dropped here and exists nowhere else: the row is
			// the login's record, not a credential anybody holds.
			if _, mintErr := s.refreshTokens.Issue(ctx, tx, scope, &RefreshTokenRequest{
				TTL:             s.impersonationTokenTTL,
				FamilyID:        familyID,
				SubjectID:       principal.User.ID,
				ActiveAccountID: principal.ActiveAccountID,
				AccessTokenID:   signIn.TokenID,
				ActorID:         operator.ID,
				CredentialKind:  auth.CredentialKind,
			}); mintErr != nil {
				return platformerrors.Wrap(mintErr, "recording an impersonation's login")
			}
		}

		if hookErr := s.hooks.AfterAuthenticate(ctx, tx, scope, auth); hookErr != nil {
			return hookErr
		}

		return s.hooks.AfterIssueToken(ctx, tx, scope, signIn)
	}); err != nil {
		return nil, op.Error(err, "recording an impersonation")
	}

	return signIn, nil
}
