package links

import (
	"net"
	"net/url"
	"strings"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// ActionPolicy is everything a Minter needs to know about one kind of link:
// where it points, and how long it lives.
//
// The two travel together because they are one decision. "Fifteen minutes" is
// only defensible next to "this signs somebody in", and a deployment that moves
// the login URL without revisiting the lifetime has moved half a policy.
// Declaring both at construction also means neither has to be remembered at a
// call site, which is where one of them eventually is not.
type ActionPolicy struct {
	// URL is the address links for this action point at, containing
	// TokenPlaceholder exactly once:
	//
	//	https://app.example.com/auth/magic/{token}
	//	https://app.example.com/unsubscribe?t={token}
	//
	// Either position works. Tokens are base64url, whose alphabet is safe
	// unescaped in a path segment and in a query value alike, which is why
	// substitution here is a substitution and not an encoding decision.
	//
	// It must be https unless the host is loopback — see WithInsecureURLs.
	URL string `json:"url,omitempty" yaml:"url,omitempty"`

	// TTL is how long links for this action stay redeemable, and it is
	// required.
	//
	// Fifteen minutes suits a magic login, an hour a password reset, a day an
	// email verification, a year an unsubscribe link. There is no default
	// because no single one of those is wrong in a harmless direction: a
	// package-chosen fifteen minutes silently breaks unsubscribe links, and a
	// package-chosen year silently leaves login links live in mailboxes.
	TTL time.Duration `json:"ttl,omitempty" yaml:"ttl,omitempty"`
}

// validate reports whether a policy is usable, wrapping the sentinel with the
// action it belongs to so a misconfiguration names itself.
//
// allowInsecure relaxes only the scheme check. Everything else here is about
// whether a URL can be built at all.
func (p ActionPolicy) validate(action Action, allowInsecure bool) error {
	if p.TTL <= 0 {
		return platformerrors.Wrapf(ErrInvalidTTL, "action %q", action)
	}

	if strings.Count(p.URL, TokenPlaceholder) != 1 {
		return platformerrors.Wrapf(
			ErrInvalidActionURL, "action %q URL must contain %s exactly once", action, TokenPlaceholder,
		)
	}

	// Parsed with the placeholder still in it. The placeholder's own characters
	// are legal in a path and in a query value, so this rejects a malformed
	// template rather than one that only becomes malformed once expanded.
	parsed, err := url.Parse(p.URL)
	if err != nil {
		return platformerrors.Wrapf(ErrInvalidActionURL, "action %q URL: %s", action, err.Error())
	}

	if parsed.Host == "" {
		return platformerrors.Wrapf(ErrInvalidActionURL, "action %q URL is not absolute", action)
	}

	switch {
	case parsed.Scheme == "https", allowInsecure:
	case parsed.Scheme == "http" && isLoopback(parsed.Hostname()):
		// The exception that keeps WithInsecureURLs out of local development,
		// where it would be added once and then never removed.
	default:
		return platformerrors.Wrapf(ErrInsecureActionURL, "action %q URL scheme %q", action, parsed.Scheme)
	}

	return nil
}

// expand puts a token into the policy's URL. The placeholder count was checked
// at construction, so this cannot silently produce a URL with no token in it.
func (p ActionPolicy) expand(token Token) string {
	return strings.Replace(p.URL, TokenPlaceholder, string(token), 1)
}

// isLoopback reports whether a hostname is one nothing but this machine can
// reach, so that http against it does not put a token on a network.
//
// "localhost" is matched by name because it is the spelling developers use and
// resolving it here would make validation depend on a resolver.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}
