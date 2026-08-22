package oauth2server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/routing"
)

// ErrEmptyResource indicates a ResourceMetadata built without a resource
// identifier. It is what a client matches its token's audience against, so an
// empty one would publish a document that authorizes nothing to be checked.
var ErrEmptyResource = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty protected resource identifier")

// ErrNoAuthorizationServer indicates a ResourceMetadata naming no authorization
// server. The document's entire purpose is to say where to go and get a token.
var ErrNoAuthorizationServer = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no authorization servers named")

// ResourceMetadata publishes the RFC 9728 protected resource metadata document.
//
// It is a separate type from Server, and separately mountable, because the
// resource server and the authorization server are not necessarily the same
// process — usually they are not. A resource server publishes this to say "the
// tokens I accept come from over there"; the authorization server publishes its
// own document at PathAuthorizationServerMetadata to say what it can do.
//
// A client that discovered the resource at runtime reads this first, follows
// authorization_servers to the other document, registers, and only then has a
// client_id. That chain is the reason any of this exists.
type ResourceMetadata struct {
	doc ProtectedResourceMetadata
}

// ResourceOption configures a ResourceMetadata.
type ResourceOption func(*ProtectedResourceMetadata)

// WithResourceName sets the human-readable name in the document.
func WithResourceName(name string) ResourceOption {
	return func(d *ProtectedResourceMetadata) { d.ResourceName = name }
}

// WithResourceDocumentation sets the documentation URL in the document.
func WithResourceDocumentation(url string) ResourceOption {
	return func(d *ProtectedResourceMetadata) { d.ResourceDocumentation = url }
}

// WithResourceScopes declares the scopes this resource understands, so a client
// knows what to ask the authorization server for.
func WithResourceScopes(scopes ...string) ResourceOption {
	return func(d *ProtectedResourceMetadata) { d.ScopesSupported = append(d.ScopesSupported, scopes...) }
}

// NewResourceMetadata builds the document a protected resource publishes.
//
// resource is the identifier a client sends as the RFC 8707 "resource"
// parameter and that this server's tokens carry as their audience — so it has
// to be the same string in both places, which is why it is a parameter here and
// not derived from a request's Host header. A document whose resource
// identifier came from the request would say something different depending on
// which proxy answered.
func NewResourceMetadata(resource string, authorizationServers []string, opts ...ResourceOption) (*ResourceMetadata, error) {
	if strings.TrimSpace(resource) == "" {
		return nil, ErrEmptyResource
	}

	if len(authorizationServers) == 0 {
		return nil, ErrNoAuthorizationServer
	}

	doc := ProtectedResourceMetadata{
		Resource:             resource,
		AuthorizationServers: slices.Clone(authorizationServers),
		// Header only. RFC 6750 also defines a form parameter and a query
		// parameter; the query parameter puts a bearer token in every access
		// log and Referer header between the client and here, and advertising
		// it would be inviting that.
		BearerMethodsSupported: []string{"header"},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(&doc)
		}
	}

	return &ResourceMetadata{doc: doc}, nil
}

// Document returns the metadata this publishes.
func (m *ResourceMetadata) Document() ProtectedResourceMetadata {
	doc := m.doc
	doc.AuthorizationServers = slices.Clone(m.doc.AuthorizationServers)
	doc.ScopesSupported = slices.Clone(m.doc.ScopesSupported)
	doc.BearerMethodsSupported = slices.Clone(m.doc.BearerMethodsSupported)

	return doc
}

// Handler serves the document.
func (m *ResourceMetadata) Handler() http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		// Cacheable: it carries no credential and changes only on redeploy.
		res.Header().Set("Cache-Control", "public, max-age=3600")

		writeMetadata(res, m.Document())
	})
}

// Mount registers the document on a routing.Router at the path RFC 9728 fixes.
func (m *ResourceMetadata) Mount(r *routing.Router, middleware ...routing.Middleware) {
	r.Handle(http.MethodGet, PathProtectedResourceMetadata, m.Handler(), middleware...)
}

// Challenge renders the WWW-Authenticate header a protected resource sends with
// a 401.
//
// This is the other half of discovery and the half that is easy to leave out.
// A client with no token has no reason to fetch the metadata document until
// something tells it to, and RFC 9728 §5.1 makes this header that something:
// the resource_metadata parameter points at the document, and the client
// follows it, registers, and comes back. Without the header, a client that was
// never configured with this server simply gets a 401 and stops.
//
// errorCode is an RFC 6750 §3.1 code — "invalid_token" for a token that is
// expired, revoked, or unknown; empty for a request that carried no token at
// all, which is not an error so much as an absence.
func (m *ResourceMetadata) Challenge(errorCode, description string) string {
	// The trailing slash is trimmed rather than assumed absent. A resource
	// identifier is conventionally written with one — "https://api.example/" —
	// and concatenating the path onto that renders a double slash, which some
	// clients follow and some do not. The identifier in the document keeps
	// whatever form it was given, because that is the string a token's audience
	// is compared against; only the derived URL is normalized.
	challenge := fmt.Sprintf(`Bearer resource_metadata=%q`,
		strings.TrimSuffix(m.doc.Resource, "/")+PathProtectedResourceMetadata)

	if errorCode != "" {
		challenge += fmt.Sprintf(`, error=%q`, errorCode)
	}

	if description != "" {
		challenge += fmt.Sprintf(`, error_description=%q`, description)
	}

	return challenge
}
