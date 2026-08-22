package httpclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var signingKeyring = requestsigning.Keyring{Current: []byte("the shared key")}

// signedAt is the instant every fixture here is stamped with.
var signedAt = time.Unix(1753900000, 0).UTC()

// testSigner is the v1 signer over signingKeyring, reading c.
func testSigner(t *testing.T, c clock.Clock) requestsigning.Signer {
	t.Helper()

	signer, err := requestsigning.NewSigner(requestsigning.StaticKeyring(signingKeyring), requestsigning.WithClock(c))
	must.NoError(t, err)

	return signer
}

// failingSigner reports a key source it could not read.
type failingSigner struct{ err error }

var _ requestsigning.Signer = (*failingSigner)(nil)

func (*failingSigner) Scheme() string { return "stub" }

func (f *failingSigner) SignRequest(context.Context, *http.Request) error { return f.err }

func TestSigningTransport_RoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var (
			gotHeader http.Header
			gotBody   []byte
		)

		client := newClient(t,
			WithTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				gotHeader = req.Header.Clone()
				gotBody, _ = io.ReadAll(req.Body)

				return response(http.StatusOK, "fine"), nil
			})),
			WithRequestSigning(testSigner(t, &steppingClock{now: signedAt})),
		)

		body := `{"amount":4200}`

		resp, err := post(t.Context(), client, "http://example.com/thing", strings.NewReader(body))
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		// The body still reaches the wire, byte for byte, having been read once
		// to sign it.
		test.EqOp(t, body, string(gotBody))

		// And the signature covers exactly those bytes.
		test.NoError(t, requestsigning.Verify(
			signingKeyring,
			gotBody,
			gotHeader.Get(requestsigning.SignatureHeader),
			requestsigning.WithVerificationTime(signedAt),
		))
	})

	// The reason this transport sits below the retry loop: a signature carries a
	// timestamp, and an attempt that reused the first one's would arrive stale
	// after a long backoff.
	T.Run("signs each attempt afresh", func(t *testing.T) {
		t.Parallel()

		var signatures []string

		// The clock moves between attempts rather than during one, which is the
		// backoff a real retry loop would have spent.
		c := &steppingClock{now: signedAt}

		client := newClient(t,
			WithTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				signatures = append(signatures, req.Header.Get(requestsigning.SignatureHeader))
				c.advance(time.Minute)

				return response(http.StatusServiceUnavailable, "later"), nil
			})),
			WithRequestSigning(testSigner(t, c)),
			WithRetryPolicy(&immediatePolicy{attempts: 3}),
		)

		resp, err := get(t.Context(), client, "http://example.com/thing")
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		must.SliceLen(t, 3, signatures)
		test.NotEqOp(t, signatures[0], signatures[1])
		test.NotEqOp(t, signatures[1], signatures[2])
	})

	// A retried POST's body has to survive being read for the signature as well
	// as being rewound for the attempt, which is two different consumers of one
	// stream.
	T.Run("a retried body is signed and sent whole every time", func(t *testing.T) {
		t.Parallel()

		body := `{"amount":4200}`

		var bodies []string

		client := newClient(t,
			WithTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				received, _ := io.ReadAll(req.Body)
				bodies = append(bodies, string(received))

				test.NoError(t, requestsigning.Verify(
					signingKeyring,
					received,
					req.Header.Get(requestsigning.SignatureHeader),
					requestsigning.WithVerificationTime(signedAt),
				))

				return response(http.StatusServiceUnavailable, "later"), nil
			})),
			WithRequestSigning(testSigner(t, &steppingClock{now: signedAt})),
			WithRetryPolicy(&immediatePolicy{attempts: 2}, WithRetryMethods(http.MethodPost)),
		)

		resp, err := post(t.Context(), client, "http://example.com/thing", strings.NewReader(body))
		must.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		test.Eq(t, []string{body, body}, bodies)
	})

	// Below the limiter as well as below retry: signing costs a key resolution
	// and an HMAC over the whole body, and a request that never leaves should
	// not pay either.
	T.Run("a rate-limited request is never signed", func(t *testing.T) {
		t.Parallel()

		signer := &countingSigner{inner: testSigner(t, &steppingClock{now: signedAt})}

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request reached the wire")

				return response(http.StatusOK, "fine"), nil
			})),
			WithRequestSigning(signer),
			WithRateLimit(&stubLimiter{allow: func(string) (bool, error) { return false, nil }}),
		)

		_, err := get(t.Context(), client, "http://example.com/thing")
		test.ErrorIs(t, err, ratelimiting.ErrRateLimited)
		test.EqOp(t, 0, signer.calls)
	})

	// A client that cannot sign sends nothing. The failure is a key source this
	// process could not read, so it must not look like a transport error from
	// the far side.
	T.Run("a signer that cannot answer fails the request", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the key store is down")

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request reached the wire")

				return response(http.StatusOK, "fine"), nil
			})),
			WithRequestSigning(&failingSigner{err: boom}),
		)

		_, err := get(t.Context(), client, "http://example.com/thing")
		test.ErrorIs(t, err, boom)
	})

	// A body that cannot be read cannot be signed, and a request that cannot be
	// signed must not go out unsigned.
	T.Run("a body that will not read fails the request", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the disk went away mid-upload")

		client := newClient(t,
			WithTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Error("the request reached the wire")

				return response(http.StatusOK, "fine"), nil
			})),
			WithRequestSigning(testSigner(t, &steppingClock{now: signedAt})),
		)

		_, err := post(t.Context(), client, "http://example.com/thing", &failingReader{err: boom})
		test.ErrorIs(t, err, boom)
	})

	T.Run("a nil signer installs no layer", func(t *testing.T) {
		t.Parallel()

		cfg := newClientConfig()
		WithRequestSigning(nil)(cfg)

		test.Nil(t, cfg.signer)
	})
}

// failingReader is a request body that fails partway through, the way a client
// hanging up mid-upload does.
type failingReader struct{ err error }

var _ io.Reader = (*failingReader)(nil)

func (f *failingReader) Read([]byte) (int, error) { return 0, f.err }

// countingSigner records how many times it was asked to sign.
type countingSigner struct {
	inner requestsigning.Signer
	calls int
}

var _ requestsigning.Signer = (*countingSigner)(nil)

func (c *countingSigner) Scheme() string { return c.inner.Scheme() }

func (c *countingSigner) SignRequest(ctx context.Context, req *http.Request) error {
	c.calls++

	return c.inner.SignRequest(ctx, req)
}
