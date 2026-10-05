package passkeys

import (
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/conformance"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func registration(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an enrolled passkey is its owner's, listed once", func(t *testing.T) {
		t.Parallel()

		sub := enrollee(t, s)
		key := enroll(t, s, sub)

		test.Eq(t, key.device.CredentialID(), key.passkey.GetCredentialId())
		test.EqOp(t, 1, listed(t, sub, key.device.CredentialID()))
	})

	// The only over-the-wire proof that a registration's challenge is spent:
	// the same attestation a second time is refused, and the credential it
	// registered is still there exactly once rather than twice.
	t.Run("a replayed attestation registers nothing new", func(t *testing.T) {
		t.Parallel()

		sub := enrollee(t, s)
		key := enroll(t, s, sub)

		_, err := sub.Surfaces.Passkeys.FinishRegistration(sub.Context(t.Context()), &passkeyspb.FinishRegistrationRequest{
			FriendlyName: "Conformance key",
			Response:     key.response,
		})
		test.Error(t, err, test.Sprint("an attestation replayed after its challenge was spent registered again"))

		test.EqOp(t, 1, listed(t, sub, key.device.CredentialID()))
	})
}

func login(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The reason the surface exists: a passkey sign-in answers with the token a
	// password sign-in does, and that token is somebody — the registrant.
	t.Run("a passkey signs its owner in to a working token", func(t *testing.T) {
		t.Parallel()

		sub := enrollee(t, s)
		key := enroll(t, s, sub)
		anon := doors(t, s)

		issued := signIn(t, anon, key)

		if sub.Surfaces.SignIn == nil {
			conformance.Skip(t, "conformance: this subject mounts no sign-in surface, so whom the token names cannot be read")
		}

		caller := s.SignedIn(t, issued, conformance.Making(getAuthStatus))

		answered, err := caller.Surfaces.SignIn.GetAuthStatus(caller.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
		must.NoError(t, err, must.Sprint("the token a passkey login issued could not ask who it is"))
		test.True(t, answered.GetAuthenticated(), test.Sprint("the token a passkey login issued is nobody"))
		test.EqOp(t, sub.UserID, answered.GetStatus().GetUser().GetId())
	})

	// A named login, and the defense it owes: a username nobody holds is
	// answered with options the same shape as one somebody holds.
	t.Run("a named login signs in, and an unknown username begins like a known one", func(t *testing.T) {
		t.Parallel()

		sub := enrollee(t, s, getAuthStatus)
		key := enroll(t, s, sub)
		anon := doors(t, s)

		username := usernameOf(t, sub)

		_, err := finish(t, anon, username, assertion(t, anon, username, key.device, nil))
		must.NoError(t, err, must.Sprint("a named passkey login refused the passkey's owner"))

		test.EqOp(t, shape(t, anon, username), shape(t, anon, "conf-nobody-by-this-name"))
	})

	// The only over-the-wire proof of durable single consumption of a login's
	// challenge. The first answer succeeds, which is the control.
	t.Run("a replayed assertion is refused", func(t *testing.T) {
		t.Parallel()

		key := enroll(t, s, enrollee(t, s))
		anon := doors(t, s)

		response := assertion(t, anon, "", key.device, key.handle)

		_, err := finish(t, anon, "", response)
		must.NoError(t, err, must.Sprint("a fresh assertion was refused, so a refused replay would prove nothing"))

		_, err = finish(t, anon, "", response)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})

	t.Run("a challenge nobody issued is refused", func(t *testing.T) {
		t.Parallel()

		key := enroll(t, s, enrollee(t, s))
		anon := doors(t, s)

		_, err := finish(t, anon, "", key.device.Assert(t, neverIssued(t), key.handle))
		test.EqOp(t, codes.Unauthenticated, status.Code(err))

		signIn(t, anon, key)
	})

	// Ruled: a clone is refused, and no token is issued. The copy's counter
	// stops where the original's was when it was copied, so once the original
	// has signed in the copy's next assertion carries a counter that did not
	// advance.
	t.Run("a cloned authenticator is refused, and the original still signs in", func(t *testing.T) {
		t.Parallel()

		key := enroll(t, s, enrollee(t, s))
		anon := doors(t, s)

		clone := key.device.Clone()

		signIn(t, anon, key)

		_, err := finish(t, anon, "", assertion(t, anon, "", clone, key.handle))
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		signIn(t, anon, key)
	})
}

// usernameOf is the name sub signs in with, read the way a client reads it.
func usernameOf(t *testing.T, sub *conformance.Subject) string {
	t.Helper()

	if sub.Surfaces.SignIn == nil {
		conformance.Skip(t, "conformance: this subject mounts no sign-in surface, so a caller's username cannot be read for a named login")
	}

	answered, err := sub.Surfaces.SignIn.GetAuthStatus(sub.Context(t.Context()), &signinpb.GetAuthStatusRequest{})
	must.NoError(t, err, must.Sprint("a caller could not read its own status"))

	username := answered.GetStatus().GetUser().GetUsername()
	must.NotEqOp(t, "", username, must.Sprint("the caller has no username to sign in with"))

	return username
}

// shape is a named login's options with the challenge taken out, which is
// everything a caller could compare two answers by.
func shape(t *testing.T, anon *conformance.Subject, username string) string {
	t.Helper()

	begun, err := anon.Surfaces.Passkeys.BeginLogin(t.Context(), &passkeyspb.BeginLoginRequest{Username: username})
	must.NoError(t, err, must.Sprintf("beginning a named login for %q", username))

	var rendered map[string]map[string]any
	must.NoError(t, json.Unmarshal(begun.GetOptions(), &rendered))

	delete(rendered["publicKey"], "challenge")

	out, err := json.Marshal(rendered)
	must.NoError(t, err)

	return string(out)
}

func confinement(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a colleague cannot archive somebody else's passkey", func(t *testing.T) {
		t.Parallel()

		key := enroll(t, s, enrollee(t, s))
		colleague := s.Subject(t, conformance.Making(archivePasskey), conformance.InTenant(surface, tenancy.Global()))

		_, err := colleague.Surfaces.Passkeys.ArchivePasskey(colleague.Context(t.Context()),
			&passkeyspb.ArchivePasskeyRequest{Id: key.passkey.GetId()})
		test.EqOp(t, codes.NotFound, status.Code(err))

		// And it still signs its owner in, which is what says nothing was
		// archived rather than that the refusal was the only thing that
		// happened.
		signIn(t, doors(t, s), key)
	})
}
