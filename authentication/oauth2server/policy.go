package oauth2server

import (
	"context"
	"net/url"
	"strings"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Bounds the default registration policy enforces.
//
// They are not a security boundary on their own — a caller who can register one
// client can register a thousand — but they bound what a single registration
// costs, which is what keeps a row in the client table a row rather than a
// place to store a megabyte. The actual defense against volume is rate limiting
// middleware, which the policy cannot do; see RegistrationPolicy.
const (
	// MaxRedirectURIs is how many callbacks one registration may declare.
	MaxRedirectURIs = 16

	// MaxRedirectURILength bounds one callback URI.
	MaxRedirectURILength = 2048

	// MaxClientNameLength bounds the display name, which is rendered on the
	// consent form.
	MaxClientNameLength = 256
)

// Registration rejection reasons. Each wraps ErrRegistrationRejected, so a
// caller writing its own policy can return these, and one reading an error can
// check the general case.
var (
	// ErrNoRedirectURI indicates a registration declaring no callback. There is
	// nowhere to send an authorization code, so the registration would be
	// unusable — and a client with no registered URI is the client for which
	// "any redirect_uri is accepted" was invented.
	ErrNoRedirectURI = platformerrors.Wrap(ErrRegistrationRejected, "registration declares no redirect_uris")

	// ErrTooManyRedirectURIs indicates a registration over MaxRedirectURIs.
	ErrTooManyRedirectURIs = platformerrors.Wrap(ErrRegistrationRejected, "registration declares too many redirect_uris")

	// ErrRedirectURINotAbsolute indicates a callback that is not an absolute
	// URI with a host. A relative one cannot be matched exactly against
	// anything, which is the check the whole registration exists to enable.
	ErrRedirectURINotAbsolute = platformerrors.Wrap(ErrRegistrationRejected, "redirect_uri is not an absolute URI")

	// ErrRedirectURIInsecure indicates an http callback to something that is
	// not a loopback address. An authorization code sent over plaintext to the
	// network is a code anybody on the path can read, and PKCE does not help:
	// the verifier travels over the same network to the same endpoint.
	ErrRedirectURIInsecure = platformerrors.Wrap(ErrRegistrationRejected, "redirect_uri must be https, a loopback http address, or a private-use scheme")

	// ErrRedirectURIHasFragment indicates a callback with a fragment. RFC 6749
	// §3.1.2 forbids it, and it cannot survive the redirect anyway — the
	// server appends its own query, and a browser drops the original fragment
	// when the response adds one.
	ErrRedirectURIHasFragment = platformerrors.Wrap(ErrRegistrationRejected, "redirect_uri must not contain a fragment")

	// ErrRedirectURITooLong indicates a callback over MaxRedirectURILength.
	ErrRedirectURITooLong = platformerrors.Wrap(ErrRegistrationRejected, "redirect_uri is too long")

	// ErrClientNameTooLong indicates a client_name over MaxClientNameLength.
	ErrClientNameTooLong = platformerrors.Wrap(ErrRegistrationRejected, "client_name is too long")

	// ErrUnsupportedAuthMethod indicates a requested token_endpoint_auth_method
	// this server does not implement.
	ErrUnsupportedAuthMethod = platformerrors.Wrap(ErrRegistrationRejected, "unsupported token_endpoint_auth_method")
)

// DefaultRegistrationPolicy is what /register enforces when nothing else is
// configured: the rules a dynamically registered client has to satisfy for the
// rest of this package's checks to mean anything.
//
// Chief among them is that there is at least one redirect URI and that each is
// exactly matchable. Everything the authorization endpoint does about redirect
// URIs rests on the registered set being a set of exact, absolute strings; a
// registration that declared none, or declared a relative one, would leave that
// check with nothing to compare against.
var DefaultRegistrationPolicy RegistrationPolicy = RegistrationPolicyFunc(defaultRegistrationPolicy)

func defaultRegistrationPolicy(_ context.Context, req *RegistrationRequest) error {
	if req == nil {
		return ErrNilRecord
	}

	if len(req.ClientName) > MaxClientNameLength {
		return ErrClientNameTooLong
	}

	switch req.TokenEndpointAuthMethod {
	case "", AuthMethodNone, AuthMethodClientSecret, AuthMethodClientBasic:
	default:
		return platformerrors.Wrapf(ErrUnsupportedAuthMethod, "%q", req.TokenEndpointAuthMethod)
	}

	if len(req.RedirectURIs) == 0 {
		return ErrNoRedirectURI
	}

	if len(req.RedirectURIs) > MaxRedirectURIs {
		return ErrTooManyRedirectURIs
	}

	for _, raw := range req.RedirectURIs {
		if err := ValidateRedirectURI(raw); err != nil {
			return err
		}
	}

	return nil
}

// ValidateRedirectURI reports whether a callback is one this server will send
// an authorization code to.
//
// Three shapes are allowed, and they are the three OAuth 2.1 §8.4 recognizes
// for a client that cannot be confidential:
//
//   - https, for anything reachable over a network;
//   - http on a loopback host, for a native client that spins up a listener on
//     an ephemeral port;
//   - a private-use scheme with a dot in it (com.example.app:/callback), for a
//     mobile client the operating system routes by scheme.
//
// It is exported because a policy that wants to add its own rule — an allowlist
// of hosts, say — should be adding to this rather than replacing it, and
// re-deriving the loopback and private-use cases at that call site is exactly
// the copy that drifts.
func ValidateRedirectURI(raw string) error {
	if raw == "" {
		return ErrRedirectURINotAbsolute
	}

	if len(raw) > MaxRedirectURILength {
		return platformerrors.Wrapf(ErrRedirectURITooLong, "%d bytes", len(raw))
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return platformerrors.Wrap(ErrRedirectURINotAbsolute, "parsing redirect_uri")
	}

	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return ErrRedirectURIHasFragment
	}

	switch {
	case parsed.Scheme == "":
		// A relative reference. Checked before the scheme cases so that
		// "/callback" is refused for what is actually wrong with it — there is
		// nothing to match exactly against — rather than as an insecure scheme,
		// which would send a client author looking for a TLS problem.
		return ErrRedirectURINotAbsolute
	case parsed.Scheme == "https":
		if parsed.Host == "" {
			return ErrRedirectURINotAbsolute
		}
	case parsed.Scheme == "http":
		if !loopbackHost(parsed.Hostname()) {
			return ErrRedirectURIInsecure
		}
	case privateUseScheme(parsed.Scheme):
		// A private-use scheme has no authority component to check; the
		// operating system's registration is what routes it.
	default:
		return ErrRedirectURIInsecure
	}

	return nil
}

// privateUseScheme reports whether scheme looks like the reverse-DNS scheme a
// mobile client registers with its operating system.
//
// The dot is the whole test, and it is the one OAuth 2.1 §8.4.2 recommends:
// a scheme containing a dot is one the registrant controls a domain for, which
// is what stops two applications claiming the same one. It is a convention
// rather than an enforced fact, so this rules out "myapp:" without pretending
// to have verified "com.example.app:".
func privateUseScheme(scheme string) bool {
	return strings.Contains(scheme, ".")
}
