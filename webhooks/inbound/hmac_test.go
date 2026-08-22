package inbound

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	"github.com/primandproper/platform-go/v13/cryptography/hashing/hmac"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// gitHubDocSecret, gitHubDocBody, and gitHubDocSignature are the worked example from GitHub's
// own webhook documentation. They pin this implementation to a published vector rather than to
// whatever it happened to produce first, which is the only way to know the scheme is right
// rather than merely self-consistent.
const (
	gitHubDocSecret    = "It's a Secret to Everybody"
	gitHubDocBody      = "Hello, World!"
	gitHubDocSignature = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
)

// signedHeaders builds a header bag carrying signature under name.
func signedHeaders(name, signature string) http.Header {
	headers := http.Header{}
	headers.Set(name, signature)

	return headers
}

func TestNewGitHubVerifier(T *testing.T) {
	T.Parallel()

	T.Run("matches GitHub's published example", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewGitHubVerifier(gitHubDocSecret)
		must.NoError(t, err)

		test.EqOp(t, providerGitHub, verifier.Provider())
		test.NoError(t, verifier.Verify(
			t.Context(),
			signedHeaders(GitHubSignatureHeader, gitHubDocSignature),
			[]byte(gitHubDocBody),
		))
	})

	T.Run("accepts an uppercase hex digest", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewGitHubVerifier(gitHubDocSecret)
		must.NoError(t, err)

		upper := "sha256=757107EA0EB2509FC211221CCE984B8A37570B6D7586C22C46F4379C8B043E17"

		test.NoError(t, verifier.Verify(t.Context(), signedHeaders(GitHubSignatureHeader, upper), []byte(gitHubDocBody)))
	})

	T.Run("rejects a body it did not sign", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewGitHubVerifier(gitHubDocSecret)
		must.NoError(t, err)

		test.ErrorIs(t,
			verifier.Verify(t.Context(), signedHeaders(GitHubSignatureHeader, gitHubDocSignature), []byte("Hello, World?")),
			ErrInvalidSignature,
		)
	})

	// GitHub still sends the SHA-1 X-Hub-Signature alongside the SHA-256 one. Reading it would
	// let the caller choose which algorithm this endpoint's security rests on.
	T.Run("ignores the legacy SHA-1 header", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewGitHubVerifier(gitHubDocSecret)
		must.NoError(t, err)

		headers := http.Header{}
		headers.Set("X-Hub-Signature", "sha1=whatever")

		test.ErrorIs(t, verifier.Verify(t.Context(), headers, []byte(gitHubDocBody)), ErrInvalidSignature)
	})

	T.Run("refuses to build without a secret", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewGitHubVerifier("")

		test.ErrorIs(t, err, ErrNoSecret)
		test.Nil(t, verifier)
	})
}

func TestNewHMACVerifier(T *testing.T) {
	T.Parallel()

	scheme := &HMACScheme{Provider: "acme", Header: "X-Acme-Signature"}

	T.Run("verifies a hex digest with no prefix", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewHMACVerifier(scheme, "sekrit")
		must.NoError(t, err)

		body := []byte(`{"hello":"world"}`)
		signature := hashing.Hex(hmac.NewHMACSHA256Hasher([]byte("sekrit")), body)

		test.EqOp(t, "acme", verifier.Provider())
		test.NoError(t, verifier.Verify(t.Context(), signedHeaders(scheme.Header, signature), body))
	})

	T.Run("verifies a base64 SHA-512 digest", func(t *testing.T) {
		t.Parallel()

		b64Scheme := &HMACScheme{
			Provider: "acme",
			Header:   "X-Acme-Signature",
			Digest:   DigestSHA512,
			Encoding: EncodingBase64,
		}

		verifier, err := NewHMACVerifier(b64Scheme, "sekrit")
		must.NoError(t, err)

		body := []byte(`{"hello":"world"}`)
		signature := base64.StdEncoding.EncodeToString(hmac.NewHMACSHA512Hasher([]byte("sekrit")).Hash(body))

		test.NoError(t, verifier.Verify(t.Context(), signedHeaders(b64Scheme.Header, signature), body))
	})

	// The rotation window: the provider is still signing with the outgoing secret while the
	// incoming one is configured, and both have to verify or the rotation is an outage.
	T.Run("accepts a signature under an additional secret", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewHMACVerifier(scheme, "incoming", WithAdditionalSecrets("", "outgoing"))
		must.NoError(t, err)

		body := []byte(`{"hello":"world"}`)

		for _, secret := range []string{"incoming", "outgoing"} {
			signature := hashing.Hex(hmac.NewHMACSHA256Hasher([]byte(secret)), body)

			test.NoError(t, verifier.Verify(t.Context(), signedHeaders(scheme.Header, signature), body))
		}
	})

	T.Run("rejects a signature under a secret it does not hold", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewHMACVerifier(scheme, "sekrit")
		must.NoError(t, err)

		body := []byte(`{"hello":"world"}`)
		signature := hashing.Hex(hmac.NewHMACSHA256Hasher([]byte("not sekrit")), body)

		test.ErrorIs(t, verifier.Verify(t.Context(), signedHeaders(scheme.Header, signature), body), ErrInvalidSignature)
	})

	T.Run("rejects a delivery carrying no signature at all", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewHMACVerifier(scheme, "sekrit")
		must.NoError(t, err)

		test.ErrorIs(t, verifier.Verify(t.Context(), http.Header{}, []byte("{}")), ErrInvalidSignature)
		test.ErrorIs(t, verifier.Verify(t.Context(), nil, []byte("{}")), ErrInvalidSignature)
	})

	T.Run("rejects a digest that is not the configured encoding", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewHMACVerifier(scheme, "sekrit")
		must.NoError(t, err)

		test.ErrorIs(t, verifier.Verify(t.Context(), signedHeaders(scheme.Header, "not hex"), []byte("{}")), ErrInvalidSignature)
	})

	// A provider that moves to a new algorithm label is announcing a new scheme. Accepting the
	// digest anyway would verify the new algorithm's output as if it were the old one's.
	T.Run("rejects a header missing the configured prefix", func(t *testing.T) {
		t.Parallel()

		prefixed := &HMACScheme{Provider: "acme", Header: "X-Acme-Signature", Prefix: "sha256="}

		verifier, err := NewHMACVerifier(prefixed, "sekrit")
		must.NoError(t, err)

		body := []byte(`{"hello":"world"}`)
		signature := hashing.Hex(hmac.NewHMACSHA256Hasher([]byte("sekrit")), body)

		test.NoError(t, verifier.Verify(t.Context(), signedHeaders(prefixed.Header, "sha256="+signature), body))
		test.ErrorIs(t, verifier.Verify(t.Context(), signedHeaders(prefixed.Header, signature), body), ErrInvalidSignature)
	})

	T.Run("refuses to build an incomplete or unknown scheme", func(t *testing.T) {
		t.Parallel()

		for name, broken := range map[string]*HMACScheme{
			"nil scheme":  nil,
			"no provider": {Header: "X-Acme-Signature"},
			"no header":   {Provider: "acme"},
			"bad digest":  {Provider: "acme", Header: "X-Acme-Signature", Digest: "md5"},
			"bad encoding": {
				Provider: "acme",
				Header:   "X-Acme-Signature",
				Encoding: "base32",
			},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				verifier, err := NewHMACVerifier(broken, "sekrit")

				test.Error(t, err)
				test.Nil(t, verifier)
			})
		}
	})

	T.Run("refuses to build without a secret", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewHMACVerifier(scheme, "", WithAdditionalSecrets(""))

		test.ErrorIs(t, err, ErrNoSecret)
		test.Nil(t, verifier)
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewHMACVerifier(scheme, "sekrit", nil)

		must.NoError(t, err)
		test.NotNil(t, verifier)
	})
}

func TestHasherFactory(T *testing.T) {
	T.Parallel()

	T.Run("reports an unknown digest as an unknown provider", func(t *testing.T) {
		t.Parallel()

		factory, err := hasherFactory("md5")

		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
		test.Nil(t, factory)
	})
}
