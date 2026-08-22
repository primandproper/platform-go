package inbound

import (
	"fmt"
	"strings"
	"testing"
	"time"

	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	"github.com/primandproper/platform-go/v13/cryptography/hashing/hmac"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// signedAt is the instant every Stripe test signs at and verifies against, so nothing here
// depends on how long the test took to run.
var signedAt = time.Unix(1614556800, 0)

// stripeHeader renders a Stripe-Signature header signing body at ts under each secret.
func stripeHeader(ts time.Time, body []byte, secrets ...string) string {
	seconds := fmt.Sprintf("%d", ts.Unix())

	elements := []string{"t=" + seconds}
	for _, secret := range secrets {
		elements = append(elements, "v1="+hashing.HexString(
			hmac.NewHMACSHA256Hasher([]byte(secret)),
			seconds+"."+string(body),
		))
	}

	return strings.Join(elements, ",")
}

func TestNewStripeVerifier(T *testing.T) {
	T.Parallel()

	body := []byte(`{"id":"evt_123","type":"payment_intent.succeeded"}`)

	newVerifier := func(t *testing.T, secret string, opts ...VerifierOption) Verifier {
		t.Helper()

		verifier, err := NewStripeVerifier(secret, append([]VerifierOption{WithVerificationTime(signedAt)}, opts...)...)
		must.NoError(t, err)

		return verifier
	}

	T.Run("verifies a signature over the timestamp and body", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		test.EqOp(t, providerStripe, verifier.Provider())
		test.NoError(t, verifier.Verify(
			t.Context(),
			signedHeaders(StripeSignatureHeader, stripeHeader(signedAt, body, "whsec_test")),
			body,
		))
	})

	// The signature is over "<timestamp>.<body>", not over the body alone. A verifier that
	// dropped the timestamp from the signed material would still be checking a MAC under the
	// right key, and would accept a delivery whose timestamp had been rewritten.
	T.Run("rejects a signature that omits the timestamp from the signed payload", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		bodyOnly := hashing.Hex(hmac.NewHMACSHA256Hasher([]byte("whsec_test")), body)
		header := fmt.Sprintf("t=%d,v1=%s", signedAt.Unix(), bodyOnly)

		test.ErrorIs(t, verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body), ErrInvalidSignature)
	})

	// Stripe emits one v1 per active endpoint secret while it rolls its own. Only one of them
	// is ours, and rejecting on the first mismatch would fail every delivery for the length of
	// a rotation this receiver has no say in.
	T.Run("accepts any one of several v1 signatures", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		header := stripeHeader(signedAt, body, "whsec_someone_else", "whsec_test", "whsec_also_not_ours")

		test.NoError(t, verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body))
	})

	T.Run("accepts a signature under an additional secret", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_incoming", WithAdditionalSecrets("whsec_outgoing"))

		for _, secret := range []string{"whsec_incoming", "whsec_outgoing"} {
			header := stripeHeader(signedAt, body, secret)

			test.NoError(t, verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body))
		}
	})

	// Stripe has added elements to this header before (v0, for its test-mode scheme) and will
	// again. Failing on an unknown element would break on a change designed to be compatible.
	T.Run("skips elements it does not recognize", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		header := stripeHeader(signedAt, body, "whsec_test") + ",v0=deadbeef,junk"

		test.NoError(t, verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body))
	})

	T.Run("skips a v1 element that is not hex", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		header := "v1=not-hex," + stripeHeader(signedAt, body, "whsec_test")

		test.NoError(t, verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body))
	})

	T.Run("rejects a body it did not sign", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		header := stripeHeader(signedAt, body, "whsec_test")

		test.ErrorIs(t,
			verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), append(body, ' ')),
			ErrInvalidSignature,
		)
	})

	T.Run("rejects a signature under a secret it does not hold", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		header := stripeHeader(signedAt, body, "whsec_someone_else")

		test.ErrorIs(t, verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body), ErrInvalidSignature)
	})

	T.Run("rejects a malformed or absent header", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		for name, header := range map[string]string{
			"empty":             "",
			"no elements":       "gibberish",
			"no timestamp":      "v1=deadbeef",
			"unparsed t":        "t=whenever,v1=deadbeef",
			"no v1":             fmt.Sprintf("t=%d", signedAt.Unix()),
			"only unknown keys": fmt.Sprintf("t=%d,v0=deadbeef", signedAt.Unix()),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				test.ErrorIs(t,
					verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body),
					ErrInvalidSignature,
				)
			})
		}

		test.ErrorIs(t, verifier.Verify(t.Context(), nil, body), ErrInvalidSignature)
	})

	// The whole point of signing a timestamp: a captured delivery stops being replayable once
	// the window closes.
	T.Run("rejects a delivery outside the tolerance", func(t *testing.T) {
		t.Parallel()

		verifier := newVerifier(t, "whsec_test")

		for name, ts := range map[string]time.Time{
			"too old":       signedAt.Add(-DefaultTolerance - time.Second),
			"too far ahead": signedAt.Add(DefaultTolerance + time.Second),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				header := stripeHeader(ts, body, "whsec_test")

				test.ErrorIs(t,
					verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body),
					ErrStaleSignature,
				)
			})
		}
	})

	T.Run("honors a widened tolerance", func(t *testing.T) {
		t.Parallel()

		header := stripeHeader(signedAt.Add(-time.Hour), body, "whsec_test")
		headers := signedHeaders(StripeSignatureHeader, header)

		test.ErrorIs(t, newVerifier(t, "whsec_test").Verify(t.Context(), headers, body), ErrStaleSignature)
		test.NoError(t, newVerifier(t, "whsec_test", WithTolerance(2*time.Hour)).Verify(t.Context(), headers, body))
	})

	T.Run("refuses to build without a secret", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewStripeVerifier("")

		test.ErrorIs(t, err, ErrNoSecret)
		test.Nil(t, verifier)
	})

	// Nothing pins the verifier to the epoch or to a clock it was not given.
	T.Run("reads the injected clock when no time is pinned", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewStripeVerifier("whsec_test", WithClock(&clockmock.ClockMock{
			NowFunc: func() time.Time { return signedAt },
		}))
		must.NoError(t, err)

		header := stripeHeader(signedAt, body, "whsec_test")

		test.NoError(t, verifier.Verify(t.Context(), signedHeaders(StripeSignatureHeader, header), body))
	})
}

func TestParseStripeSignature(T *testing.T) {
	T.Parallel()

	T.Run("tolerates whitespace around elements", func(t *testing.T) {
		t.Parallel()

		sig := parseStripeSignature(" t=1614556800 , v1=deadbeef ")

		test.EqOp(t, "1614556800", sig.rawTimestamp)
		test.EqOp(t, signedAt.Unix(), sig.timestamp.Unix())
		must.SliceLen(t, 1, sig.candidates)
	})

	T.Run("abandons a header whose timestamp does not parse", func(t *testing.T) {
		t.Parallel()

		sig := parseStripeSignature("t=nope,v1=deadbeef")

		test.True(t, sig.timestamp.IsZero())
		test.SliceLen(t, 0, sig.candidates)
	})
}
