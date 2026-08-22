package inbound

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	"github.com/primandproper/platform-go/v13/cryptography/hashing/hmac"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

const (
	// GitHubSignatureHeader carries GitHub's HMAC-SHA-256 over the raw body.
	//
	// The older X-Hub-Signature (SHA-1) is deliberately not read. GitHub still
	// sends it for compatibility, and accepting it would mean a receiver's
	// security is set by whichever header an attacker chooses to present.
	GitHubSignatureHeader = "X-Hub-Signature-256"

	// GitHubDeliveryHeader carries GitHub's delivery ID. It is the value a
	// consumer keys deduplication on, and it exists only in the headers — see
	// Delivery.Headers on what that does and does not prove.
	GitHubDeliveryHeader = "X-GitHub-Delivery"

	// githubSignaturePrefix is the algorithm label GitHub writes ahead of the
	// hex digest.
	githubSignaturePrefix = "sha256="

	// providerGitHub is the Provider label NewGitHubVerifier reports.
	providerGitHub = "github"
)

type (
	// Digest names the hash an HMACScheme is computed with.
	Digest string

	// Encoding names how an HMACScheme renders the MAC as text.
	Encoding string
)

const (
	// DigestSHA256 is HMAC-SHA-256, and the default.
	DigestSHA256 Digest = "sha256"
	// DigestSHA512 is HMAC-SHA-512.
	DigestSHA512 Digest = "sha512"

	// EncodingHex is lowercase hex, and the default. Comparison is
	// case-insensitive, so a provider that sends uppercase still verifies.
	EncodingHex Encoding = "hex"
	// EncodingBase64 is standard base64 with padding.
	EncodingBase64 Encoding = "base64"
)

// HMACScheme describes a provider that signs the raw request body with an HMAC
// and sends the result in a single header. It covers most of the long tail:
// Shopify, Twilio's older scheme, Slack's inner shell, and whatever the next
// vendor ships.
//
// It is a struct rather than four positional arguments because three of the
// four are strings, and a call site that reads NewHMACVerifier("acme",
// "X-Acme-Signature", secret, "sha256=") is one transposition away from
// verifying nothing while looking correct.
//
// It does not cover a scheme that signs anything other than the body —
// Stripe's timestamp-prefixed payload, an AWS SNS canonical string. Those are
// their own Verifier implementations; NewStripeVerifier is the one this
// package ships.
type HMACScheme struct {
	// Provider is the label the verifier reports and the receiver stamps on
	// every Delivery. Required.
	Provider string

	// Header names the request header carrying the MAC. Required. Lookup is
	// case-insensitive, as HTTP header lookup always is.
	Header string

	// Prefix is the algorithm label the provider writes ahead of the encoded
	// MAC, e.g. "sha256=". Empty when the header carries the MAC alone. A
	// header that does not begin with a non-empty Prefix is rejected, so a
	// provider that changes its algorithm label cannot have the new one
	// silently accepted under the old key.
	Prefix string

	// Digest selects the hash. Empty means DigestSHA256.
	Digest Digest

	// Encoding selects how the MAC is rendered as text. Empty means
	// EncodingHex.
	Encoding Encoding
}

// HMACVerifier verifies a single-header HMAC over the raw body.
type HMACVerifier struct {
	decode  func(string) ([]byte, error)
	scheme  HMACScheme
	hashers []hashing.Hasher
}

var _ Verifier = (*HMACVerifier)(nil)

// NewGitHubVerifier builds a Verifier for GitHub's X-Hub-Signature-256, which
// is an HMAC-SHA-256 over the raw body, hex-encoded, prefixed "sha256=".
//
// The secret is the webhook secret configured on the repository, organization,
// or app. Reads WithAdditionalSecrets.
func NewGitHubVerifier(secret string, opts ...VerifierOption) (*HMACVerifier, error) {
	return NewHMACVerifier(&HMACScheme{
		Provider: providerGitHub,
		Header:   GitHubSignatureHeader,
		Prefix:   githubSignaturePrefix,
		Digest:   DigestSHA256,
		Encoding: EncodingHex,
	}, secret, opts...)
}

// NewHMACVerifier builds a Verifier for a provider that signs the raw body
// under scheme.
//
// Reads WithAdditionalSecrets. The timestamp options do nothing here: a scheme
// with no signed timestamp has no freshness to check, and pretending otherwise
// by reading some unsigned header would check a value an attacker can edit.
func NewHMACVerifier(scheme *HMACScheme, secret string, opts ...VerifierOption) (*HMACVerifier, error) {
	if scheme == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil HMAC scheme")
	}

	// Copied before anything is defaulted, so filling in the blanks does not edit the
	// caller's descriptor and a reused literal cannot mean two different things.
	s := *scheme

	if s.Provider == "" {
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "HMAC scheme provider")
	}

	if s.Header == "" {
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "HMAC scheme header")
	}

	if s.Digest == "" {
		s.Digest = DigestSHA256
	}

	if s.Encoding == "" {
		s.Encoding = EncodingHex
	}

	newHasher, err := hasherFactory(s.Digest)
	if err != nil {
		return nil, err
	}

	decode, err := decoderFor(s.Encoding)
	if err != nil {
		return nil, err
	}

	cfg := newVerifierConfig(opts)

	secrets := cfg.secretsWith(secret)
	if len(secrets) == 0 {
		return nil, ErrNoSecret
	}

	hashers := make([]hashing.Hasher, 0, len(secrets))
	for _, key := range secrets {
		hashers = append(hashers, newHasher([]byte(key)))
	}

	return &HMACVerifier{decode: decode, scheme: s, hashers: hashers}, nil
}

// Provider returns the scheme's provider label.
func (v *HMACVerifier) Provider() string { return v.scheme.Provider }

// Verify checks the scheme's header against body.
func (v *HMACVerifier) Verify(_ context.Context, headers http.Header, body []byte) error {
	// A nil header bag reads as an absent header rather than a special case, which is what
	// http.Header.Get already does.
	presented := headers.Get(v.scheme.Header)
	if presented == "" {
		// The same error a wrong signature gets: an unsigned delivery and a
		// badly signed one both failed to prove they came from the provider,
		// and separating them tells a prober which header this endpoint reads.
		return platformerrors.Wrapf(ErrInvalidSignature, "no %s header", v.scheme.Header)
	}

	if v.scheme.Prefix != "" {
		rest, ok := strings.CutPrefix(presented, v.scheme.Prefix)
		if !ok {
			return platformerrors.Wrapf(ErrInvalidSignature, "%s does not carry the %q prefix", v.scheme.Header, v.scheme.Prefix)
		}

		presented = rest
	}

	candidate, err := v.decode(presented)
	if err != nil {
		return platformerrors.Wrapf(ErrInvalidSignature, "%s is not valid %s", v.scheme.Header, v.scheme.Encoding)
	}

	// Every secret, without short-circuiting; see hmac.MatchesAny.
	if !hmac.MatchesAny(v.hashers, body, candidate) {
		return ErrInvalidSignature
	}

	return nil
}

// hasherFactory returns the keyed-hasher constructor for a digest.
func hasherFactory(d Digest) (func(key []byte) *hmac.Hasher, error) {
	switch d {
	case DigestSHA256:
		return hmac.NewHMACSHA256Hasher, nil
	case DigestSHA512:
		return hmac.NewHMACSHA512Hasher, nil
	default:
		return nil, platformerrors.Wrapf(platformerrors.ErrUnknownProvider, "webhook signature digest %q", d)
	}
}

// decoderFor returns the decoder for an encoding.
//
// Hex is decoded case-insensitively because encoding/hex accepts either case,
// which is what lets a provider that sends uppercase verify without the
// comparison ever touching a string.
func decoderFor(e Encoding) (func(string) ([]byte, error), error) {
	switch e {
	case EncodingHex:
		return hex.DecodeString, nil
	case EncodingBase64:
		return base64.StdEncoding.DecodeString, nil
	default:
		return nil, platformerrors.Wrapf(platformerrors.ErrUnknownProvider, "webhook signature encoding %q", e)
	}
}
