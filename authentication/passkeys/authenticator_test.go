package passkeys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/shoenig/test/must"
)

// A virtual authenticator, because the service is the order a real ceremony's
// steps run in, and the only way to assert on that order is a ceremony that
// completes.
//
// It is primitives-go's authentication/webauthn test authenticator, carried
// here rather than exported from there: a real ES256 key, a COSE public key,
// authenticator data, and a signature over the bytes the specification says to
// sign, registering with the "none" attestation format a passkey deployment
// asks for. It sets neither backup flag, in either ceremony.
const (
	flagUserPresent            = 0x01
	flagUserVerified           = 0x04
	flagAttestedCredentialData = 0x40

	// coseKeyType, coseAlgorithm, coseCurve are the COSE labels for an ES256
	// key on P-256: kty=EC2(2), alg=ES256(-7), crv=P-256(1).
	coseKeyType   = 2
	coseAlgorithm = -7
	coseCurve     = 1

	aaguidLength     = 16
	coordinateLength = 32

	testRPID   = "example.com"
	testOrigin = "https://example.com"
)

// virtualAuthenticator is one passkey on one device.
type virtualAuthenticator struct {
	key          *ecdsa.PrivateKey
	credentialID []byte
	handle       []byte
	signCount    uint32
}

// newAuthenticator mints a device holding one discoverable credential for the
// user behind handle.
func newAuthenticator(tb testing.TB, handle []byte) *virtualAuthenticator {
	tb.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must.NoError(tb, err)

	credentialID := make([]byte, 32)
	_, err = rand.Read(credentialID)
	must.NoError(tb, err)

	return &virtualAuthenticator{key: key, credentialID: credentialID, handle: handle, signCount: 1}
}

// register produces the attestation response a browser would POST to finish a
// registration ceremony for challenge.
func (a *virtualAuthenticator) register(tb testing.TB, challenge string) []byte {
	tb.Helper()

	clientData := clientData(tb, "webauthn.create", challenge)
	authData := a.authenticatorData(flagUserPresent|flagUserVerified|flagAttestedCredentialData, a.attestedCredentialData(tb))

	attestation, err := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": authData,
	})
	must.NoError(tb, err)

	return marshalResponse(tb, a.credentialID, map[string]any{
		"clientDataJSON":    encode(clientData),
		"attestationObject": encode(attestation),
		"transports":        []string{"internal", "hybrid"},
	})
}

// assert produces the assertion response a browser would POST to finish a
// login ceremony for challenge. It returns the user handle, as a discoverable
// credential does.
func (a *virtualAuthenticator) assert(tb testing.TB, challenge string) []byte {
	tb.Helper()

	a.signCount++

	clientData := clientData(tb, "webauthn.get", challenge)
	authData := a.authenticatorData(flagUserPresent|flagUserVerified, nil)

	clientDataHash := sha256.Sum256(clientData)
	signed := sha256.Sum256(append(append([]byte{}, authData...), clientDataHash[:]...))

	signature, err := ecdsa.SignASN1(rand.Reader, a.key, signed[:])
	must.NoError(tb, err)

	return marshalResponse(tb, a.credentialID, map[string]any{
		"clientDataJSON":    encode(clientData),
		"authenticatorData": encode(authData),
		"signature":         encode(signature),
		"userHandle":        encode(a.handle),
	})
}

// clientData renders the collected client data for one ceremony step.
func clientData(tb testing.TB, ceremony, challenge string) []byte {
	tb.Helper()

	data, err := json.Marshal(map[string]any{
		"type":        ceremony,
		"challenge":   challenge,
		"origin":      testOrigin,
		"crossOrigin": false,
	})
	must.NoError(tb, err)

	return data
}

// authenticatorData renders the relying party's hash, the flags, the counter,
// and — for a registration — the credential itself.
func (a *virtualAuthenticator) authenticatorData(flags byte, attested []byte) []byte {
	rpIDHash := sha256.Sum256([]byte(testRPID))

	data := make([]byte, 0, sha256.Size+1+4+len(attested))
	data = append(data, rpIDHash[:]...)
	data = append(data, flags)
	data = binary.BigEndian.AppendUint32(data, a.signCount)

	return append(data, attested...)
}

// attestedCredentialData renders the AAGUID, the credential ID, and the key.
func (a *virtualAuthenticator) attestedCredentialData(tb testing.TB) []byte {
	tb.Helper()

	data := make([]byte, aaguidLength)
	data = binary.BigEndian.AppendUint16(data, uint16(len(a.credentialID)))
	data = append(data, a.credentialID...)

	return append(data, a.coseKey(tb)...)
}

// coseKey renders the public key in the COSE encoding the specification
// requires.
func (a *virtualAuthenticator) coseKey(tb testing.TB) []byte {
	tb.Helper()

	encoder, err := cbor.CanonicalEncOptions().EncMode()
	must.NoError(tb, err)

	point, err := a.key.PublicKey.Bytes()
	must.NoError(tb, err)
	must.SliceLen(tb, 1+2*coordinateLength, point)

	key, err := encoder.Marshal(map[int]any{
		1:  coseKeyType,
		3:  coseAlgorithm,
		-1: coseCurve,
		-2: point[1 : 1+coordinateLength],
		-3: point[1+coordinateLength:],
	})
	must.NoError(tb, err)

	return key
}

// marshalResponse wraps one ceremony's response in the credential envelope the
// browser's WebAuthn API produces.
func marshalResponse(tb testing.TB, credentialID []byte, response map[string]any) []byte {
	tb.Helper()

	body, err := json.Marshal(map[string]any{
		"id":       encode(credentialID),
		"rawId":    encode(credentialID),
		"type":     "public-key",
		"response": response,
	})
	must.NoError(tb, err)

	return body
}

// encode renders bytes the way every field of a WebAuthn response is rendered.
func encode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
