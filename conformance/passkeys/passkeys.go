package passkeys

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn/webauthntest"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "passkeys"

// The calls this suite makes, as the names a caller is minted to make them by,
// or that a door is reached by with nobody on it.
const (
	archivePasskey     = passkeyspb.PasskeysService_ArchivePasskey_FullMethodName
	beginLogin         = passkeyspb.PasskeysService_BeginLogin_FullMethodName
	beginRegistration  = passkeyspb.PasskeysService_BeginRegistration_FullMethodName
	finishLogin        = passkeyspb.PasskeysService_FinishLogin_FullMethodName
	finishRegistration = passkeyspb.PasskeysService_FinishRegistration_FullMethodName
	listPasskeys       = passkeyspb.PasskeysService_ListPasskeys_FullMethodName
	getAuthStatus      = signinpb.SignInService_GetAuthStatus_FullMethodName
)

// selfService is every call an enrolled person makes about their own passkeys.
var selfService = []string{beginRegistration, finishRegistration, listPasskeys, archivePasskey}

// Suite is the passkey surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
		Mounted: func(s conformance.Surfaces) bool { return s.Passkeys != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	if s.Seams().WebAuthn == nil {
		conformance.Skip(t, "conformance: this subject names no WebAuthn relying party, so no authenticator can answer its ceremonies; skipping")
	}

	t.Run("registration", func(t *testing.T) {
		t.Parallel()
		registration(t, s)
	})
	t.Run("login", func(t *testing.T) {
		t.Parallel()
		login(t, s)
	})
	t.Run("confinement", func(t *testing.T) {
		t.Parallel()
		confinement(t, s)
	})
	t.Run("last passkey", func(t *testing.T) {
		t.Parallel()
		lastPasskey(t, s)
	})
}

// newDevice mints an authenticator answering as the deployment's relying
// party, verifying the person behind every tap.
func newDevice(t *testing.T, s *conformance.Session) *webauthntest.Authenticator {
	t.Helper()

	rp := s.Seams().WebAuthn

	return webauthntest.NewAuthenticator(t, rp.RPID, rp.Origin)
}

// enrollee mints a caller in the directory a passkey login is placed in,
// making the self-service calls and any extra. See the package documentation
// for why that directory is the global one.
func enrollee(t *testing.T, s *conformance.Session, extra ...string) *conformance.Subject {
	t.Helper()

	return s.Subject(t,
		conformance.Making(append(slices.Clone(selfService), extra...)...),
		conformance.InTenant(surface, tenancy.Global()))
}

// doors mints a caller whose connection the login half is reached through with
// nobody on the call, skipping where the subject reserves either door.
func doors(t *testing.T, s *conformance.Session) *conformance.Subject {
	t.Helper()

	s.NeedsPublic(t, beginLogin, finishLogin)

	return s.Subject(t, conformance.Making(beginLogin, finishLogin))
}

// options is the part of a begin's JSON the suite reads: the challenge every
// ceremony signs, and the user handle a registration hands out.
type options struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"publicKey"`
}

func parse(t *testing.T, raw []byte) options {
	t.Helper()

	var parsed options
	must.NoError(t, json.Unmarshal(raw, &parsed), must.Sprint("a begin answered with options that are not the WebAuthn JSON"))
	must.NotEqOp(t, "", parsed.PublicKey.Challenge, must.Sprint("a begin answered with no challenge"))

	return parsed
}

// enrolled is a passkey somebody registered: the device that holds it, what the
// surface answered, the response that registered it and the handle the
// options handed out.
type enrolled struct {
	device   *webauthntest.Authenticator
	passkey  *passkeyspb.Passkey
	response []byte
	handle   []byte
}

// enroll runs a whole registration as sub with a fresh device.
func enroll(t *testing.T, s *conformance.Session, sub *conformance.Subject) *enrolled {
	t.Helper()

	device := newDevice(t, s)
	ctx := sub.Context(t.Context())

	begun, err := sub.Surfaces.Passkeys.BeginRegistration(ctx, &passkeyspb.BeginRegistrationRequest{})
	must.NoError(t, err, must.Sprint("beginning a passkey registration"))

	parsed := parse(t, begun.GetOptions())

	handle, err := base64.RawURLEncoding.DecodeString(parsed.PublicKey.User.ID)
	must.NoError(t, err, must.Sprint("the registration options' user.id is not base64url"))
	must.SliceNotEmpty(t, handle, must.Sprint("the registration options carry no user handle"))

	response := device.Register(t, parsed.PublicKey.Challenge)

	finished, err := sub.Surfaces.Passkeys.FinishRegistration(ctx, &passkeyspb.FinishRegistrationRequest{
		FriendlyName: "Conformance key",
		Response:     response,
	})
	must.NoError(t, err, must.Sprint("finishing a passkey registration"))

	return &enrolled{device: device, passkey: finished.GetPasskey(), response: response, handle: handle}
}

// assertion begins a login for username — empty for a discoverable one — and
// answers with what device signs for it, carrying handle.
func assertion(t *testing.T, anon *conformance.Subject, username string, device *webauthntest.Authenticator, handle []byte) []byte {
	t.Helper()

	begun, err := anon.Surfaces.Passkeys.BeginLogin(t.Context(), &passkeyspb.BeginLoginRequest{Username: username})
	must.NoError(t, err, must.Sprint("beginning a passkey login"))

	return device.Assert(t, parse(t, begun.GetOptions()).PublicKey.Challenge, handle)
}

// finish answers a login with an assertion, with nobody on the call.
func finish(t *testing.T, anon *conformance.Subject, username string, response []byte) (*signinpb.IssuedToken, error) {
	t.Helper()

	finished, err := anon.Surfaces.Passkeys.FinishLogin(t.Context(), &passkeyspb.FinishLoginRequest{
		Username: username,
		Response: response,
	})
	if err != nil {
		return nil, err
	}

	return finished.GetToken(), nil
}

// signIn runs a whole discoverable login with the passkey and fails the test
// if it is refused.
func signIn(t *testing.T, anon *conformance.Subject, key *enrolled) *signinpb.IssuedToken {
	t.Helper()

	issued, err := finish(t, anon, "", assertion(t, anon, "", key.device, key.handle))
	must.NoError(t, err, must.Sprint("a passkey that was just enrolled did not sign its owner in"))
	must.NotEqOp(t, "", issued.GetToken(), must.Sprint("a passkey login answered with no token"))

	return issued
}

// neverIssued is a challenge no begin handed out.
func neverIssued(t *testing.T) string {
	t.Helper()

	challenge := make([]byte, 32)
	_, err := rand.Read(challenge)
	must.NoError(t, err)

	return base64.RawURLEncoding.EncodeToString(challenge)
}

// listed is how many of sub's live passkeys carry the credential ID.
func listed(t *testing.T, sub *conformance.Subject, credentialID []byte) int {
	t.Helper()

	response, err := sub.Surfaces.Passkeys.ListPasskeys(sub.Context(t.Context()), &passkeyspb.ListPasskeysRequest{})
	must.NoError(t, err, must.Sprint("listing the caller's passkeys"))

	count := 0

	for _, passkey := range response.GetPasskeys() {
		if slices.Equal(passkey.GetCredentialId(), credentialID) {
			count++
		}
	}

	return count
}
