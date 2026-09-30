package signin

import (
	"slices"
	"testing"
	"time"

	domain "github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func doors(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("the right password signs in, for the registrant's own account", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		who := signInAs(t, s, anon)

		issued := loggedIn(t, anon, who.username, password)

		test.NotEqOp(t, "", issued.GetToken(), test.Sprint("a sign-in answered with no access token"))
		test.NotEqOp(t, "", issued.GetTokenId(), test.Sprint("a sign-in answered with no token identifier"))
		test.EqOp(t, who.accountID, issued.GetActiveAccountId(),
			test.Sprint("a sign-in naming no account landed somewhere other than the registrant's only one"))
		test.False(t, issued.GetAdministrative(), test.Sprint("the ordinary door minted an administrative token"))

		// The contract populates the family whether or not a refresh token was
		// stored, because it names a sign-in rather than a row.
		test.NotEqOp(t, "", issued.GetFamilyId(), test.Sprint("a sign-in answered with no family"))

		// Whole seconds and generously: what is asserted is that the token is
		// not born expired, not how long it lives, which is the deployment's.
		must.NotNil(t, issued.GetExpiresAt(), must.Sprint("a sign-in answered with no expiry"))
		test.True(t, issued.GetExpiresAt().AsTime().After(time.Now().Add(-time.Second)),
			test.Sprint("a sign-in answered with a token already expired"))
	})

	// The refusal the whole package is organized around. Telling an unknown
	// handle from a wrong password is telling whoever is guessing which half of
	// the guess was right, so the two must be the same answer on every channel a
	// client reads — and the right password must still get in, or a door that
	// refused everybody would pass.
	t.Run("a wrong password and an unknown username are one answer", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		who := signInAs(t, s, anon)

		_, wrong := login(t.Context(), anon, who.username, wrongPassword, "")
		refused(t, s, wrong, codes.Unauthenticated, reasonInvalidCredentials)

		// The sentinel's own words rather than the code's name, which is what a
		// client with no access to the details reads.
		test.EqOp(t, domain.ErrInvalidCredentials.Error(), status.Convert(wrong).Message())

		_, unknown := login(t.Context(), anon, "conf_"+identifiers.New(), password, "")
		indistinguishable(t, s, wrong, unknown, "an unknown username against a wrong password")

		loggedIn(t, anon, who.username, password)
	})

	// The calling code being wrong rather than a guess at a credential, so it is
	// told to correct its request rather than to try another password.
	t.Run("a sign-in naming no credentials is a bad request", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		who := signInAs(t, s, anon)

		_, err := anon.LoginForToken(t.Context(), &signinpb.LoginForTokenRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		loggedIn(t, anon, who.username, password)
	})

	// The one disclosure the door makes on purpose, made in a form a client can
	// branch on: the password was right and a code is needed. A wrong code is
	// then the ordinary refusal, and the right one gets in.
	t.Run("a user with a second factor is told to send a code, by reason", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		sub, who := signedIn(t, s, anon, refreshTOTPSecret, verifyTOTPSecret)
		secret := enroll(t, sub)

		_, withoutCode := login(t.Context(), anon, who.username, password, "")
		refused(t, s, withoutCode, codes.Unauthenticated, reasonSecondFactorRequired)
		test.EqOp(t, domain.ErrSecondFactorRequired.Error(), status.Convert(withoutCode).Message())

		_, badPassword := login(t.Context(), anon, who.username, wrongPassword, "")
		_, badCode := login(t.Context(), anon, who.username, password, wrongCode(t, secret))
		indistinguishable(t, s, badPassword, badCode, "a wrong code against a wrong password")
		if reasons(t, s) {
			test.EqOp(t, reasonInvalidCredentials, reason(badCode))
		}

		issued, err := login(t.Context(), anon, who.username, password, code(t, secret))
		must.NoError(t, err, must.Sprint("the right password and the right code did not sign in"))
		test.NotEqOp(t, "", issued.GetToken())
	})

	// A token is for one account, and a person in two chooses which at the
	// door. The control is the same person naming none, which lands in the
	// account they registered with: so the name moved the token, rather than a
	// second membership having moved their default.
	t.Run("a member of two accounts lands in the one they name, and in their default when they name none", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		joiner, who := signedIn(t, s, anon, acceptInvitation)
		inviter := directoryCaller(t, s, invite)

		if inviter.AccountID == "" {
			conformance.Skip(t, "conformance: this subject does not surface the inviter's account, so there is no second account to name; skipping")
		}

		must.NotEqOp(t, who.accountID, inviter.AccountID, must.Sprint("the inviter is in the registrant's own account"))

		delivered := s.Seams().Actions.InvitationToken
		s.NeedsAction(t, delivered != nil, "invitation token")

		invited, err := inviter.Surfaces.Identity.Invite(inviter.Context(t.Context()), &identitypb.InviteRequest{
			AccountId: inviter.AccountID,
			ToEmail:   who.email,
			ToName:    inviteeName,
			Roles:     []string{s.Roles().Membership[0]},
		})
		must.NoError(t, err, must.Sprint("inviting the registrant into a second account"))

		token, err := delivered(t.Context(), inviter.ScopeFor(identitySurface), invited.GetInvitation().GetId())
		must.NoError(t, err, must.Sprint("reading the token the deployment delivered"))

		_, err = joiner.Surfaces.Identity.AcceptInvitation(joiner.Context(t.Context()), &identitypb.AcceptInvitationRequest{
			InvitationId: invited.GetInvitation().GetId(),
			Token:        token,
		})
		must.NoError(t, err, must.Sprint("accepting the invitation into a second account"))

		named, err := loginInto(t.Context(), anon, who.username, password, inviter.AccountID)
		must.NoError(t, err, must.Sprint("signing in naming an account the registrant is a member of"))
		test.EqOp(t, inviter.AccountID, named.GetActiveAccountId(),
			test.Sprint("a sign-in naming an account landed somewhere else"))

		unnamed := loggedIn(t, anon, who.username, password)
		test.EqOp(t, who.accountID, unnamed.GetActiveAccountId(),
			test.Sprint("a sign-in naming no account did not land in the registrant's default"))

		there := caller(t, s, named, getAuthStatus)

		standing, err := there.Surfaces.SignIn.GetAuthStatus(there.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err)
		test.EqOp(t, inviter.AccountID, standing.GetStatus().GetActiveAccountId(),
			test.Sprint("the status of a token issued for an account names another"))
		test.SliceContains(t, standing.GetStatus().GetAccountIds(), inviter.AccountID)
		test.SliceContains(t, standing.GetStatus().GetAccountIds(), who.accountID)
	})

	// The other half of choosing an account at the door: the choice is among
	// the accounts the person belongs to, and naming somebody else's is
	// refused rather than honored. Naming their own is the control, so the
	// refusal is about whose account was named rather than about naming one,
	// and the right password still getting in afterwards shows the refusal
	// was not counted as a wrong one.
	t.Run("a sign-in naming an account lands there, and one naming somebody else's is refused", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken)
		who := signInAs(t, s, anon)
		other := signInAs(t, s, anon)

		must.NotEqOp(t, who.accountID, other.accountID, must.Sprint("two registrants share an account"))

		own, err := loginInto(t.Context(), anon, who.username, password, who.accountID)
		must.NoError(t, err, must.Sprint("signing in naming the registrant's own account"))
		test.EqOp(t, who.accountID, own.GetActiveAccountId(),
			test.Sprint("a sign-in naming the registrant's own account landed somewhere else"))

		// The code alone: the membership is identity's, so the contract lists
		// no sign-in reason for this refusal.
		issued, err := loginInto(t.Context(), anon, who.username, password, other.accountID)
		must.Error(t, err, must.Sprint("a sign-in naming somebody else's account was honored"))
		test.EqOp(t, codes.NotFound, status.Code(err), test.Sprintf("the refusal was %v", err))
		test.Nil(t, issued, test.Sprint("a refused sign-in answered with a token"))

		loggedIn(t, anon, who.username, password)
	})

	// Being made an administrator does not waive the second factor the
	// administrative door insists on, whatever the ordinary door's policy.
	// The control is the same person before the grant, refused on the role:
	// the move from that refusal to this one shows the grant took, and that
	// the door now refuses on the factor.
	t.Run("the administrative door refuses an administrator with no proven second factor", func(t *testing.T) {
		t.Parallel()

		role := s.Roles().Administrator
		if role == "" {
			conformance.Skip(t, "conformance: this subject names no Roles.Administrator, so nobody can be made one the administrative door admits; skipping")
		}

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, adminLoginForToken)
		granter := directoryCaller(t, s, setUserServiceRoles)
		who := signInAs(t, s, anon)

		// Naming Roles.Administrator is the subject declaring it has the door,
		// so the refusal before the grant is on the role and never on the door
		// being absent: a door that answers ADMIN_SIGNIN_UNAVAILABLE here is a
		// wiring regression, and fails rather than skipping.
		_, before := adminLogin(t.Context(), anon, who.username, password, "")
		refused(t, s, before, codes.PermissionDenied, reasonNotAnAdministrator)

		_, err := granter.Surfaces.Identity.SetUserServiceRoles(granter.Context(t.Context()),
			&identitypb.SetUserServiceRolesRequest{UserId: who.userID, Roles: []string{role}})
		must.NoError(t, err, must.Sprint("making the registrant an administrator"))

		issued, err := adminLogin(t.Context(), anon, who.username, password, "")
		refused(t, s, err, codes.FailedPrecondition, reasonSecondFactorNotEnrolled)
		test.Nil(t, issued, test.Sprint("a refused administrative sign-in answered with a token"))

		// The ordinary door holds them to the deployment's policy rather than
		// the administrative door's. Where that policy demands a factor too,
		// it answers as the administrative door did, and the refusal above
		// stands on its own.
		ordinary, err := login(t.Context(), anon, who.username, password, "")
		if err != nil {
			refused(t, s, err, codes.FailedPrecondition, reasonSecondFactorNotEnrolled)

			return
		}

		test.False(t, ordinary.GetAdministrative(), test.Sprint("the ordinary door minted an administrative token"))
	})

	// The administrative door holds an administrator to the same second
	// factor the ordinary one holds everybody to, with the same refusals: a
	// code is asked for by reason, and a wrong one is a wrong password.
	t.Run("an enrolled administrator signs in through the administrative door", func(t *testing.T) {
		t.Parallel()

		role := s.Roles().Administrator
		if role == "" {
			conformance.Skip(t, "conformance: this subject names no Roles.Administrator, so nobody can be made one the administrative door admits; skipping")
		}

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, adminLoginForToken)
		sub, who := signedIn(t, s, anon, refreshTOTPSecret, verifyTOTPSecret)
		secret := enroll(t, sub)

		granter := directoryCaller(t, s, setUserServiceRoles)
		_, err := granter.Surfaces.Identity.SetUserServiceRoles(granter.Context(t.Context()),
			&identitypb.SetUserServiceRolesRequest{UserId: who.userID, Roles: []string{role}})
		must.NoError(t, err, must.Sprint("making the registrant an administrator"))

		_, withoutCode := adminLogin(t.Context(), anon, who.username, password, "")
		refused(t, s, withoutCode, codes.Unauthenticated, reasonSecondFactorRequired)

		_, badPassword := adminLogin(t.Context(), anon, who.username, wrongPassword, "")
		_, badCode := adminLogin(t.Context(), anon, who.username, password, wrongCode(t, secret))
		indistinguishable(t, s, badPassword, badCode, "a wrong code against a wrong password, at the administrative door")
		if reasons(t, s) {
			test.EqOp(t, reasonInvalidCredentials, reason(badCode))
		}

		issued, err := adminLogin(t.Context(), anon, who.username, password, code(t, secret))
		must.NoError(t, err, must.Sprint("an enrolled administrator with the right password and code was refused"))
		test.NotEqOp(t, "", issued.GetToken())
		test.True(t, issued.GetAdministrative(), test.Sprint("the administrative door minted an ordinary token"))
	})

	// Whether a deployment has an administrative door at all is its own
	// decision, and the two ways of being refused there — no door, or not
	// admitted through it — share a code and mean the same to a person. What
	// every deployment owes is that somebody it never made an administrator is
	// told to stop asking rather than to retype a password that was right.
	t.Run("the administrative door refuses somebody never made an administrator", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, adminLoginForToken)
		who := signInAs(t, s, anon)

		_, err := anon.AdminLoginForToken(t.Context(), &signinpb.AdminLoginForTokenRequest{
			Credentials: &signinpb.Credentials{Username: who.username, Password: password},
		})
		must.Error(t, err, must.Sprint("somebody nobody made an administrator signed in as one"))
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		if reasons(t, s) {
			test.True(t, slices.Contains([]string{reasonNotAnAdministrator, reasonAdminSignInUnavailable}, reason(err)),
				test.Sprintf("the administrative refusal carried reason %q", reason(err)))
		}

		// The same credentials through the ordinary door, so the refusal above
		// was about the door rather than the password.
		issued := loggedIn(t, anon, who.username, password)
		test.False(t, issued.GetAdministrative())
	})
}
