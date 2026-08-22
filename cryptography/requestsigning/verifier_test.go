package requestsigning

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// inboundRequest builds what a server hands a Verifier: a request carrying body
// and, when signed is true, a valid signature over it.
//
// GetBody is set explicitly, because httptest.NewRequest does not set one and
// the contract requestsigning/http's middleware guarantees is a replayable
// body. A fixture without it would be testing the middleware's bug rather than
// the middleware.
func inboundRequest(t *testing.T, keyring Keyring, body []byte, at time.Time, signed bool) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/callbacks/payments", bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }

	if signed {
		signature, err := Sign(keyring, body, at)
		must.NoError(t, err)

		req.Header.Set(SignatureHeader, signature)
	}

	return req
}

func TestNewVerifier(T *testing.T) {
	T.Parallel()

	keyring := Keyring{Current: []byte("secret")}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewVerifier(StaticKeyring(keyring), WithVerificationTime(signingTime))
		must.NoError(t, err)

		test.EqOp(t, SchemeV1, verifier.Scheme())
		test.NoError(t, verifier.VerifyRequest(t.Context(), inboundRequest(t, keyring, testBody, signingTime, true)))
	})

	// The verifier reads the body and leaves it there, so the handler behind it
	// sees the bytes that were verified rather than an empty reader.
	T.Run("leaves the body readable", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewVerifier(StaticKeyring(keyring), WithVerificationTime(signingTime))
		must.NoError(t, err)

		req := inboundRequest(t, keyring, testBody, signingTime, true)
		must.NoError(t, verifier.VerifyRequest(t.Context(), req))

		remaining, err := io.ReadAll(req.Body)
		must.NoError(t, err)
		test.Eq(t, testBody, remaining)
	})

	// A missing header is the same answer as a wrong one. Separating them tells
	// a prober which header this endpoint reads.
	T.Run("an unsigned request is an invalid one", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewVerifier(StaticKeyring(keyring), WithVerificationTime(signingTime))
		must.NoError(t, err)

		test.ErrorIs(t,
			verifier.VerifyRequest(t.Context(), inboundRequest(t, keyring, testBody, signingTime, false)),
			ErrInvalidSignature,
		)
	})

	// And it is refused before the body is touched, so an unsigned flood costs
	// nothing proportional to what it carried.
	T.Run("an unsigned request is refused before its body is read", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewVerifier(StaticKeyring(keyring), WithVerificationTime(signingTime))
		must.NoError(t, err)

		read := false
		req := httptest.NewRequest(http.MethodPost, "/callbacks/payments",
			io.NopCloser(readerFunc(func([]byte) (int, error) {
				read = true

				return 0, io.EOF
			})))
		req.GetBody = nil

		test.ErrorIs(t, verifier.VerifyRequest(t.Context(), req), ErrInvalidSignature)
		test.False(t, read)
	})

	T.Run("rejects a tampered body", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewVerifier(StaticKeyring(keyring), WithVerificationTime(signingTime))
		must.NoError(t, err)

		req := inboundRequest(t, keyring, testBody, signingTime, true)
		req.Body = io.NopCloser(strings.NewReader(`{"id":"abc","amount":999}`))
		req.GetBody = nil

		test.ErrorIs(t, verifier.VerifyRequest(t.Context(), req), ErrInvalidSignature)
	})

	T.Run("honors the configured tolerance", func(t *testing.T) {
		t.Parallel()

		tight, err := NewVerifier(StaticKeyring(keyring),
			WithVerificationTime(signingTime.Add(30*time.Minute)))
		must.NoError(t, err)

		test.ErrorIs(t,
			tight.VerifyRequest(t.Context(), inboundRequest(t, keyring, testBody, signingTime, true)),
			ErrStaleSignature,
		)

		loose, err := NewVerifier(StaticKeyring(keyring),
			WithVerificationTime(signingTime.Add(30*time.Minute)),
			WithTolerance(time.Hour))
		must.NoError(t, err)

		test.NoError(t, loose.VerifyRequest(t.Context(), inboundRequest(t, keyring, testBody, signingTime, true)))
	})

	// The verifier re-reads its keyring too, so a receiver picks up its own
	// rotation without a restart.
	T.Run("re-reads the keyring per request", func(t *testing.T) {
		t.Parallel()

		trusted := Keyring{Current: []byte("first")}

		verifier, err := NewVerifier(
			KeySourceFunc(func(context.Context) (Keyring, error) { return trusted, nil }),
			WithVerificationTime(signingTime),
		)
		must.NoError(t, err)

		second := Keyring{Current: []byte("second")}

		test.ErrorIs(t,
			verifier.VerifyRequest(t.Context(), inboundRequest(t, second, testBody, signingTime, true)),
			ErrInvalidSignature,
		)

		trusted = second

		test.NoError(t, verifier.VerifyRequest(t.Context(), inboundRequest(t, second, testBody, signingTime, true)))
	})

	T.Run("reports a key source it could not read", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the store is down")

		verifier, err := NewVerifier(KeySourceFunc(func(context.Context) (Keyring, error) { return Keyring{}, boom }))
		must.NoError(t, err)

		test.ErrorIs(t,
			verifier.VerifyRequest(t.Context(), inboundRequest(t, keyring, testBody, time.Now(), true)),
			boom,
		)
	})

	T.Run("reports a body it could not read", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the client hung up")

		verifier, err := NewVerifier(StaticKeyring(keyring), WithVerificationTime(signingTime))
		must.NoError(t, err)

		req := inboundRequest(t, keyring, testBody, signingTime, true)
		req.GetBody = func() (io.ReadCloser, error) { return nil, boom }

		test.ErrorIs(t, verifier.VerifyRequest(t.Context(), req), boom)
	})

	T.Run("rejects its own bad inputs", func(t *testing.T) {
		t.Parallel()

		_, err := NewVerifier(nil)
		test.ErrorIs(t, err, ErrNilKeySource)

		verifier, err := NewVerifier(StaticKeyring(keyring))
		must.NoError(t, err)

		test.ErrorIs(t, verifier.VerifyRequest(t.Context(), nil), platformerrors.ErrNilInputParameter)
	})
}

// readerFunc adapts a function to io.Reader, for asserting that a body was not
// touched.
type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// A signer and a verifier built from the same key source agree, which is the
// property every other test in this package is a special case of.
func TestSignerVerifierRoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		keys := StaticKeyring(Keyring{Current: []byte("shared")})

		signer, err := NewSigner(keys, WithClock(fixedClock(signingTime)))
		must.NoError(t, err)

		verifier, err := NewVerifier(keys, WithVerificationTime(signingTime))
		must.NoError(t, err)

		// One request, signed on the way out and checked on the way in. Neither
		// side is told which header carries the proof, and neither side is
		// handed the body separately from the request that holds it.
		req := outboundRequest(t, testBody)
		must.NoError(t, signer.SignRequest(t.Context(), req))

		test.NoError(t, verifier.VerifyRequest(t.Context(), req))
	})
}

func TestRequestBody(T *testing.T) {
	T.Parallel()

	T.Run("rewinds through GetBody", func(t *testing.T) {
		t.Parallel()

		req := outboundRequest(t, testBody)

		first, err := RequestBody(req)
		must.NoError(t, err)
		test.Eq(t, testBody, first)

		// Twice, because a signer reads it and then a transport sends it.
		second, err := RequestBody(req)
		must.NoError(t, err)
		test.Eq(t, testBody, second)
	})

	// Without GetBody there is nothing to rewind, so the read consumes — which
	// is why both callers in this module set one.
	T.Run("consumes a body that cannot be rewound", func(t *testing.T) {
		t.Parallel()

		req := outboundRequest(t, testBody)
		req.GetBody = nil

		first, err := RequestBody(req)
		must.NoError(t, err)
		test.Eq(t, testBody, first)

		second, err := RequestBody(req)
		must.NoError(t, err)
		test.SliceEmpty(t, second)
	})

	T.Run("a request with no body reads as no bytes", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, mustBody(t, httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)))

		bodiless := httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
		bodiless.Body = nil

		test.Nil(t, mustBody(t, bodiless))
	})

	T.Run("rejects a nil request", func(t *testing.T) {
		t.Parallel()

		_, err := RequestBody(nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

// mustBody reads a request's body, failing the test rather than the caller.
func mustBody(t *testing.T, req *http.Request) []byte {
	t.Helper()

	body, err := RequestBody(req)
	must.NoError(t, err)

	return body
}
