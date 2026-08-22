package inbound

import (
	"context"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// serviceName names this package's observer, and prefixes every metric it
// records.
const serviceName = "webhooks_inbound"

// DefaultMaxBodyBytes bounds how much of a request body a Receiver reads.
//
// A webhook endpoint is public and unauthenticated until the signature checks
// out, and the signature cannot be checked without the body, so the bound is
// the only thing standing between a hostile client and an allocation of
// whatever size it names. 256 KiB is comfortably above what the providers send
// — Stripe events run in the low tens of kilobytes, GitHub's largest payloads
// in the low hundreds — and comfortably below what a request handler should
// ever hold.
const DefaultMaxBodyBytes int64 = 256 << 10

// DefaultTolerance is how far a signed timestamp may sit from the verifier's
// clock before a delivery is rejected as stale.
//
// It is requestsigning.DefaultTolerance. Five minutes is Stripe's own default
// and the customary figure generally, so the two arrived at the same number
// independently — which is precisely why there should not be two of them to
// change.
const DefaultTolerance = requestsigning.DefaultTolerance

var (
	// ErrInvalidSignature indicates a delivery whose signature header is
	// missing, malformed, or does not match the body under any secret the
	// verifier holds.
	//
	// The cases are deliberately one error. Telling a caller which of them
	// applied tells a forger how close it got, and none of the distinctions are
	// actionable for an operator: every one of them means the same thing, which
	// is that the request did not prove it came from the provider.
	//
	// It is requestsigning's sentinel rather than one of this package's own.
	// "This body did not prove it came from who it claims" is one fact whether
	// the signature was minted by requestsigning or by Stripe, and a service
	// verifying both would otherwise need two errors.Is calls to ask it — which
	// is the same reason the outbound half aliases ErrNoSigningSecret.
	ErrInvalidSignature = requestsigning.ErrInvalidSignature

	// ErrStaleSignature indicates a signature whose timestamp sits outside the
	// tolerance window. Only schemes that sign a timestamp can report it.
	//
	// It is separate from ErrInvalidSignature because it is the one
	// verification failure with a benign cause an operator can act on — clock
	// skew — and because it says nothing about whether the secret was right.
	// It is requestsigning's, for the reason above.
	ErrStaleSignature = requestsigning.ErrStaleSignature

	// ErrNoSecret indicates a verifier constructed without one. A verifier with
	// no secret rejects every delivery, which from the outside is
	// indistinguishable from a provider that has the wrong secret configured,
	// so it is refused at construction instead.
	ErrNoSecret = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no webhook signing secret")

	// ErrNilVerifier indicates NewReceiver called without one. There is no
	// default: a receiver that published unverified bodies would be a public
	// endpoint for injecting messages onto an internal topic.
	ErrNilVerifier = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webhook verifier")

	// ErrNilPublisher indicates NewReceiver called without one. Publishing is
	// the entire reason the ack is fast, so there is nothing sensible to
	// substitute; a caller that genuinely wants deliveries discarded builds a
	// noop publisher and names it.
	ErrNilPublisher = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webhook publisher")

	// ErrBodyTooLarge indicates a request body exceeding the receiver's cap. It
	// is answered with 413 rather than 400, so an operator reading provider-side
	// delivery logs sees a size problem rather than a signature problem.
	ErrBodyTooLarge = platformerrors.New("webhook body exceeds the configured limit")
)

type (
	// Verifier decides whether a delivery really came from the provider.
	//
	// It takes the header bag and the body separately rather than an
	// *http.Request, which is what makes it usable at all: by the time a
	// Receiver verifies, it has already read the body under a cap, and a
	// request handed to a verifier at that point has an empty Body and no
	// GetBody to rewind. Passing the bytes explicitly also means the bytes
	// verified are provably the bytes published — there is no second read that
	// could see something different — and it lets a consumer re-verify a
	// payload it took off a queue, where there is no request at all.
	//
	// body must be the bytes exactly as received. Decoding, re-encoding, or
	// pretty-printing JSON produces a different byte sequence and therefore a
	// different MAC, and the resulting failure looks like a wrong secret.
	//
	// The implementations here are Stripe, GitHub, and a configurable HMAC for
	// the long tail; a scheme this package does not implement satisfies the
	// same two methods and runs through the same Receiver.
	Verifier interface {
		// Provider names the provider this verifier speaks for, e.g. "stripe".
		// It is a label: it lands on Delivery, on spans, and on this package's
		// metrics, and nothing dispatches on it.
		Provider() string

		// Verify returns nil only if body was signed under a secret this
		// verifier holds. It returns ErrInvalidSignature for anything that did
		// not prove that, and ErrStaleSignature when the scheme carries a
		// timestamp and the timestamp is outside tolerance.
		Verify(ctx context.Context, headers http.Header, body []byte) error
	}

	// Delivery is the message a Receiver publishes for a verified webhook. It
	// is the package's wire contract with its consumers, so its JSON field
	// names are as much a part of the API as its Go ones.
	//
	// Body is the raw provider payload, exactly as it arrived and exactly as it
	// was verified. It is []byte rather than a decoded structure because the
	// receiver does not know the provider's schema, and rather than a string
	// because a MAC is over bytes: round-tripping the payload through anything
	// that could normalize it would leave a consumer unable to re-verify what
	// it was handed.
	Delivery struct {
		// ReceivedAt is when the receiver read the request, from its clock.
		// It is the receiver's own observation, not a value from the provider,
		// so a consumer can measure queue lag against it.
		ReceivedAt time.Time `json:"receivedAt"`

		// Headers carries the request's headers, minus credential headers and
		// minus anything WithForwardedHeaders excluded. They are here because
		// providers put things in them a consumer needs — GitHub's delivery ID
		// travels in X-GitHub-Delivery and nowhere else.
		//
		// They are NOT authenticated: these schemes sign the body, not the
		// headers. Treat them as untrusted metadata and take anything that
		// matters from Body.
		Headers http.Header `json:"headers,omitempty"`

		// Provider is the verifier's Provider, so a consumer reading one topic
		// carrying several providers can tell them apart.
		Provider string `json:"provider"`

		// Body is the verified payload, byte for byte as received.
		Body []byte `json:"body"`
	}
)
