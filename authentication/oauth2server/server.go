package oauth2server

import (
	"context"
	stderrors "errors"
	"net/http"
	"slices"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/routing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// serviceName names this package's loggers, spans, and instruments.
const serviceName = "oauth2server"

// Server is an OAuth 2.1 authorization server: the five endpoints, the grant
// logic behind them, and nothing about who the resource owner is.
//
// It is a concrete type rather than an interface because there is one
// implementation of the protocol and there is not going to be a second. What
// swaps is underneath it — the Store — and beside it, in the seams a
// deployment owns: SubjectAuthenticator, the optional SubjectResolver, and
// LoginRenderer.
type Server struct {
	store              Store
	authenticator      SubjectAuthenticator
	resolver           SubjectResolver
	renderer           LoginRenderer
	policy             RegistrationPolicy
	revocationObserver RevocationObserver
	clock              clockReader
	o11y               observability.Observer

	ops *metrics.OperationSet

	codesIssued       metrics.Int64Counter
	tokensIssued      metrics.Int64Counter
	clientsRegistered metrics.Int64Counter
	reuseDetected     metrics.Int64Counter
	revocations       metrics.Int64Counter

	issuer               string
	serviceDocumentation string

	scopes    []string
	resources []string

	codeTTL         time.Duration
	accessTTL       time.Duration
	refreshTTL      time.Duration
	registrationTTL time.Duration

	detectRefreshReuse  bool
	dynamicRegistration bool
}

// clockReader is the sliver of clock.Clock this server uses. Naming it keeps
// the field's purpose visible: nothing here sleeps or ticks, it only stamps.
type clockReader interface {
	Now() time.Time
}

// NewServer builds an authorization server.
//
// issuer, store, and authenticator are parameters rather than options because
// none of them has a defensible default. An issuer is what every metadata
// document and every audience check is derived from; a store is where the state
// lives, and an implicit in-memory one would work in every test and fail every
// login behind a load balancer; a SubjectAuthenticator is the only thing that
// knows who the human is, and a default would be a server that issues codes to
// whoever asks.
//
// Everything else is an option, observability included.
func NewServer(issuer string, store Store, authenticator SubjectAuthenticator, opts ...Option) (*Server, error) {
	normalized, err := normalizeIssuer(issuer)
	if err != nil {
		return nil, err
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if authenticator == nil {
		return nil, ErrNilAuthenticator
	}

	o := newServerOptions(opts)

	s := &Server{
		store:                store,
		authenticator:        authenticator,
		resolver:             o.subjectResolver,
		renderer:             o.loginRenderer,
		policy:               o.registrationPolicy,
		revocationObserver:   o.revocationObserver,
		clock:                o.clock,
		o11y:                 observability.NewObserver(serviceName, o.logger, o.tracerProvider),
		issuer:               normalized,
		serviceDocumentation: o.serviceDocumentation,
		scopes:               slices.Clone(o.scopes),
		resources:            slices.Clone(o.resources),
		codeTTL:              o.codeTTL,
		accessTTL:            o.accessTTL,
		refreshTTL:           o.refreshTTL,
		registrationTTL:      o.registrationTTL,
		detectRefreshReuse:   o.detectRefreshReuse,
		dynamicRegistration:  o.dynamicRegistration,
	}

	if s.ops, err = metrics.NewOperationSet(o.metricsProvider, serviceName); err != nil {
		return nil, err
	}

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	if s.codesIssued, err = mp.NewInt64Counter(serviceName + "_codes_issued"); err != nil {
		return nil, platformerrors.Wrap(err, "creating authorization codes issued counter")
	}
	if s.tokensIssued, err = mp.NewInt64Counter(serviceName + "_tokens_issued"); err != nil {
		return nil, platformerrors.Wrap(err, "creating tokens issued counter")
	}
	if s.clientsRegistered, err = mp.NewInt64Counter(serviceName + "_clients_registered"); err != nil {
		return nil, platformerrors.Wrap(err, "creating clients registered counter")
	}
	if s.reuseDetected, err = mp.NewInt64Counter(serviceName + "_refresh_reuse_detected"); err != nil {
		return nil, platformerrors.Wrap(err, "creating refresh reuse counter")
	}
	if s.revocations, err = mp.NewInt64Counter(serviceName + "_revocations"); err != nil {
		return nil, platformerrors.Wrap(err, "creating revocations counter")
	}

	return s, nil
}

// Issuer returns the normalized issuer URL. It is what a resource server puts
// in its own metadata's authorization_servers, so it is exported rather than
// left for a caller to re-derive from the string it passed in — the
// normalization is the point.
func (s *Server) Issuer() string { return s.issuer }

// Metadata returns the RFC 8414 discovery document this server publishes.
//
// It is derived rather than configured, so what it advertises and what the
// endpoints do cannot disagree: the auth methods listed are the ones /token
// verifies, the grant types are the ones it implements, S256 is the only
// challenge method because it is the only one VerifyPKCE accepts, and
// registration_endpoint is absent entirely from a server built with
// WithDynamicRegistration(false), which is the one field here a deployment can
// turn off.
func (s *Server) Metadata() AuthorizationServerMetadata {
	registrationEndpoint := ""
	if s.dynamicRegistration {
		registrationEndpoint = s.issuer + PathRegister
	}

	return AuthorizationServerMetadata{
		Issuer:                 s.issuer,
		AuthorizationEndpoint:  s.issuer + PathAuthorize,
		TokenEndpoint:          s.issuer + PathToken,
		RegistrationEndpoint:   registrationEndpoint,
		RevocationEndpoint:     s.issuer + PathRevoke,
		ServiceDocumentation:   s.serviceDocumentation,
		ScopesSupported:        slices.Clone(s.scopes),
		ResponseTypesSupported: []string{ResponseTypeCode},
		GrantTypesSupported:    []string{GrantTypeAuthorizationCode, GrantTypeRefreshToken},
		TokenEndpointAuthMethodsSupported: []string{
			AuthMethodNone, AuthMethodClientSecret, AuthMethodClientBasic,
		},
		CodeChallengeMethodsSupported: []string{CodeChallengeMethodS256},
		RevocationEndpointAuthMethodsSupported: []string{
			AuthMethodNone, AuthMethodClientSecret, AuthMethodClientBasic,
		},
		AuthorizationResponseIssParameterSupported: true,
	}
}

// Handler returns every endpoint on one http.Handler, for a caller not using
// routing.
//
// The metadata document is served from the .well-known path RFC 8414 fixes,
// which is at the root of the host rather than under whatever prefix the rest
// is mounted at. A deployment serving the authorization server under a path
// therefore has to route that one document separately — hence
// MetadataHandler, which exists to be mounted on its own.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET "+PathAuthorizationServerMetadata, s.MetadataHandler())
	mux.Handle("GET "+PathAuthorize, s.AuthorizeHandler())
	mux.Handle("POST "+PathAuthorize, s.AuthorizeHandler())
	mux.Handle("POST "+PathToken, s.TokenHandler())
	mux.Handle("POST "+PathRevoke, s.RevokeHandler())

	// Absent rather than answering 404 from the handler, so that the routes
	// this returns are the routes the discovery document names.
	if s.dynamicRegistration {
		mux.Handle("POST "+PathRegister, s.RegisterHandler())
	}

	return mux
}

// Mount registers every endpoint on a routing.Router, with middleware applied
// to all of them.
//
// The routes are raw handlers rather than typed ones, and record no OpenAPI
// operation. That is not a shortcut: these endpoints are specified elsewhere —
// form-encoded requests, 302 responses carrying credentials in a query string,
// an HTML login page — and a generated schema describing them would be a second,
// worse copy of RFC 6749 that a client author would be wrong to read.
//
// The middleware slot is how a deployment rate limits /register, which is the
// one endpoint here that an anonymous caller can write rows through. To limit
// only that one, mount the handlers individually rather than calling this — and
// a deployment that does not want the endpoint at all says so with
// WithDynamicRegistration(false), which takes it out of the discovery document
// as well as off the router.
func (s *Server) Mount(r *routing.Router, middleware ...routing.Middleware) {
	r.Handle(http.MethodGet, PathAuthorizationServerMetadata, s.MetadataHandler(), middleware...)
	r.Handle(http.MethodGet, PathAuthorize, s.AuthorizeHandler(), middleware...)
	r.Handle(http.MethodPost, PathAuthorize, s.AuthorizeHandler(), middleware...)
	r.Handle(http.MethodPost, PathToken, s.TokenHandler(), middleware...)
	r.Handle(http.MethodPost, PathRevoke, s.RevokeHandler(), middleware...)

	// Not routed at all under WithDynamicRegistration(false); see Metadata,
	// which stops naming it in the same breath.
	if s.dynamicRegistration {
		r.Handle(http.MethodPost, PathRegister, s.RegisterHandler(), middleware...)
	}
}

// MetadataHandler serves the RFC 8414 discovery document.
func (s *Server) MetadataHandler() http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		ctx, op := s.o11y.Begin(req.Context())
		defer op.End()

		s.ops.Attempt(ctx, metric.WithAttributes(attribute.String(endpointKey, endpointMetadata)))

		// Cacheable, unlike everything else here: it carries no credential, it
		// changes only on redeploy, and a client that re-fetches it on every
		// login is a client hitting this server once per login for a constant.
		res.Header().Set("Cache-Control", "public, max-age=3600")

		writeMetadata(res, s.Metadata())
	})
}

// Authenticate resolves a bearer token to the record behind it, for a resource
// server running in this process.
//
// It is the other half of the decision to make access tokens opaque: with a
// signed token a resource server verifies a signature locally, and with this
// one it asks. What it buys is that a revoked token stops working on the next
// request rather than at the end of its lifetime — see the package doc, which
// argues the trade rather than assuming it.
//
// A resource server in a *different* process cannot call this, and this package
// deliberately ships no introspection endpoint for it to call instead: RFC 7662
// introspection is an authenticated endpoint with its own client credentials
// and its own caching questions, and adding one on the way past would be
// shipping a second protocol nobody asked for. Either share the Store, or hold
// the resource server in the same process.
func (s *Server) Authenticate(ctx context.Context, bearer string) (*AccessToken, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if bearer == "" {
		return nil, ErrEmptyIdentifier
	}

	token, err := s.store.GetAccessToken(ctx, Hash(bearer))
	if err != nil {
		// An unusable token is the ordinary case at a resource server — every
		// expired session produces one — so it is not recorded as a fault. A
		// store that is actually broken is.
		if stderrors.Is(err, ErrNotFound) {
			return nil, err
		}

		return nil, op.Error(err, "reading access token")
	}

	return token, nil
}

// now reads the clock at the resolution every stamped time uses, so records
// written here compare the same way against a database column and against a
// map entry.
func (s *Server) now() time.Time {
	return s.clock.Now().UTC().Truncate(time.Microsecond)
}

// issueTokenPair mints an access token and a refresh token in one family and
// records both.
//
// The order matters and is the opposite of the obvious one: the access token is
// written first, so that a failure between the two writes leaves a usable
// access token and no refresh token — a session that ends in fifteen minutes.
// Writing the refresh token first would leave the reverse on failure: a client
// holding a refresh token for a session it was never given, which it would use,
// producing a second family for one authorization.
func (s *Server) issueTokenPair(
	ctx context.Context,
	clientID, familyID string,
	subject Subject,
	scopes, audience, resources []string,
) (*TokenResponse, error) {
	now := s.now()

	accessValue, accessHash, err := mintCredential(ctx)
	if err != nil {
		return nil, err
	}

	access := &AccessToken{
		IssuedAt:  now,
		ExpiresAt: now.Add(s.accessTTL),
		Hash:      accessHash,
		ClientID:  clientID,
		FamilyID:  familyID,
		Subject:   subject,
		Scopes:    scopes,
		Audience:  audience,
	}

	if err = s.store.CreateAccessToken(ctx, access); err != nil {
		return nil, platformerrors.Wrap(err, "storing access token")
	}

	refreshValue, refreshHash, err := mintCredential(ctx)
	if err != nil {
		return nil, err
	}

	refresh := &RefreshToken{
		IssuedAt:  now,
		ExpiresAt: now.Add(s.refreshTTL),
		Hash:      refreshHash,
		ClientID:  clientID,
		FamilyID:  familyID,
		Subject:   subject,
		Scopes:    scopes,
		Audience:  audience,
		Resources: resources,
	}

	if err = s.store.CreateRefreshToken(ctx, refresh); err != nil {
		return nil, platformerrors.Wrap(err, "storing refresh token")
	}

	s.tokensIssued.Add(ctx, 1)

	return &TokenResponse{
		AccessToken:  accessValue,
		TokenType:    TokenTypeBearer,
		ExpiresIn:    int64(s.accessTTL.Seconds()),
		RefreshToken: refreshValue,
		Scope:        joinScopes(scopes),
	}, nil
}

// TokenResponse is the RFC 6749 §5.1 successful token response.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ExpiresIn    int64  `json:"expires_in"`
}

// newFamilyID mints the identifier that ties one authorization's tokens
// together.
//
// identifiers.New rather than crypto/rand, deliberately, and it is the one
// value in this package that is not a bearer credential: it never leaves the
// store, it authorizes nothing, and being sortable makes a family's rows sit
// together in an index and in a log.
func newFamilyID() string {
	return identifiers.New()
}

// fail records a protocol error and returns it, so a handler's error path is
// one line.
func (s *Server) fail(ctx context.Context, op observability.Operation, endpoint string, err *protocolError) *protocolError {
	s.ops.Failed(ctx, metric.WithAttributes(
		attribute.String(endpointKey, endpoint),
		attribute.String(errorCodeKey, err.code),
	))

	// A 4xx is the client being told it got something wrong, which is not this
	// server's fault and not something to wake anyone for; a 5xx is. Both are
	// on the operation, at the severity the status says.
	if err.status >= http.StatusInternalServerError {
		op.Acknowledge(err, "handling oauth2 request")
	} else {
		op.Logger().WithError(err).Info("refusing oauth2 request")
	}

	return err
}

// end closes one endpoint's operation and records its latency.
//
// Its counterpart, the o11y.BeginCustom that opens the span, is deliberately
// left at each handler's own call site rather than factored in here beside
// this. The context every store call and every seam receives has to be
// visibly derived from req.Context(), and a helper that returned one built
// somewhere else would hide that from a reader and from contextcheck alike —
// on the one path where a cancelled request must actually stop the work.
func (s *Server) end(ctx context.Context, op observability.Operation, endpoint string, startedAt time.Time) {
	s.ops.Latency.Record(ctx,
		float64(s.clock.Now().Sub(startedAt).Milliseconds()),
		metric.WithAttributes(attribute.String(endpointKey, endpoint)),
	)

	op.End()
}

// operationName is the span name one endpoint's requests carry.
func operationName(endpoint string) string {
	return serviceName + "." + endpoint
}
