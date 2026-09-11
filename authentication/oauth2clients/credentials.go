package oauth2clients

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The byte lengths of the two credentials this package mints.
//
// ClientIDByteLength is 16 — 128 bits, rendered as 32 hex characters. A
// client_id is an identifier rather than a secret: it travels in every
// /authorize URL, appears in browser history and in server logs, and is meant
// to. What it must be is unguessable enough that nobody enumerates the registry
// and unique enough that the index never collides, and 128 bits is both.
//
// SecretByteLength is 32, which is oauth2server.CredentialByteLength and is
// deliberately the same number: a secret this package mints is verified by that
// package's /token endpoint, so the two are one credential and it would be
// strange for the strength to depend on which of them created it.
const (
	ClientIDByteLength = 16
	SecretByteLength   = oauth2server.CredentialByteLength
)

// CredentialGenerator mints the pair a registration is issued with: the
// identifier the client sends, and the secret it authenticates with.
//
// It is a seam for tests that need a credential they can predict, and for a
// deployment holding its randomness somewhere this package cannot reach. There
// is deliberately no option that shortens what the default produces — a knob
// whose only use is weakening a credential is a knob a misconfiguration can
// reach.
type CredentialGenerator func() (clientID, secret string, err error)

// generateCredentials is the default [CredentialGenerator]: crypto/rand, hex
// encoded.
//
// Hex rather than base64url, because a client_id is pasted into configuration
// files and read aloud over support calls, and hex has no case sensitivity and
// no characters a shell or a URL will argue about. The entropy is in the byte
// count, not the alphabet.
func generateCredentials() (clientID, secret string, err error) {
	id, err := randomHex(ClientIDByteLength)
	if err != nil {
		return "", "", platformerrors.Wrap(err, "generating an oauth2 client identifier")
	}

	value, err := randomHex(SecretByteLength)
	if err != nil {
		return "", "", platformerrors.Wrap(err, "generating an oauth2 client secret")
	}

	return id, value, nil
}

// randomHex reads n bytes from crypto/rand and renders them as hex.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", platformerrors.Wrap(ErrSecretGeneration, err.Error())
	}

	return hex.EncodeToString(buf), nil
}

// hashSecret digests a client secret for storage.
//
// It is oauth2server.Hash and not a second implementation, which is the whole
// of the point: a registration written here is authenticated by that package's
// /token endpoint against that package's digest. Two encodings of "hex of
// sha256 of a client secret" could drift, and the symptom would be every client
// this registry issued failing to authenticate for a reason neither package's
// tests would show.
func hashSecret(secret string) string { return oauth2server.Hash(secret) }

// validateClient checks a registration is one the authorization server could
// actually use, before it is written.
//
// The two identifiers and the digest are checked because a row missing one of
// them is a row nothing can resolve, authenticate, or address — states the
// schema's NOT NULL would let through, since the empty string is a value.
func validateClient(client *Client) error {
	switch {
	case client.ID == "":
		return ErrEmptyID
	case client.ClientID == "":
		return ErrEmptyClientID
	case client.SecretHash == "":
		return platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty oauth2 client secret hash")
	}

	return validateDescriptive(client.Name, client.RedirectURIs)
}

// validateDescriptive checks the fields a create and an update both supply.
//
// The redirect URIs go through oauth2server.ValidateRedirectURI — the same
// function the authorization server applies at /authorize — so this registry
// cannot accept a URI that server would later refuse. Checking it here is the
// only place the failure is legible: later it is a client that "does not work".
func validateDescriptive(name string, redirectURIs []string) error {
	if name == "" {
		return ErrEmptyName
	}

	if len(redirectURIs) == 0 {
		return ErrNoRedirectURIs
	}

	for _, uri := range redirectURIs {
		if err := oauth2server.ValidateRedirectURI(uri); err != nil {
			return platformerrors.Wrapf(ErrInvalidRedirectURI, "redirect URI %q: %s", uri, err)
		}
	}

	return nil
}
