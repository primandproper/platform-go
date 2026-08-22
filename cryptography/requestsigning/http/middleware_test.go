package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	httperrors "github.com/primandproper/platform-go/v13/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var (
	signingTime = time.Unix(1753900000, 0).UTC()
	testKeyring = requestsigning.Keyring{Current: []byte("the shared key")}
)

// failingVerifier stands in for a verifier that could not reach a verdict —
// a key source it could not read — as distinct from one that reached a
// negative one.
type failingVerifier struct{ err error }

var _ requestsigning.Verifier = (*failingVerifier)(nil)

func (*failingVerifier) Scheme() string { return "stub" }

func (f *failingVerifier) VerifyRequest(context.Context, *http.Request) error { return f.err }

// recordingVerifier accepts everything and records what it found on the
// request, so a test can assert the middleware handed over a request the
// verifier could read for itself rather than one somebody else picked apart.
type recordingVerifier struct {
	headerName string
	seenHeader string
	seenBody   []byte
}

var _ requestsigning.Verifier = (*recordingVerifier)(nil)

func (*recordingVerifier) Scheme() string { return "stub" }

func (r *recordingVerifier) VerifyRequest(_ context.Context, req *http.Request) error {
	r.seenHeader = req.Header.Get(r.headerName)

	body, err := requestsigning.RequestBody(req)
	if err != nil {
		return err
	}

	r.seenBody = body

	return nil
}

// testVerifier is the v1 verifier pinned to signingTime, which is what every
// signed fixture below is stamped with.
func testVerifier(t *testing.T) requestsigning.Verifier {
	t.Helper()

	verifier, err := requestsigning.NewVerifier(
		requestsigning.StaticKeyring(testKeyring),
		requestsigning.WithVerificationTime(signingTime),
	)
	must.NoError(t, err)

	return verifier
}

// signedRequest renders a POST carrying body and a valid signature over it.
func signedRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()

	signature, err := requestsigning.Sign(testKeyring, body, signingTime)
	must.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/callbacks/payments", bytes.NewReader(body))
	req.Header.Set(requestsigning.SignatureHeader, signature)

	return req
}

// serve runs one request through mw and reports what the client saw, whether
// the wrapped handler ran, and what body it was handed.
func serve(
	t *testing.T,
	mw func(http.Handler) http.Handler,
	req *http.Request,
) (rec *httptest.ResponseRecorder, reached bool, seen []byte) {
	t.Helper()

	handler := mw(http.HandlerFunc(func(res http.ResponseWriter, r *http.Request) {
		reached = true
		seen, _ = io.ReadAll(r.Body)

		res.WriteHeader(http.StatusNoContent)
	}))

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec, reached, seen
}

func TestNewMiddleware(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(testVerifier(t))
		must.NoError(t, err)

		body := []byte(`{"amount":4200}`)

		rec, reached, seen := serve(t, mw, signedRequest(t, body))

		test.EqOp(t, http.StatusNoContent, rec.Code)
		test.True(t, reached)

		// The handler reads the bytes that were verified, not an empty body
		// somebody else already drained.
		test.Eq(t, body, seen)
	})

	// Nothing here is specific to v1. A scheme carrying its proof in some other
	// header finds it for itself, and the middleware neither knows nor cares —
	// which is what stops this package from being a v1-shaped hole that every
	// other scheme has to be bent through.
	T.Run("hands the verifier a request it can read for itself", func(t *testing.T) {
		t.Parallel()

		verifier := &recordingVerifier{headerName: "X-Partner-Signature"}

		mw, err := NewMiddleware(verifier)
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/callbacks/partner", strings.NewReader(`{"partner":true}`))
		req.Header.Set("X-Partner-Signature", "whatever the partner sends")
		req.Header.Set(requestsigning.SignatureHeader, "the platform's own, which is none of its business")

		rec, reached, seen := serve(t, mw, req)

		test.EqOp(t, http.StatusNoContent, rec.Code)
		test.True(t, reached)
		test.EqOp(t, "whatever the partner sends", verifier.seenHeader)

		// And the verifier and the handler read the same bytes: the verifier's
		// read rewinds rather than consumes, so nothing downstream is starved.
		test.Eq(t, []byte(`{"partner":true}`), verifier.seenBody)
		test.Eq(t, []byte(`{"partner":true}`), seen)
	})

	T.Run("refuses to build without a verifier", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(nil)
		must.ErrorIs(t, err, ErrNilVerifier)
		test.Nil(t, mw)
	})

	T.Run("rejects an unsigned request", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(testVerifier(t))
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/callbacks/payments", strings.NewReader(`{"amount":4200}`))

		rec, reached, _ := serve(t, mw, req)

		test.EqOp(t, http.StatusUnauthorized, rec.Code)
		test.False(t, reached)
		test.EqOp(t, httperrors.ErrInvalidRequestSignature, decodeCode(t, rec))
	})

	T.Run("rejects a tampered body", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(testVerifier(t))
		must.NoError(t, err)

		req := signedRequest(t, []byte(`{"amount":4200}`))
		req.Body = io.NopCloser(strings.NewReader(`{"amount":9999}`))

		rec, reached, _ := serve(t, mw, req)

		test.EqOp(t, http.StatusUnauthorized, rec.Code)
		test.False(t, reached)
	})

	// Stale is a 401 like any other rejection, but the message names the one
	// benign cause so an operator can act on it.
	T.Run("rejects a stale signature", func(t *testing.T) {
		t.Parallel()

		verifier, err := requestsigning.NewVerifier(
			requestsigning.StaticKeyring(testKeyring),
			requestsigning.WithVerificationTime(signingTime.Add(time.Hour)),
		)
		must.NoError(t, err)

		mw, err := NewMiddleware(verifier)
		must.NoError(t, err)

		rec, reached, _ := serve(t, mw, signedRequest(t, []byte(`{"amount":4200}`)))

		test.EqOp(t, http.StatusUnauthorized, rec.Code)
		test.False(t, reached)
		test.EqOp(t, httperrors.ErrInvalidRequestSignature, decodeCode(t, rec))
	})

	// A body past the cap is refused unverified, as a 401 rather than a 413:
	// what happened is that the request could not be authenticated, and a
	// distinct status would tell a prober where the cap sits.
	T.Run("refuses a body past the cap", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(testVerifier(t), WithMaxBodySize(8))
		must.NoError(t, err)

		rec, reached, _ := serve(t, mw, signedRequest(t, []byte(`{"amount":4200}`)))

		test.EqOp(t, http.StatusUnauthorized, rec.Code)
		test.False(t, reached)
	})

	// The boundary: a body of exactly the maximum size is not truncated, and so
	// still verifies.
	T.Run("admits a body of exactly the cap", func(t *testing.T) {
		t.Parallel()

		body := []byte(strings.Repeat("x", 16))

		mw, err := NewMiddleware(testVerifier(t), WithMaxBodySize(int64(len(body))))
		must.NoError(t, err)

		rec, reached, seen := serve(t, mw, signedRequest(t, body))

		test.EqOp(t, http.StatusNoContent, rec.Code)
		test.True(t, reached)
		test.Eq(t, body, seen)
	})

	// A verifier that could not reach a verdict is the service's fault, not the
	// caller's, and must not be reported as a signature that did not verify.
	T.Run("a verifier that could not answer is a 500", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(&failingVerifier{err: platformerrors.New("the key store is down")})
		must.NoError(t, err)

		rec, reached, _ := serve(t, mw, signedRequest(t, []byte(`{"amount":4200}`)))

		test.EqOp(t, http.StatusInternalServerError, rec.Code)
		test.False(t, reached)
	})

	// A body the middleware could not read is not a verdict about the caller's
	// key, and must not be counted or reported as one.
	T.Run("a body that will not read is a 500", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(testVerifier(t))
		must.NoError(t, err)

		req := signedRequest(t, []byte(`{"amount":4200}`))
		req.Body = io.NopCloser(&failingReader{err: platformerrors.New("the client hung up")})

		rec, reached, _ := serve(t, mw, req)

		test.EqOp(t, http.StatusInternalServerError, rec.Code)
		test.False(t, reached)
	})

	T.Run("a request with no body verifies against no bytes", func(t *testing.T) {
		t.Parallel()

		signature, err := requestsigning.Sign(testKeyring, nil, signingTime)
		must.NoError(t, err)

		mw, err := NewMiddleware(testVerifier(t))
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/callbacks/ping", http.NoBody)
		req.Header.Set(requestsigning.SignatureHeader, signature)

		rec, reached, _ := serve(t, mw, req)

		test.EqOp(t, http.StatusNoContent, rec.Code)
		test.True(t, reached)
	})

	// The handler may replay the body — a downstream middleware, a retry of the
	// decode — and must get the same verified bytes each time.
	T.Run("the handler can replay the body", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{"amount":4200}`)

		mw, err := NewMiddleware(testVerifier(t))
		must.NoError(t, err)

		var replayed []byte

		handler := mw(http.HandlerFunc(func(res http.ResponseWriter, r *http.Request) {
			must.NotNil(t, r.GetBody)

			rewound, getErr := r.GetBody()
			must.NoError(t, getErr)

			replayed, _ = io.ReadAll(rewound)

			res.WriteHeader(http.StatusNoContent)
		}))

		handler.ServeHTTP(httptest.NewRecorder(), signedRequest(t, body))

		test.Eq(t, body, replayed)
	})

	T.Run("renders rejections through a custom error encoder", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(testVerifier(t),
			WithErrorEncoder(func(context.Context, error) (int, any) {
				return http.StatusTeapot, map[string]string{"nope": "nope"}
			}))
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/callbacks/payments", strings.NewReader(`{}`))

		rec, reached, _ := serve(t, mw, req)

		test.EqOp(t, http.StatusTeapot, rec.Code)
		test.False(t, reached)
	})

	// An encoder returning a status no ResponseWriter can serve must not panic
	// a request that was already being refused.
	T.Run("clamps an out-of-range status", func(t *testing.T) {
		t.Parallel()

		mw, err := NewMiddleware(testVerifier(t),
			WithErrorEncoder(func(context.Context, error) (int, any) { return 9999, nil }))
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/callbacks/payments", strings.NewReader(`{}`))

		rec, _, _ := serve(t, mw, req)

		test.EqOp(t, http.StatusInternalServerError, rec.Code)
	})

	T.Run("a nil option is skipped", func(t *testing.T) {
		t.Parallel()

		var absent Option

		mw, err := NewMiddleware(testVerifier(t), absent)
		must.NoError(t, err)
		test.NotNil(t, mw)
	})
}

// failingReader is a request body that fails partway through, the way a client
// hanging up mid-upload does.
type failingReader struct{ err error }

var _ io.Reader = (*failingReader)(nil)

func (f *failingReader) Read([]byte) (int, error) { return 0, f.err }

// decodeCode reads the platform error envelope's code out of a response.
func decodeCode(t *testing.T, rec *httptest.ResponseRecorder) httperrors.ErrorCode {
	t.Helper()

	var envelope struct {
		Error struct {
			Code httperrors.ErrorCode `json:"code"`
		} `json:"error"`
	}

	must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))

	return envelope.Error.Code
}
