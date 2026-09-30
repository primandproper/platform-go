package grpc_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passkeys"
	passkeysgrpc "github.com/primandproper/platform-go/v14/authentication/passkeys/grpc"
	"github.com/primandproper/platform-go/v14/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn/webauthntest"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// enroll runs a whole registration for user with device, and answers with the
// passkey it enrolled and the handle the options handed out.
func (h *harness) enroll(t *testing.T, user *identity.User, device *webauthntest.Authenticator) (enrolled *passkeyspb.Passkey, handle []byte) {
	t.Helper()

	ctx := asUser(t.Context(), user.ID)

	begun, err := h.client.BeginRegistration(ctx, &passkeyspb.BeginRegistrationRequest{})
	must.NoError(t, err)

	options := parseOptions(t, begun.GetOptions())

	finished, err := h.client.FinishRegistration(ctx, &passkeyspb.FinishRegistrationRequest{
		FriendlyName: "Phone",
		Response:     device.Register(t, options.PublicKey.Challenge),
	})
	must.NoError(t, err)

	return finished.GetPasskey(), options.handle(t)
}

// assertion begins a login for username (empty for a discoverable one) and
// answers with what device signs for it.
func (h *harness) assertion(t *testing.T, username string, device *webauthntest.Authenticator, handle []byte) []byte {
	t.Helper()

	begun, err := h.client.BeginLogin(t.Context(), &passkeyspb.BeginLoginRequest{Username: username})
	must.NoError(t, err)

	return device.Assert(t, parseOptions(t, begun.GetOptions()).PublicKey.Challenge, handle)
}

func (h *harness) finish(t *testing.T, username string, response []byte, totpCode string) (*passkeyspb.FinishLoginResponse, error) {
	t.Helper()

	return h.client.FinishLogin(t.Context(), &passkeyspb.FinishLoginRequest{
		Username: username,
		Response: response,
		TotpCode: totpCode,
	})
}

func TestNewServer(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for name, build := range map[string]func() (*passkeysgrpc.Server, error){
		"nil service": func() (*passkeysgrpc.Server, error) {
			return passkeysgrpc.NewServer(nil, h.db, &signin.Service{}, extractPrincipal)
		},
		"nil database client": func() (*passkeysgrpc.Server, error) {
			return passkeysgrpc.NewServer(&passkeys.Service{}, nil, &signin.Service{}, extractPrincipal)
		},
		"nil issuer": func() (*passkeysgrpc.Server, error) {
			return passkeysgrpc.NewServer(&passkeys.Service{}, h.db, nil, extractPrincipal)
		},
		"nil extractor": func() (*passkeysgrpc.Server, error) {
			return passkeysgrpc.NewServer(&passkeys.Service{}, h.db, &signin.Service{}, nil)
		},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			srv, err := build()
			test.Nil(t, srv)
			test.Error(t, err)
		})
	}
}

func TestRegisterThenSignIn(T *testing.T) {
	T.Parallel()

	T.Run("a named login answers with sign-in's token, stamped passkey", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		device := newDevice(t)

		enrolled, _ := h.enroll(t, h.jane, device)
		test.EqOp(t, "Phone", enrolled.GetFriendlyName())
		test.Eq(t, device.CredentialID(), enrolled.GetCredentialId())

		listed, err := h.client.ListPasskeys(asUser(t.Context(), h.jane.ID), &passkeyspb.ListPasskeysRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 1, listed.GetPasskeys())
		test.EqOp(t, enrolled.GetId(), listed.GetPasskeys()[0].GetId())

		signedIn, err := h.finish(t, "jane", h.assertion(t, "jane", device, nil), "")
		must.NoError(t, err)
		test.EqOp(t, "token-for-"+h.jane.ID, signedIn.GetToken().GetToken())
		test.Eq(t, []signin.CredentialKind{passkeysgrpc.CredentialKind}, h.signIns.recorded())
	})

	T.Run("a discoverable login is named by the handle registration handed out", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		device := newDevice(t)

		_, handle := h.enroll(t, h.jane, device)
		test.Eq(t, []byte(h.jane.ID), handle)

		signedIn, err := h.finish(t, "", h.assertion(t, "", device, handle), "")
		must.NoError(t, err)
		test.EqOp(t, "token-for-"+h.jane.ID, signedIn.GetToken().GetToken())
	})

	T.Run("the self-service half requires a caller", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.BeginRegistration(t.Context(), &passkeyspb.BeginRegistrationRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))

		_, err = h.client.ListPasskeys(t.Context(), &passkeyspb.ListPasskeysRequest{})
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	T.Run("a handle that names somebody else enrolls nothing", func(t *testing.T) {
		t.Parallel()

		var bobID string

		h := newHarnessWith(t, []passkeysgrpc.Option{
			passkeysgrpc.WithUserHandle(func(context.Context, tenancy.Scope, string) ([]byte, error) {
				return []byte(bobID), nil
			}),
		})
		bobID = h.bob.ID

		_, err := h.client.BeginRegistration(asUser(t.Context(), h.jane.ID), &passkeyspb.BeginRegistrationRequest{})
		test.EqOp(t, codes.Internal, status.Code(err))
		test.ErrorIs(t, err, passkeys.ErrHandleMismatch)
	})
}

func TestUnknownUsernameLooksLikeAKnownOne(T *testing.T) {
	T.Parallel()

	h := newHarness(T)
	h.enroll(T, h.jane, newDevice(T))

	shape := func(username string) string {
		begun, err := h.client.BeginLogin(T.Context(), &passkeyspb.BeginLoginRequest{Username: username})
		must.NoError(T, err)

		var options map[string]map[string]any
		must.NoError(T, json.Unmarshal(begun.GetOptions(), &options))

		delete(options["publicKey"], "challenge")

		rendered, err := json.Marshal(options)
		must.NoError(T, err)

		return string(rendered)
	}

	test.EqOp(T, shape("jane"), shape("nobody-by-this-name"))
}

func TestLoginRefusals(T *testing.T) {
	T.Parallel()

	T.Run("a replayed assertion is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		device := newDevice(t)
		h.enroll(t, h.jane, device)

		response := h.assertion(t, "jane", device, nil)

		_, err := h.finish(t, "jane", response, "")
		must.NoError(t, err)

		_, err = h.finish(t, "jane", response, "")
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
		test.ErrorIs(t, err, passkeys.ErrLoginFailed)
	})

	T.Run("a cloned key is refused and the original still signs in", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		device := newDevice(t)
		h.enroll(t, h.jane, device)

		clone := device.Clone()

		_, err := h.finish(t, "jane", h.assertion(t, "jane", device, nil), "")
		must.NoError(t, err)

		_, err = h.finish(t, "jane", h.assertion(t, "jane", clone, nil), "")
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		test.ErrorIs(t, err, passkeys.ErrSignCountRegressed)

		_, err = h.finish(t, "jane", h.assertion(t, "jane", device, nil), "")
		test.NoError(t, err)
	})

	T.Run("somebody else's passkey does not sign in as the username", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		janes := newDevice(t)
		bobs := newDevice(t)
		h.enroll(t, h.jane, janes)
		h.enroll(t, h.bob, bobs)

		_, err := h.finish(t, "jane", h.assertion(t, "jane", bobs, nil), "")
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	T.Run("a named login where only the discoverable one is offered", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, passkeys.WithUsernameResolver(nil))

		_, err := h.client.BeginLogin(t.Context(), &passkeyspb.BeginLoginRequest{Username: "jane"})
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestSecondFactor(T *testing.T) {
	T.Parallel()

	T.Run("a verified passkey is two factors on its own", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.enrollTOTP(t, h.jane)

		device := newDevice(t)
		h.enroll(t, h.jane, device)

		_, err := h.finish(t, "jane", h.assertion(t, "jane", device, nil), "")
		test.NoError(t, err)
	})

	T.Run("a key tap alone is asked for the second factor", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		secret := h.enrollTOTP(t, h.jane)

		device := newDevice(t, webauthntest.WithoutUserVerification())
		h.enroll(t, h.jane, device)

		_, err := h.finish(t, "jane", h.assertion(t, "jane", device, nil), "")
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
		test.ErrorIs(t, err, signin.ErrSecondFactorRequired)

		_, err = h.finish(t, "jane", h.assertion(t, "jane", device, nil), totpCode(t, secret))
		test.NoError(t, err)
	})
}

func TestArchivePasskey(T *testing.T) {
	T.Parallel()

	T.Run("confined to the owner", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, passkeys.WithoutLastCredentialGuard())
		device := newDevice(t)
		enrolled, _ := h.enroll(t, h.jane, device)

		_, err := h.client.ArchivePasskey(asUser(t.Context(), h.bob.ID), &passkeyspb.ArchivePasskeyRequest{Id: enrolled.GetId()})
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.ErrorIs(t, err, passkeys.ErrCredentialNotFound)

		_, err = h.finish(t, "jane", h.assertion(t, "jane", device, nil), "")
		test.NoError(t, err)

		archived, err := h.client.ArchivePasskey(asUser(t.Context(), h.jane.ID), &passkeyspb.ArchivePasskeyRequest{Id: enrolled.GetId()})
		must.NoError(t, err)
		test.NotNil(t, archived.GetPasskey().GetArchivedAt())

		_, err = h.finish(t, "jane", h.assertion(t, "jane", device, nil), "")
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	T.Run("the last passkey of a passwordless user stays", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		device := newDevice(t)
		enrolled, _ := h.enroll(t, h.jane, device)

		_, err := h.client.ArchivePasskey(asUser(t.Context(), h.jane.ID), &passkeyspb.ArchivePasskeyRequest{Id: enrolled.GetId()})
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
		test.ErrorIs(t, err, passkeys.ErrLastCredential)

		second, _ := h.enroll(t, h.jane, newDevice(t))

		_, err = h.client.ArchivePasskey(asUser(t.Context(), h.jane.ID), &passkeyspb.ArchivePasskeyRequest{Id: second.GetId()})
		test.NoError(t, err)
	})
}
