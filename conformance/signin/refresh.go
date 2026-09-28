package signin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

	idempotencygrpc "github.com/primandproper/primitives-go/v2/idempotency/grpc"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// rotating signs somebody in and returns the token, skipping where the
// deployment minted no refresh token — which docs/client-contract.md calls a
// valid shape rather than an error, so it is an absence and not a failure.
func rotating(t *testing.T, client signinpb.SignInServiceClient, username, secret string) *signinpb.IssuedToken {
	t.Helper()

	issued := loggedIn(t, client, username, secret)

	if issued.GetRefreshToken() == "" {
		t.Skip("conformance: this deployment stores no refresh tokens, so a sign-in answered none and there is no login to rotate or end")
	}

	return issued
}

// exchange spends a refresh token.
func exchange(ctx context.Context, client signinpb.SignInServiceClient, refreshToken string) (*signinpb.IssuedToken, error) {
	response, err := client.ExchangeRefreshToken(ctx, &signinpb.ExchangeRefreshTokenRequest{RefreshToken: refreshToken})
	if err != nil {
		return nil, err
	}

	return response.GetToken(), nil
}

// neverMinted is a refresh token no deployment issued, for the refusal every
// dead token is compared against.
func neverMinted() string { return "conf-never-minted-" + identifiers.New() }

func refresh(t *testing.T, s *conformance.Session) {
	t.Helper()

	// One login, two credentials: the family carries and the secret does not.
	// It is a door, reached with nobody on the request, because a client whose
	// access token has expired has no credential to present but this one.
	t.Run("an exchange mints a successor in the same login", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		who := signInAs(t, s, anon)
		first := rotating(t, anon, who.username, password)

		test.NotEqOp(t, "", first.GetFamilyId())
		test.NotNil(t, first.GetRefreshTokenExpiresAt(), test.Sprint("a refresh token answered with no deadline"))

		second, err := exchange(t.Context(), anon, first.GetRefreshToken())
		must.NoError(t, err, must.Sprint("a refresh token that was just minted could not be exchanged"))

		test.EqOp(t, first.GetFamilyId(), second.GetFamilyId())
		test.NotEqOp(t, first.GetRefreshToken(), second.GetRefreshToken())
		test.NotEqOp(t, "", second.GetToken())
		test.EqOp(t, who.accountID, second.GetActiveAccountId())
	})

	// A replay is how a stolen token shows itself, and it answers exactly as a
	// token nobody minted does: the thief is not told the theft was noticed.
	t.Run("a replayed refresh token is one answer with a token never minted", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		who := signInAs(t, s, anon)
		first := rotating(t, anon, who.username, password)

		_, err := exchange(t.Context(), anon, first.GetRefreshToken())
		must.NoError(t, err, must.Sprint("the control: the first exchange was refused"))

		_, replayed := exchange(t.Context(), anon, first.GetRefreshToken())
		refused(t, s, replayed, codes.Unauthenticated, reasonInvalidCredentials)

		_, unknown := exchange(t.Context(), anon, neverMinted())
		indistinguishable(t, s, unknown, replayed, "a replayed refresh token against one never minted")
	})

	// What turns a stolen token from a shared session nobody can see into a
	// detected event: the replay ends the whole login, so the successor the
	// legitimate holder was given stops working too.
	t.Run("a replay ends the login, the successor included", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		who := signInAs(t, s, anon)
		first := rotating(t, anon, who.username, password)

		second, err := exchange(t.Context(), anon, first.GetRefreshToken())
		must.NoError(t, err)

		_, err = exchange(t.Context(), anon, first.GetRefreshToken())
		must.Error(t, err, must.Sprint("a spent refresh token was exchanged a second time"))

		_, err = exchange(t.Context(), anon, second.GetRefreshToken())
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)
	})

	// R10: a client that lost the answer to an exchange retries under the key
	// it sent the first time and is answered with a fresh successor in the same
	// login, rather than treated as the thief a bare re-send looks like. It
	// ends up holding exactly one live refresh token.
	t.Run("a retry under the same idempotency key survives a lost answer", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		who := signInAs(t, s, anon)
		first := rotating(t, anon, who.username, password)

		keyed := metadata.AppendToOutgoingContext(t.Context(), idempotencygrpc.MetadataKey, identifiers.New())

		lost, err := exchange(keyed, anon, first.GetRefreshToken())
		must.NoError(t, err)

		retried, err := exchange(keyed, anon, first.GetRefreshToken())
		must.NoError(t, err, must.Sprint("a keyed retry was refused as a replay"))

		test.EqOp(t, first.GetFamilyId(), retried.GetFamilyId())
		test.NotEqOp(t, lost.GetRefreshToken(), retried.GetRefreshToken(),
			test.Sprint("a retry was handed the successor the lost answer carried, which is a replay rather than a re-mint"))

		_, err = exchange(t.Context(), anon, retried.GetRefreshToken())
		test.NoError(t, err, test.Sprint("the successor a retry minted does not work"))

		_, err = exchange(t.Context(), anon, lost.GetRefreshToken())
		test.Error(t, err, test.Sprint("the successor the lost answer carried is still live beside the retry's"))
	})

	// A key the deployment will not accept is refused before the token is
	// touched, so the client corrects its header and its next attempt is a
	// first attempt. The contract states the bound as 255 bytes.
	t.Run("a malformed idempotency key is a bad request that spends nothing", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		who := signInAs(t, s, anon)
		first := rotating(t, anon, who.username, password)

		tooLong := metadata.AppendToOutgoingContext(t.Context(), idempotencygrpc.MetadataKey, strings.Repeat("k", 256))

		_, err := exchange(tooLong, anon, first.GetRefreshToken())
		must.Error(t, err, must.Sprint("an idempotency key longer than the contract allows was accepted"))
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		_, err = exchange(t.Context(), anon, first.GetRefreshToken())
		test.NoError(t, err, test.Sprint("a refused key spent the token anyway"))
	})

	t.Run("signing out ends the login the refresh token names", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken, signOut)
		who := signInAs(t, s, anon)
		ended := rotating(t, anon, who.username, password)
		kept := loggedIn(t, anon, who.username, password)

		_, err := anon.SignOut(t.Context(), &signinpb.SignOutRequest{RefreshToken: ended.GetRefreshToken()})
		must.NoError(t, err)

		_, err = exchange(t.Context(), anon, ended.GetRefreshToken())
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)

		// The control, and the scope of the button: a second login of the same
		// person is another device, and signing out of one ends that one.
		_, err = exchange(t.Context(), anon, kept.GetRefreshToken())
		test.NoError(t, err, test.Sprint("signing out of one login ended another"))
	})

	// Every refusal a presented token can draw is one answer, and that answer is
	// OK. A sign-out that refused an unknown token would be an oracle for which
	// tokens are real, and one that refused a second press would be an error
	// for something that had already happened.
	t.Run("signing out answers a dead token as it answers a live one", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, signOut)
		who := signInAs(t, s, anon)
		issued := rotating(t, anon, who.username, password)

		first, err := anon.SignOut(t.Context(), &signinpb.SignOutRequest{RefreshToken: issued.GetRefreshToken()})
		must.NoError(t, err)

		again, err := anon.SignOut(t.Context(), &signinpb.SignOutRequest{RefreshToken: issued.GetRefreshToken()})
		must.NoError(t, err, must.Sprint("signing out twice was refused the second time"))

		never, err := anon.SignOut(t.Context(), &signinpb.SignOutRequest{RefreshToken: neverMinted()})
		must.NoError(t, err, must.Sprint("signing out with a token never minted was refused"))

		test.True(t, proto.Equal(first, again))
		test.True(t, proto.Equal(first, never))
	})

	// The caller's every login on every device, this one included — and the
	// subject is the caller, so there is nothing on the request to name
	// anybody else with.
	t.Run("signing out everywhere ends every login the caller holds", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		sub, who := signedIn(t, s, anon, signOutEverywhere)

		phone := rotating(t, anon, who.username, password)
		laptop := loggedIn(t, anon, who.username, password)
		must.NotEqOp(t, phone.GetFamilyId(), laptop.GetFamilyId(), must.Sprint("two sign-ins were one login"))

		// The control: the phone's login is live before the button is pressed.
		phoneNext, err := exchange(t.Context(), anon, phone.GetRefreshToken())
		must.NoError(t, err)

		_, err = sub.Surfaces.SignIn.SignOutEverywhere(sub.Context(t.Context()), &signinpb.SignOutEverywhereRequest{})
		must.NoError(t, err)

		for _, dead := range []string{phoneNext.GetRefreshToken(), laptop.GetRefreshToken()} {
			_, exchangeErr := exchange(t.Context(), anon, dead)
			refused(t, s, exchangeErr, codes.Unauthenticated, reasonInvalidCredentials)
		}

		// Ending every login is not ending the account: the password still
		// signs in, as a new login.
		loggedIn(t, anon, who.username, password)
	})

	// The logins a person holds, as a screen lists them, and the button beside
	// each one. A family identifier is not a secret, so ending one that is not
	// the caller's must end nothing and say nothing: every EndSignIn answer is
	// the same answer, and only the tokens behind it tell what happened.
	t.Run("the caller's live logins are listed, and ending one by name ends that one only", func(t *testing.T) {
		t.Parallel()

		anon := anonymous(t, s, verifyEmailAddress, loginForToken, exchangeRefreshToken)
		sub, who := signedIn(t, s, anon, listSignIns, endSignIn)

		phone := rotating(t, anon, who.username, password)
		laptop := loggedIn(t, anon, who.username, password)
		must.NotEqOp(t, phone.GetFamilyId(), laptop.GetFamilyId(), must.Sprint("two sign-ins were one login"))

		ctx := sub.Context(t.Context())

		listed, err := sub.Surfaces.SignIn.ListSignIns(ctx, &signinpb.ListSignInsRequest{})
		must.NoError(t, err)

		// By family and never by count or order: the caller's own login is
		// listed too, and "most recently refreshed first" ties on a dialect
		// that keeps whole seconds.
		byFamily := map[string]*signinpb.ActiveSignIn{}
		current := 0

		for _, signIn := range listed.GetSignIns() {
			byFamily[signIn.GetFamilyId()] = signIn

			if signIn.GetCurrent() {
				current++
			}
		}

		test.LessEq(t, 1, current, test.Sprint("more than one login is the one the request was made through"))

		for _, family := range []string{phone.GetFamilyId(), laptop.GetFamilyId()} {
			signIn, ok := byFamily[family]
			if !ok {
				t.Errorf("a live login %s is not listed", family)

				continue
			}

			test.EqOp(t, who.accountID, signIn.GetActiveAccountId())
			test.False(t, signIn.GetAdministrative())
			must.NotNil(t, signIn.GetExpiresAt())
			test.True(t, signIn.GetExpiresAt().AsTime().After(time.Now().Add(-time.Second)),
				test.Sprint("a live login is listed as already over"))
		}

		ended, err := sub.Surfaces.SignIn.EndSignIn(ctx, &signinpb.EndSignInRequest{FamilyId: phone.GetFamilyId()})
		must.NoError(t, err)

		_, err = exchange(t.Context(), anon, phone.GetRefreshToken())
		refused(t, s, err, codes.Unauthenticated, reasonInvalidCredentials)

		// The control, and the scope of the button: the laptop is another
		// login, and still works.
		_, err = exchange(t.Context(), anon, laptop.GetRefreshToken())
		test.NoError(t, err, test.Sprint("ending one login ended another"))

		relisted, err := sub.Surfaces.SignIn.ListSignIns(ctx, &signinpb.ListSignInsRequest{})
		must.NoError(t, err)

		for _, signIn := range relisted.GetSignIns() {
			test.NotEqOp(t, phone.GetFamilyId(), signIn.GetFamilyId(), test.Sprint("an ended login is still listed"))
		}

		// Somebody else's login, one that never existed, and the one just
		// ended: each is answered exactly as the ending that worked was.
		stranger := signInAs(t, s, anon)
		theirs := rotating(t, anon, stranger.username, password)

		for what, family := range map[string]string{
			"somebody else's login": theirs.GetFamilyId(),
			"a login never begun":   "conf-never-begun-" + identifiers.New(),
			"a login already ended": phone.GetFamilyId(),
		} {
			answer, endErr := sub.Surfaces.SignIn.EndSignIn(ctx, &signinpb.EndSignInRequest{FamilyId: family})
			must.NoError(t, endErr, must.Sprintf("ending %s was refused", what))
			test.True(t, proto.Equal(ended, answer), test.Sprintf("ending %s answered differently", what))
		}

		_, err = exchange(t.Context(), anon, theirs.GetRefreshToken())
		test.NoError(t, err, test.Sprint("a caller ended somebody else's login by naming it"))
	})
}
