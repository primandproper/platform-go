package requestsigning

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// fixedClock reads one instant forever, which is what a signer needs from a
// clock in a test: the assertion is about what got stamped, not about time
// passing.
func fixedClock(at time.Time) clock.Clock {
	return &clockmock.ClockMock{NowFunc: func() time.Time { return at }}
}

// outboundRequest builds the shape the signing transport hands a Signer: a
// request whose body is buffered and replayable, so signing rewinds it rather
// than consuming it.
func outboundRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(),
		http.MethodPost, "http://example.com/thing", strings.NewReader(string(body)))
	must.NoError(t, err)

	return req
}

func TestNewSigner(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		signer, err := NewSigner(StaticKeyring(Keyring{Current: []byte("secret")}), WithClock(fixedClock(signingTime)))
		must.NoError(t, err)

		test.EqOp(t, SchemeV1, signer.Scheme())

		req := outboundRequest(t, testBody)
		must.NoError(t, signer.SignRequest(t.Context(), req))

		// What was stamped is what Verify accepts, which is the only property
		// worth asserting about a signer.
		test.NoError(t, Verify(
			Keyring{Current: []byte("secret")},
			testBody,
			req.Header.Get(SignatureHeader),
			WithVerificationTime(signingTime),
		))

		// The timestamp header is the same instant as the one inside the
		// signature, so a receiver that sheds stale requests before hashing
		// cannot disagree with the one that hashes.
		test.EqOp(t, strconv.FormatInt(signingTime.Unix(), 10), req.Header.Get(TimestampHeader))
	})

	// The signer reads the body it was handed and leaves it there. Anything else
	// would mean the caller signs a request it can no longer send.
	T.Run("leaves the body readable", func(t *testing.T) {
		t.Parallel()

		signer, err := NewSigner(StaticKeyring(Keyring{Current: []byte("secret")}), WithClock(fixedClock(signingTime)))
		must.NoError(t, err)

		req := outboundRequest(t, testBody)
		must.NoError(t, signer.SignRequest(t.Context(), req))

		remaining, err := RequestBody(req)
		must.NoError(t, err)
		test.Eq(t, testBody, remaining)
	})

	// Each call reads the clock again. This is what makes a retry that fires
	// after a long backoff arrive fresh rather than stale.
	T.Run("stamps a fresh timestamp per call", func(t *testing.T) {
		t.Parallel()

		now := signingTime
		signer, err := NewSigner(
			StaticKeyring(Keyring{Current: []byte("secret")}),
			WithClock(&clockmock.ClockMock{NowFunc: func() time.Time { return now }}),
		)
		must.NoError(t, err)

		first := outboundRequest(t, testBody)
		must.NoError(t, signer.SignRequest(t.Context(), first))

		now = signingTime.Add(time.Hour)

		second := outboundRequest(t, testBody)
		must.NoError(t, signer.SignRequest(t.Context(), second))

		test.NotEqOp(t, first.Header.Get(SignatureHeader), second.Header.Get(SignatureHeader))
		test.EqOp(t, strconv.FormatInt(now.Unix(), 10), second.Header.Get(TimestampHeader))
	})

	// The keyring is read per request, so a rotation in the store reaches the
	// wire without a restart.
	T.Run("re-reads the keyring per request", func(t *testing.T) {
		t.Parallel()

		key := []byte("first")

		signer, err := NewSigner(
			KeySourceFunc(func(context.Context) (Keyring, error) { return Keyring{Current: key}, nil }),
			WithClock(fixedClock(signingTime)),
		)
		must.NoError(t, err)

		key = []byte("second")

		req := outboundRequest(t, testBody)
		must.NoError(t, signer.SignRequest(t.Context(), req))

		test.NoError(t, Verify(
			Keyring{Current: []byte("second")},
			testBody,
			req.Header.Get(SignatureHeader),
			WithVerificationTime(signingTime),
		))
	})

	// A request with no body signs over no bytes, and the two sides agree about
	// what that means.
	T.Run("signs a request with no body", func(t *testing.T) {
		t.Parallel()

		signer, err := NewSigner(StaticKeyring(Keyring{Current: []byte("secret")}), WithClock(fixedClock(signingTime)))
		must.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/ping", http.NoBody)
		must.NoError(t, signer.SignRequest(t.Context(), req))

		test.NoError(t, Verify(
			Keyring{Current: []byte("secret")},
			nil,
			req.Header.Get(SignatureHeader),
			WithVerificationTime(signingTime),
		))
	})

	T.Run("reports a key source it could not read", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the store is down")

		signer, err := NewSigner(KeySourceFunc(func(context.Context) (Keyring, error) { return Keyring{}, boom }))
		must.NoError(t, err)

		test.ErrorIs(t, signer.SignRequest(t.Context(), outboundRequest(t, testBody)), boom)
	})

	T.Run("reports a keyring with no current key", func(t *testing.T) {
		t.Parallel()

		signer, err := NewSigner(StaticKeyring(Keyring{Previous: []byte("old")}))
		must.NoError(t, err)

		test.ErrorIs(t, signer.SignRequest(t.Context(), outboundRequest(t, testBody)), ErrNoSigningKey)
	})

	T.Run("reports a body it could not read", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("the disk went away")

		signer, err := NewSigner(StaticKeyring(Keyring{Current: []byte("secret")}))
		must.NoError(t, err)

		req := outboundRequest(t, testBody)
		req.GetBody = func() (io.ReadCloser, error) { return nil, boom }

		test.ErrorIs(t, signer.SignRequest(t.Context(), req), boom)
	})

	T.Run("rejects its own bad inputs", func(t *testing.T) {
		t.Parallel()

		_, err := NewSigner(nil)
		test.ErrorIs(t, err, ErrNilKeySource)

		signer, err := NewSigner(StaticKeyring(Keyring{Current: []byte("k")}))
		must.NoError(t, err)

		test.ErrorIs(t, signer.SignRequest(t.Context(), nil), platformerrors.ErrNilInputParameter)
	})
}
