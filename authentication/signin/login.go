package signin

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Authenticate proves a password — and a second-factor code from a user who
// holds one — and answers with the principal it proved, minting nothing.
//
// It is [Service.LoginForToken] stopped one step short. The same reads in the
// same order, the same decoy hash on a handle that names nobody, the same four
// collapsed refusals: it is the same code rather than a second copy of it, so
// the two doors cannot drift on the parts that matter most. See LoginForToken
// for why the order is that order.
//
// It exists for a caller that needs to know who somebody is and holds nothing
// afterwards. [github.com/primandproper/platform-go/v14/authentication/oauth2clients/authserver]'s
// login-form step is the case it was added for: it compares a registration
// against the person who just proved a password, and a token it would throw
// away is a row in whatever the consumer indexes tokens by that nobody will
// ever present.
//
// Nothing about issuing is consulted here — not [WithTokenTTL], not
// [ClaimsBuilder], not the consumer's [TokenIssuer]. [Hooks.AfterAuthenticate]
// still runs, so this door is not invisible to an access log; it is the same
// hook the token doors run, in a transaction of the same shape.
func (s *Service) Authenticate(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *Credentials,
) (*identity.Principal, error) {
	return s.authenticate(ctx, scope, credentials, false)
}

// AdminAuthenticate is Authenticate through the administrative door, and stands
// to it exactly as [Service.AdminLoginForToken] stands to
// [Service.LoginForToken]: the caller must hold one of the service roles
// [WithAdminServiceRoles] named, and must hold a proven second factor whatever
// the service's policy says.
//
// A service that named no administrative roles has no administrative door here
// either, and every call is [ErrAdminLoginDisabled]. See AdminLoginForToken for
// why the second factor is not configurable and why the role is checked after
// the password.
func (s *Service) AdminAuthenticate(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *Credentials,
) (*identity.Principal, error) {
	return s.authenticate(ctx, scope, credentials, true)
}

// LoginForToken proves a password — and a second-factor code from a user who
// holds one — and issues a token for the account the caller named.
//
// # The order, and why it is this order
//
// The handle is read, the password is compared, the status is checked, the
// second factor is checked, the principal is resolved and the token is minted.
// The status check is after the password on purpose: a caller who cannot prove
// the password learns nothing about whether the account exists, is suspended,
// or was terminated, because all three answer with the same sentinel. Moving it
// earlier would turn a suspension into something anybody could enumerate.
//
// A handle that names nobody still costs a password hash, so the two are not
// told apart by a stopwatch either. That is the one place this package
// deliberately does work it has no use for.
//
// # What is in a transaction
//
// Nothing until the end. One transaction is opened after the token exists, and
// it holds [Hooks.AfterAuthenticate] and then [Hooks.AfterIssueToken] — the two
// events a sign-in is, in the order they happened. A failure in either rolls
// both back and no token is returned. Everything before it, the password hash
// included, runs outside any transaction.
//
// A caller who wants the first of those two events and not the second wants
// [Service.Authenticate].
//
// # Policy
//
// Whether the user must hold a second factor is [SecondFactorPolicy]. Which
// account the token is for is the caller's ActiveAccountID, resolved by the
// directory, which refuses an account they are not a live member of. How long
// the token lives is [WithTokenTTL] and what it carries is [ClaimsBuilder].
// Everything else — a rate limit, a lockout, a captcha, a device check — is the
// consumer's, in front of this call, informed by what
// [Hooks.AfterFailedSignIn] told them last time.
func (s *Service) LoginForToken(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *Credentials,
) (*SignIn, error) {
	return s.login(ctx, scope, credentials, false)
}

// AdminLoginForToken is LoginForToken through the administrative door: the
// caller must hold one of the service roles [WithAdminServiceRoles] named, and
// must hold a proven second factor whatever the service's policy says.
//
// The second factor is not optional here and is not configurable. A service
// role that can ban a user, terminate an account or grant operator access to
// somebody else is the one credential where a password alone is not an answer,
// and a knob that let it be would be a knob whose only use is turning that off.
//
// A service that named no administrative roles has no administrative door, and
// every call here is [ErrAdminLoginDisabled]. That refusal and
// [ErrNotAnAdministrator] map to the same code, so a caller cannot tell the two
// apart; a consumer reading their own logs can.
//
// The role check runs after the password, like the status check and for the
// same reason: "is this person an administrator" is not a question an
// unauthenticated caller gets to ask.
func (s *Service) AdminLoginForToken(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *Credentials,
) (*SignIn, error) {
	return s.login(ctx, scope, credentials, true)
}

// authenticate is both doors that stop at the principal.
func (s *Service) authenticate(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *Credentials,
	administrative bool,
) (principal *identity.Principal, err error) {
	name := opAuthenticate
	if administrative {
		name = opAdminAuthenticate
	}

	ctx, op, done := s.begin(ctx, name,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(adminKey, administrative),
	)
	defer func() { done(err) }()

	if principal, err = s.prove(ctx, op, scope, credentials, administrative); err != nil {
		return nil, err
	}

	auth := &Authentication{Principal: principal, Administrative: administrative}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.hooks.AfterAuthenticate(ctx, tx, scope, auth)
	}); err != nil {
		return nil, op.Error(err, "recording an authentication")
	}

	return principal, nil
}

// login is both doors that mint. The two differ in four places — the role
// check, the second-factor requirement, the token lifetime and the flag on what
// comes back — and are otherwise one flow, so they are one function rather than
// two copies that could drift on the parts that matter most.
func (s *Service) login(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *Credentials,
	administrative bool,
) (signIn *SignIn, err error) {
	name := opLogin
	if administrative {
		name = opAdminLogin
	}

	ctx, op, done := s.begin(ctx, name,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(adminKey, administrative),
	)
	defer func() { done(err) }()

	principal, err := s.prove(ctx, op, scope, credentials, administrative)
	if err != nil {
		return nil, err
	}

	if signIn, err = s.mintToken(ctx, principal, administrative); err != nil {
		return nil, op.Error(err, "issuing a token")
	}

	auth := &Authentication{Principal: principal, Administrative: administrative}

	// One transaction for both hooks, in the order the two events happened, so
	// a consumer's token row may reference its authentication row and neither
	// outlives the other. See Hooks.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if hookErr := s.hooks.AfterAuthenticate(ctx, tx, scope, auth); hookErr != nil {
			return hookErr
		}

		return s.hooks.AfterIssueToken(ctx, tx, scope, signIn)
	}); err != nil {
		return nil, op.Error(err, "recording a sign-in")
	}

	return signIn, nil
}

// prove is every door's one flow: it reads the handle, proves the credentials
// against it and resolves who they belong to. What the four doors do with the
// principal it returns is what makes them four rather than one.
//
// It takes the operation rather than beginning one, because the caller's
// deferred done(err) has to see the error the operation actually returns — a
// hook failure after this returns included — and an operation ended here would
// have closed before that existed.
//
// Everything it returns has already been through op.Error or s.refuse, so its
// callers return it bare. A second op.Error would put two entries on one span
// for one refusal.
func (s *Service) prove(
	ctx context.Context,
	op observability.Operation,
	scope tenancy.Scope,
	credentials *Credentials,
	administrative bool,
) (*identity.Principal, error) {
	handle, err := credentials.handle()
	if err != nil {
		// Refused before anything was looked up, so it is not an attempt at
		// anybody's account and no hook hears about it. See
		// Hooks.AfterFailedSignIn.
		return nil, op.Error(err, "reading sign-in credentials")
	}

	op.SpanOnly("signin.handle_is_email", credentials.Username == "")

	attempt := &FailedSignIn{Handle: handle, Administrative: administrative}

	user, err := s.readByHandle(ctx, scope, credentials)
	if err != nil {
		return nil, s.refuse(ctx, op, scope, attempt, err, "reading the user a sign-in named")
	}

	attempt.UserID = user.ID
	op.Set(userIDKey, user.ID)

	if err = s.verifyPassword(ctx, user, credentials.Password); err != nil {
		return nil, s.refuse(ctx, op, scope, attempt, err, "verifying a password")
	}

	if !user.AccountStatus.AdmitsSignIn() {
		return nil, s.refuse(ctx, op, scope, attempt, statusRefusal(user), "admitting a sign-in")
	}

	if administrative {
		if err = s.verifyAdministrator(user); err != nil {
			return nil, s.refuse(ctx, op, scope, attempt, err, "admitting an administrative sign-in")
		}
	}

	if err = s.verifySecondFactor(ctx, user, credentials.TOTPCode, administrative); err != nil {
		return nil, s.refuse(ctx, op, scope, attempt, err, "verifying a second factor")
	}

	// Past here the credentials are proven, and a failure is this service's or
	// the directory's rather than an attempt at somebody's account — so it is
	// not recorded as one.
	principal, err := s.directory.GetPrincipal(ctx, s.client.Reader(), scope, user.ID, credentials.ActiveAccountID)
	if err != nil {
		return nil, op.Error(err, "resolving the principal for a sign-in")
	}

	op.Set(accountIDKey, principal.ActiveAccountID)

	return principal, nil
}

// handle returns the one handle a set of credentials names, refusing both and
// neither.
func (c *Credentials) handle() (string, error) {
	if c == nil {
		return "", ErrNilCredentials
	}

	switch {
	case c.Username != "" && c.EmailAddress != "":
		return "", ErrAmbiguousHandle
	case c.Username == "" && c.EmailAddress == "":
		return "", ErrEmptyHandle
	case c.Password == "":
		return "", ErrEmptyPassword
	case c.Username != "":
		return c.Username, nil
	default:
		return c.EmailAddress, nil
	}
}

// readByHandle reads the user a set of credentials names, on the reader.
//
// An absence is ErrInvalidCredentials rather than identity's ErrUserNotFound,
// and that translation is the point: the directory's answer is accurate and
// this one is safe, and a sign-in is the one caller that must not pass the
// accurate one on. Anything else the directory says — a connection that is
// gone, a scope that will not validate — passes through as itself, because
// collapsing those into "invalid credentials" would tell a user to check their
// password while the database is down.
func (s *Service) readByHandle(
	ctx context.Context,
	scope tenancy.Scope,
	credentials *Credentials,
) (*identity.User, error) {
	read := s.directory.GetUserByUsername
	if credentials.Username == "" {
		read = s.directory.GetUserByEmailAddress
	}

	handle := credentials.Username
	if handle == "" {
		handle = credentials.EmailAddress
	}

	user, err := read(ctx, s.client.Reader(), scope, handle)
	if err != nil {
		if platformerrors.Is(err, identity.ErrUserNotFound) {
			// The decoy hash. It is the same argon2 comparison a real user's
			// password would have cost, so an unknown handle and a wrong
			// password take the same time, and the timing oracle that would
			// otherwise enumerate the directory is not there to measure.
			//
			// Hashing the submitted password is what makes this work without a
			// stored decoy: hashing and verifying cost the same, and there is no
			// decoy value to keep valid for whatever engine the consumer chose.
			// Its result is discarded, its error included — a hasher that failed
			// here has told us nothing about these credentials.
			//nolint:errcheck // The decoy's result is discarded on purpose, its error included: a hasher that failed here has told us nothing about these credentials, and reporting it would make the unknown handle distinguishable again.
			_, _ = s.authenticator.HashPassword(ctx, credentials.Password)

			return nil, ErrInvalidCredentials
		}

		return nil, err
	}

	return user, nil
}

// verifyPassword compares a submitted password against the stored hash.
//
// A user who holds no password is ErrInvalidCredentials here, not
// ErrNoPasswordCredential: identity.User.HasPassword is right that the two are
// different answers, and this is the caller that cannot afford to give the
// specific one. Service.UpdatePassword, where the caller is the subject and is
// already signed in, gives it.
func (s *Service) verifyPassword(ctx context.Context, user *identity.User, password string) error {
	if !user.HasPassword() {
		// The same decoy the unknown handle gets, for the same reason: a
		// passwordless user must not be identifiable by how quickly they are
		// refused.
		//nolint:errcheck // The decoy hash again — see readByHandle.
		_, _ = s.authenticator.HashPassword(ctx, password)

		return ErrInvalidCredentials
	}

	matches, err := s.authenticator.PasswordMatches(ctx, user.HashedPassword, password)
	if err != nil {
		// A malformed stored hash is this service's problem and not the
		// caller's guess, so it is not collapsed into the refusal. It is joined
		// to it: the caller is told the credentials did not prove anything,
		// which is true, and an operator is told the hash would not parse.
		return platformerrors.Join(ErrInvalidCredentials, err)
	}

	if !matches {
		return ErrInvalidCredentials
	}

	return nil
}

// statusRefusal names why a status admits no sign-in.
//
// The banned user's explanation is wrapped around the sentinel rather than
// replacing it, so a caller matches ErrUserBanned and a person reads why. It is
// the operator's own prose and is meant to be shown to them — identity says so
// on the column.
func statusRefusal(user *identity.User) error {
	switch user.AccountStatus {
	case identity.StatusBanned:
		if user.AccountStatusExplanation != "" {
			return platformerrors.Wrapf(ErrUserBanned, "%s", user.AccountStatusExplanation)
		}

		return ErrUserBanned
	case identity.StatusTerminated:
		return ErrUserTerminated
	case identity.StatusUnverified:
		return ErrUserUnverified
	case identity.StatusGood:
		// Unreachable: this is only called for a status that admits no sign-in.
		// It is named rather than left to the default so that a fifth status
		// added to identity fails the exhaustiveness linter here.
		return nil
	default:
		return platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue,
			"account status %q", user.AccountStatus)
	}
}

// verifyAdministrator checks a user against the roles a consumer named as
// administrative.
func (s *Service) verifyAdministrator(user *identity.User) error {
	if len(s.adminRoles) == 0 {
		return ErrAdminLoginDisabled
	}

	for _, role := range user.ServiceRoles {
		if slices.Contains(s.adminRoles, role) {
			return nil
		}
	}

	return ErrNotAnAdministrator
}

// verifySecondFactor applies the second-factor rule for one sign-in.
//
// Three answers, and the middle one is the disclosure the package documentation
// is about: a user with a proven secret and no code is told to send one, which
// says the password was right. A wrong code is ErrInvalidCredentials, which
// says nothing more than the refusal already did.
func (s *Service) verifySecondFactor(ctx context.Context, user *identity.User, code string, administrative bool) error {
	if !user.TwoFactorEnabled() {
		if administrative || s.secondFactor == SecondFactorRequired {
			return ErrSecondFactorNotEnrolled
		}

		return nil
	}

	if code == "" {
		return ErrSecondFactorRequired
	}

	if err := s.verifier.Verify(ctx, user.TwoFactorSecret, code); err != nil {
		return ErrInvalidCredentials
	}

	return nil
}

// mintToken issues the token a completed sign-in hands back.
func (s *Service) mintToken(ctx context.Context, principal *identity.Principal, administrative bool) (*SignIn, error) {
	ttl := s.tokenTTL
	if administrative {
		ttl = s.adminTokenTTL
	}

	claims, err := s.claims(ctx, principal)
	if err != nil {
		return nil, platformerrors.Wrap(err, "building token claims")
	}

	token, jti, err := s.issuer.IssueToken(ctx, principal.User.ID, ttl, claims)
	if err != nil {
		return nil, err
	}

	return &SignIn{
		Token:          token,
		TokenID:        jti,
		ExpiresAt:      s.clk.Now().UTC().Add(ttl),
		Principal:      principal,
		Administrative: administrative,
	}, nil
}

// refuse records a refused attempt and returns what the caller is told.
//
// The reason reaches the span, the log line and the hook; the caller gets the
// sentinel and nothing else. A hook that fails does not rescue the sign-in — it
// was already refused — so its error is joined to the refusal rather than
// replacing it, which keeps errors.Is against the sentinel matching and keeps
// the consumer's failure from being swallowed.
func (s *Service) refuse(
	ctx context.Context,
	op observability.Operation,
	scope tenancy.Scope,
	attempt *FailedSignIn,
	reason error,
	description string,
) error {
	attempt.Reason = reason

	op.SpanOnly(reasonKey, reason.Error())

	err := op.Error(reason, "%s", description)

	if hookErr := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.hooks.AfterFailedSignIn(ctx, tx, scope, attempt)
	}); hookErr != nil {
		op.Acknowledge(hookErr, "recording a failed sign-in")

		return platformerrors.Join(err, hookErr)
	}

	return err
}
