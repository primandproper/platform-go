package webhooks

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func signingKeys(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The property this surface was written around: whatever a registration
	// stores for signing is not readable back over it. Asserted on the encoded
	// messages rather than on fields, and on every answer that renders the
	// endpoint, because a deployment that put its own projection in front of
	// this surface could render a keyring the proto has no field for.
	//
	// What a client cannot see is that the keys reached the database at all;
	// webhooks/grpc asserts that half, against the store.
	t.Run("no answer renders the signing keys an endpoint was registered with", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		keys := keyring()

		saved := register(t, caller, endpointFor(catalog(t, caller, 1)[0]), keys)

		page, err := caller.Surfaces.Webhooks.ListEndpoints(caller.Context(t.Context()), &webhookspb.ListEndpointsRequest{})
		must.NoError(t, err)

		answers := map[string]proto.Message{
			"SaveEndpoint":  saved,
			"GetEndpoint":   endpoint(t, caller, saved.GetId()),
			"ListEndpoints": page,
		}

		for rpc, answer := range answers {
			test.False(t, carries(t, answer, keys.GetCurrent()),
				test.Sprintf("the current signing key is readable through %s", rpc))
			test.False(t, carries(t, answer, keys.GetPrevious()),
				test.Sprintf("the previous signing key is readable through %s", rpc))
		}
	})

	// The whole reason rotation is an RPC rather than a save with a fresh
	// keyring: nothing comes back, so rolling a key obliges nobody to have been
	// able to read the one it replaces.
	t.Run("a rotation answers with neither the key it installed nor the one it replaced", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		keys := keyring()
		saved := register(t, caller, endpointFor(catalog(t, caller, 1)[0]), keys)

		rolled := rolledKey()

		rotated, err := caller.Surfaces.Webhooks.RotateSecret(caller.Context(t.Context()),
			&webhookspb.RotateSecretRequest{EndpointId: saved.GetId(), SigningKey: rolled})
		must.NoError(t, err)
		must.NotNil(t, rotated)

		test.False(t, carries(t, rotated, rolled), test.Sprint("the rotation's answer carries the key it installed"))
		test.False(t, carries(t, rotated, keys.GetCurrent()), test.Sprint("the rotation's answer carries the key it replaced"))

		// And the endpoint still renders none of them afterwards.
		after := endpoint(t, caller, saved.GetId())
		test.False(t, carries(t, after, rolled), test.Sprint("the rotated key is readable through GetEndpoint"))
	})

	t.Run("a rotation naming no key is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		saved := registered(t, caller, catalog(t, caller, 1)[0])

		// The positive control: the same endpoint rotates when a key is named,
		// so the refusal below is about the missing key.
		_, err := caller.Surfaces.Webhooks.RotateSecret(caller.Context(t.Context()), &webhookspb.RotateSecretRequest{
			EndpointId: saved.GetId(),
			SigningKey: rolledKey(),
		})
		must.NoError(t, err, must.Sprint("the caller cannot rotate their own endpoint; the refusal below proves nothing"))

		_, err = caller.Surfaces.Webhooks.RotateSecret(caller.Context(t.Context()),
			&webhookspb.RotateSecretRequest{EndpointId: saved.GetId()})
		refused(t, err, codes.InvalidArgument, "a rotation to no key")
	})

	// The answer a neighbor's endpoint gets too, which the confinement suite
	// asserts; this is the half that keeps that answer from disclosing that
	// the identifier named something.
	t.Run("rotating an identifier that names nothing is answered as absent", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := caller.Surfaces.Webhooks.RotateSecret(caller.Context(t.Context()), &webhookspb.RotateSecretRequest{
			EndpointId: absentID(),
			SigningKey: rolledKey(),
		})
		refused(t, err, codes.NotFound, "rotating an endpoint that does not exist")
	})
}
