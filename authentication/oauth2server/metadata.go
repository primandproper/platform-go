package oauth2server

import (
	"net/url"
	"strings"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// The paths this package mounts. They are the ones RFC 8414 and RFC 9728 fix
// (the two .well-known documents) and the conventional spellings for the rest.
//
// They are constants rather than options because a discovery document is what
// tells a client where the endpoints are, so nothing outside this package needs
// to know them — and a deployment that wants them elsewhere mounts the handlers
// itself. What must not vary is that the metadata and the mount agree, which is
// what sharing these constants buys.
const (
	PathAuthorizationServerMetadata = "/.well-known/oauth-authorization-server"
	PathProtectedResourceMetadata   = "/.well-known/oauth-protected-resource"
	PathAuthorize                   = "/authorize"
	PathToken                       = "/token"
	PathRegister                    = "/register"
	PathRevoke                      = "/revoke"
)

// AuthorizationServerMetadata is the RFC 8414 discovery document.
//
// The field set is what this server actually implements, not the whole
// registry. A document advertising something the endpoints do not do is worse
// than a shorter one: a client believes it, and the failure surfaces at the
// token endpoint as an error the client's author has no reason to expect. The
// map-backed examples advertise client_secret_post from an endpoint that reads
// no secret, which is exactly this failure with the sign flipped.
//
// RegistrationEndpoint is the one field here that can be absent rather than
// merely empty, and it is spelled omitempty for a reason an empty string cannot
// carry: a server built with WithDynamicRegistration(false) does not serve
// /register, and a client that resolved "" against the issuer would get this
// server's root rather than learning the endpoint is not there.
type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	ServiceDocumentation              string   `json:"service_documentation,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`

	// CodeChallengeMethodsSupported lists S256 and nothing else, which is also
	// how a client discovers that PKCE is not optional here.
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`

	RevocationEndpointAuthMethodsSupported []string `json:"revocation_endpoint_auth_methods_supported"`

	// AuthorizationResponseIssParameterSupported reports that every
	// authorization response carries the "iss" parameter (RFC 9207), which is
	// what lets a client with more than one authorization server tell which one
	// answered. A client holding two servers and no iss cannot detect a mix-up
	// attack.
	AuthorizationResponseIssParameterSupported bool `json:"authorization_response_iss_parameter_supported"`
}

// ProtectedResourceMetadata is the RFC 9728 document a resource server
// publishes so that a client which discovered the resource at runtime can find
// the authorization server behind it.
//
// It is emitted by the resource server rather than by this one, and the two are
// not necessarily the same process — which is why it is a separate mountable
// thing rather than a sixth route on the Server.
type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	ResourceName           string   `json:"resource_name,omitempty"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// normalizeIssuer vets an issuer URL and returns it without a trailing slash.
//
// RFC 8414 §2 requires an https URL with no query and no fragment, and the
// requirement is load-bearing rather than pedantry: the issuer is compared
// verbatim by clients against the "iss" in an authorization response, and
// concatenated with the endpoint paths to build the discovery document. A
// trailing slash therefore renders "https://as.example//token", which some
// clients follow and some do not.
//
// http is permitted for a loopback host, and only there. A development server
// on 127.0.0.1 is not reachable by anyone who is not already on the machine;
// anything else on http is an authorization server handing bearer tokens to the
// network.
func normalizeIssuer(issuer string) (string, error) {
	if strings.TrimSpace(issuer) == "" {
		return "", ErrEmptyIssuer
	}

	parsed, err := url.Parse(issuer)
	if err != nil {
		return "", platformerrors.Wrap(ErrInvalidIssuer, "parsing oauth2 issuer")
	}

	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return "", platformerrors.Wrapf(ErrInvalidIssuer, "oauth2 issuer %q", issuer)
	}

	switch parsed.Scheme {
	case "https":
	case "http":
		if !loopbackHost(parsed.Hostname()) {
			return "", platformerrors.Wrapf(ErrInvalidIssuer, "oauth2 issuer %q", issuer)
		}
	default:
		return "", platformerrors.Wrapf(ErrInvalidIssuer, "oauth2 issuer %q", issuer)
	}

	return strings.TrimSuffix(parsed.String(), "/"), nil
}

// loopbackHost reports whether host is one an http URL may legitimately name.
//
// "localhost" is included because that is what a human types, even though it
// resolves through the name service and could in principle be pointed
// elsewhere; on the machine where that matters, the operator has already lost.
func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
