package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lateProfiles is a ProfileUpdater wired after the harness builds the identity
// Service it writes through.
type lateProfiles struct {
	target signin.ProfileUpdater
}

func (p *lateProfiles) UpdateProfile(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	update *identity.ProfileUpdate,
) (*identity.User, error) {
	return p.target.UpdateProfile(ctx, scope, userID, update)
}

// newHandleHarness is a refresh harness whose handle doors are on.
func newHandleHarness(t *testing.T) *harness {
	t.Helper()

	profiles := &lateProfiles{}
	h := newRefreshHarness(t, []signin.ServiceOption{signin.WithProfileUpdater(profiles)})
	profiles.target = h.directory

	return h
}

// janesLogin signs jane in and answers with the family her login is, which the
// wire does not carry and the listing does.
func (h *harness) janesLogin(t *testing.T) string {
	t.Helper()

	_, err := h.client.LoginForToken(h.rootCtx, &signinpb.LoginForTokenRequest{
		Credentials: &signinpb.Credentials{Username: "jane", Password: h.password},
	})
	must.NoError(t, err)

	signIns, err := h.svc.ListSignIns(t.Context(), testScope, h.user.ID, 0)
	must.NoError(t, err)
	must.SliceLen(t, 1, signIns)

	return signIns[0].FamilyID
}

func TestServer_UpdateEmailAddress(T *testing.T) {
	T.Parallel()

	T.Run("the current password moves the address", func(t *testing.T) {
		t.Parallel()

		h := newHandleHarness(t)

		response, err := h.client.UpdateEmailAddress(h.asJane(), &signinpb.UpdateEmailAddressRequest{
			CurrentPassword: h.password,
			NewEmailAddress: "jane.new@example.com",
		})
		must.NoError(t, err)
		test.EqOp(t, "jane.new@example.com", response.GetUser().GetEmailAddress())
	})

	// PermissionDenied rather than the Unauthenticated a wrong password is at
	// the sign-in door, because the caller's token is good and a client reads
	// Unauthenticated as one to refresh. The reason and the words are the
	// sign-in door's, so a client tells a wrong password from a missing code
	// exactly as it does there.
	T.Run("a wrong password is PermissionDenied, keeps its reason, and changes nothing", func(t *testing.T) {
		t.Parallel()

		h := newHandleHarness(t)

		_, err := h.client.UpdateEmailAddress(h.asJane(), &signinpb.UpdateEmailAddressRequest{
			CurrentPassword: "not it",
			NewEmailAddress: "thief@example.com",
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		test.EqOp(t, "INVALID_CREDENTIALS", reasonOf(err))
		test.EqOp(t, signin.ErrInvalidCredentials.Error(), status.Convert(err).Message())

		me, err := h.client.GetSelf(h.asJane(), &signinpb.GetSelfRequest{})
		must.NoError(t, err)
		test.EqOp(t, "jane@example.com", me.GetUser().GetEmailAddress())
	})

	T.Run("the login the principal names stands in for the password", func(t *testing.T) {
		t.Parallel()

		h := newHandleHarness(t)
		familyID := h.janesLogin(t)

		response, err := h.client.UpdateEmailAddress(asUserIn(h.rootCtx, h.user.ID, familyID),
			&signinpb.UpdateEmailAddressRequest{NewEmailAddress: "jane.new@example.com"})
		must.NoError(t, err)
		test.EqOp(t, "jane.new@example.com", response.GetUser().GetEmailAddress())
	})

	T.Run("a principal naming no login offers only the password", func(t *testing.T) {
		t.Parallel()

		h := newHandleHarness(t)
		h.janesLogin(t)

		_, err := h.client.UpdateEmailAddress(h.asJane(),
			&signinpb.UpdateEmailAddressRequest{NewEmailAddress: "thief@example.com"})
		test.ErrorIs(t, err, signin.ErrReauthenticationRequired)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		test.EqOp(t, "REAUTHENTICATION_REQUIRED", reasonOf(err))
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h := newHandleHarness(t)

		_, err := h.client.UpdateEmailAddress(h.rootCtx, &signinpb.UpdateEmailAddressRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
	})
}

func TestServer_UpdateUsername(T *testing.T) {
	T.Parallel()

	T.Run("the current password renames", func(t *testing.T) {
		t.Parallel()

		h := newHandleHarness(t)

		response, err := h.client.UpdateUsername(h.asJane(), &signinpb.UpdateUsernameRequest{
			CurrentPassword: h.password,
			NewUsername:     "jane2",
		})
		must.NoError(t, err)
		test.EqOp(t, "jane2", response.GetUser().GetUsername())
	})

	T.Run("a wrong password is refused", func(t *testing.T) {
		t.Parallel()

		h := newHandleHarness(t)

		_, err := h.client.UpdateUsername(h.asJane(), &signinpb.UpdateUsernameRequest{
			CurrentPassword: "not it",
			NewUsername:     "thief",
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h := newHandleHarness(t)

		_, err := h.client.UpdateUsername(h.rootCtx, &signinpb.UpdateUsernameRequest{})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
	})
}
