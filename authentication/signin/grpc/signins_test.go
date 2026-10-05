package grpc_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// signInAsJane opens one login through the password door and answers with the
// token it issued.
func (h *harness) signInAsJane(t *testing.T) *signinpb.IssuedToken {
	t.Helper()

	signedIn, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
		Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
	})
	must.NoError(t, err)

	return signedIn.GetToken()
}

func TestServer_ListSignIns(T *testing.T) {
	T.Parallel()

	T.Run("lists the caller's logins and marks the current one", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		phone := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		listed, err := h.client.ListSignIns(asUserIn(h.rootCtx, h.user.ID, laptop.GetFamilyId()), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 2, listed.GetSignIns())

		current := map[string]bool{}
		for _, signIn := range listed.GetSignIns() {
			current[signIn.GetFamilyId()] = signIn.GetCurrent()

			test.NotNil(t, signIn.GetSignedInAt())
			test.NotNil(t, signIn.GetLastRefreshedAt())
			test.NotNil(t, signIn.GetExpiresAt())
			test.EqOp(t, h.accountID, signIn.GetActiveAccountId())
		}

		test.Eq(t, map[string]bool{phone.GetFamilyId(): false, laptop.GetFamilyId(): true}, current)
	})

	// A consumer whose principal does not carry the claim is told nothing it
	// could mistake for an answer.
	T.Run("marks nothing current when the principal names no login", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.GetSignIns())
		test.False(t, listed.GetSignIns()[0].GetCurrent())
	})

	T.Run("honors a limit", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		h.signInAsJane(t)
		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{Limit: 1})
		must.NoError(t, err)
		test.SliceLen(t, 1, listed.GetSignIns())
	})

	// The subject is the caller, so another user's logins are not reachable by
	// asking as them.
	T.Run("lists nobody else's logins", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(asUser(h.rootCtx, "somebody_else"), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		test.SliceEmpty(t, listed.GetSignIns())
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		_, err := h.client.ListSignIns(h.rootCtx, &signinpb.ListSignInsRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	// A service that stores no refresh tokens has no logins to list, and says
	// so as the wiring failure it is.
	T.Run("a service that stores no refresh tokens is a server error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{})
		test.ErrorIs(t, err, signin.ErrRefreshTokensNotConfigured)
		test.EqOp(t, codes.Internal, status.Code(err))
	})
}

func TestServer_EndSignIn(T *testing.T) {
	T.Parallel()

	// The lost-phone case: the login is ended from another device, by name,
	// with no refresh token of its own in hand.
	T.Run("ends one of the caller's logins by name", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		phone := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		_, err := h.client.EndSignIn(asUserIn(h.rootCtx, h.user.ID, laptop.GetFamilyId()), &signinpb.EndSignInRequest{
			FamilyId: phone.GetFamilyId(),
		})
		must.NoError(t, err)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: phone.GetRefreshToken(),
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: laptop.GetRefreshToken(),
		})
		test.NoError(t, err)
	})

	// A family identifier is not a secret. Somebody else presenting Jane's gets
	// the answer an unknown one gets, and Jane stays signed in.
	T.Run("cannot end somebody else's login", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		janes := h.signInAsJane(t)

		_, err := h.client.EndSignIn(asUser(h.rootCtx, "somebody_else"), &signinpb.EndSignInRequest{
			FamilyId: janes.GetFamilyId(),
		})
		must.NoError(t, err)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: janes.GetRefreshToken(),
		})
		test.NoError(t, err)
	})

	T.Run("a request naming no login is invalid", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		_, err := h.client.EndSignIn(h.asJane(), &signinpb.EndSignInRequest{})
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		_, err := h.client.EndSignIn(h.rootCtx, &signinpb.EndSignInRequest{FamilyId: "family"})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestServer_EndOtherSignIns(T *testing.T) {
	T.Parallel()

	// The login kept is the one the request came through, read off the
	// principal; every other one of the caller's ends.
	T.Run("ends every login but the one asking", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		phone := h.signInAsJane(t)
		tablet := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		_, err := h.client.EndOtherSignIns(asUserIn(h.rootCtx, h.user.ID, laptop.GetFamilyId()), &signinpb.EndOtherSignInsRequest{})
		must.NoError(t, err)

		for _, ended := range []*signinpb.IssuedToken{phone, tablet} {
			_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
				RefreshToken: ended.GetRefreshToken(),
			})
			test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		}

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: laptop.GetRefreshToken(),
		})
		test.NoError(t, err)
	})

	// The refusal this RPC is built around. ListSignIns marks nothing when it
	// is not told which login is asking; a sign-out cannot, because "keep
	// nothing" is every login ending.
	T.Run("refuses a principal that names no login and ends nothing", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		janes := h.signInAsJane(t)

		_, err := h.client.EndOtherSignIns(h.asJane(), &signinpb.EndOtherSignInsRequest{})
		test.ErrorIs(t, err, signin.ErrSignInNotIdentified)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))

		info, ok := grpcerrors.ClientReasonFromStatus(err)
		must.True(t, ok)
		test.EqOp(t, "SIGN_IN_NOT_IDENTIFIED", info.GetReason())
		test.EqOp(t, signin.ClientReasonDomain, info.GetDomain())

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: janes.GetRefreshToken(),
		})
		test.NoError(t, err)
	})

	// The subject is the caller, so somebody else asking ends nothing of Jane's
	// whichever family their token names.
	T.Run("ends nobody else's logins", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		janes := h.signInAsJane(t)

		_, err := h.client.EndOtherSignIns(asUserIn(h.rootCtx, "somebody_else", "family_theirs"), &signinpb.EndOtherSignInsRequest{})
		must.NoError(t, err)

		_, err = h.client.ExchangeRefreshToken(h.rootCtx, &signinpb.ExchangeRefreshTokenRequest{
			RefreshToken: janes.GetRefreshToken(),
		})
		test.NoError(t, err)
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		_, err := h.client.EndOtherSignIns(h.rootCtx, &signinpb.EndOtherSignInsRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

// annotatorCall is what a SignInAnnotator was asked.
type annotatorCall struct {
	scope     tenancy.Scope
	userID    string
	familyIDs []string
}

// recordingAnnotator answers with what it was given and records every call,
// standing in for a consumer's device table keyed on the family.
type recordingAnnotator struct {
	answer map[string]map[string]string
	err    error
	calls  []annotatorCall
	mu     sync.Mutex
}

func (a *recordingAnnotator) annotate(
	_ context.Context,
	scope tenancy.Scope,
	userID string,
	familyIDs []string,
) (map[string]map[string]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.calls = append(a.calls, annotatorCall{scope: scope, userID: userID, familyIDs: slices.Clone(familyIDs)})

	return a.answer, a.err
}

func (a *recordingAnnotator) heard() []annotatorCall {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.calls)
}

func TestServer_ListSignIns_Annotation(T *testing.T) {
	T.Parallel()

	// The platform's half of the screen is on the wire whether or not a
	// consumer annotates: how the login happened is signin's to say.
	T.Run("says how each login happened, and carries no attributes without an annotator", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil)

		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.GetSignIns())
		test.EqOp(t, string(signin.CredentialKindPassword), listed.GetSignIns()[0].GetCredentialKind())
		test.MapEmpty(t, listed.GetSignIns()[0].GetAttributes())
	})

	T.Run("fills each login's attributes from the annotator's answer", func(t *testing.T) {
		t.Parallel()

		annotator := &recordingAnnotator{}
		h := newRefreshHarness(t, nil, signingrpc.WithSignInAnnotator(annotator.annotate))

		phone := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		annotator.answer = map[string]map[string]string{
			phone.GetFamilyId(): {"device": "Jane's phone", "user_agent": "Mobile Safari"},
			// A family the listing did not return is ignored rather than
			// invented into an entry.
			"family_not_listed": {"device": "somebody else's"},
		}

		listed, err := h.client.ListSignIns(asUserIn(h.rootCtx, h.user.ID, laptop.GetFamilyId()), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 2, listed.GetSignIns())

		attributes := map[string]map[string]string{}
		for _, signIn := range listed.GetSignIns() {
			attributes[signIn.GetFamilyId()] = signIn.GetAttributes()
		}

		test.Eq(t, map[string]string{"device": "Jane's phone", "user_agent": "Mobile Safari"}, attributes[phone.GetFamilyId()])
		test.MapEmpty(t, attributes[laptop.GetFamilyId()], test.Sprint("a login the annotator had nothing for was given something"))

		// One call for the whole listing, about the caller, in the listing's
		// scope, naming every family it returned.
		calls := annotator.heard()
		must.SliceLen(t, 1, calls)
		test.EqOp(t, testScope, calls[0].scope)
		test.EqOp(t, h.user.ID, calls[0].userID)
		test.SliceContainsAll(t, []string{phone.GetFamilyId(), laptop.GetFamilyId()}, calls[0].familyIDs)
	})

	// No silent half-answer: a screen listing the logins and none of their
	// devices would look like an answer and not be one.
	T.Run("an annotator that fails fails the listing", func(t *testing.T) {
		t.Parallel()

		annotator := &recordingAnnotator{err: platformerrors.New("device table unreachable")}
		h := newRefreshHarness(t, nil, signingrpc.WithSignInAnnotator(annotator.annotate))

		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{})
		test.Nil(t, listed)
		test.EqOp(t, codes.Internal, status.Code(err))
		test.SliceLen(t, 1, annotator.heard())
	})

	T.Run("is not asked about a listing with no logins in it", func(t *testing.T) {
		t.Parallel()

		annotator := &recordingAnnotator{err: platformerrors.New("should not have been asked")}
		h := newRefreshHarness(t, nil, signingrpc.WithSignInAnnotator(annotator.annotate))

		listed, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		test.SliceEmpty(t, listed.GetSignIns())
		test.SliceEmpty(t, annotator.heard())
	})

	T.Run("a nil annotator is ignored", func(t *testing.T) {
		t.Parallel()

		h := newRefreshHarness(t, nil, signingrpc.WithSignInAnnotator(nil))

		h.signInAsJane(t)

		listed, err := h.client.ListSignIns(h.asJane(), &signinpb.ListSignInsRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.GetSignIns())
		test.MapEmpty(t, listed.GetSignIns()[0].GetAttributes())
	})

	// The operator's view of somebody else's logins is annotated about the
	// user it names, not about the operator asking.
	T.Run("annotates an operator's listing about the user it names", func(t *testing.T) {
		t.Parallel()

		annotator := &recordingAnnotator{}
		h := newRefreshHarness(t, nil, signingrpc.WithSignInAnnotator(annotator.annotate))

		phone := h.signInAsJane(t)
		annotator.answer = map[string]map[string]string{phone.GetFamilyId(): {"device": "Jane's phone"}}

		listed, err := h.admin.ListSignInsForUser(asUser(h.rootCtx, operatorID),
			&signinpb.ListSignInsForUserRequest{UserId: h.user.ID})
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.GetSignIns())
		test.Eq(t, map[string]string{"device": "Jane's phone"}, listed.GetSignIns()[0].GetAttributes())
		test.EqOp(t, string(signin.CredentialKindPassword), listed.GetSignIns()[0].GetCredentialKind())

		calls := annotator.heard()
		must.SliceLen(t, 1, calls)
		test.EqOp(t, h.user.ID, calls[0].userID)
		test.Eq(t, []string{phone.GetFamilyId()}, calls[0].familyIDs)
	})

	T.Run("an annotator that fails fails an operator's listing", func(t *testing.T) {
		t.Parallel()

		annotator := &recordingAnnotator{err: platformerrors.New("device table unreachable")}
		h := newRefreshHarness(t, nil, signingrpc.WithSignInAnnotator(annotator.annotate))

		h.signInAsJane(t)

		_, err := h.admin.ListSignInsForUser(asUser(h.rootCtx, operatorID),
			&signinpb.ListSignInsForUserRequest{UserId: h.user.ID})
		test.EqOp(t, codes.Internal, status.Code(err))
	})
}
