package authserver

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
)

// storeName scopes the store decorator's spans, logger and instruments.
const storeName = "oauth2clients_authserver_store"

var _ oauth2server.Store = (*Store)(nil)

// Store is an oauth2server.Store with its client half redirected at an
// administered registry.
//
// Codes, access tokens and refresh tokens are protocol records and delegate to
// the wrapped store unchanged. A registration is not a protocol record: it is an
// administered object with a listing surface, permissions, an archival lifecycle
// and a hook that audits it, none of which oauth2server.Store models — its
// client half is sized for the anonymous RFC 7591 registration a deployment
// using this registry does not serve.
//
// The embedding is deliberate rather than a field: a method added to
// oauth2server.Store later reaches this type without an edit here, and only the
// three that are overridden below are this package's.
type Store struct {
	// Store is the wrapped protocol store, supplying the credential half.
	oauth2server.Store

	registry oauth2clients.Store
	client   database.Client
	o11y     observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	opts observabilityOptions
}

// NewStore wraps a protocol store so that its clients come from the registry.
//
// The database client is taken for Reader(), which is what the lookup runs on:
// resolving a client_id happens outside any transaction of the consumer's,
// because /authorize is not inside one.
//
// Observability is optional and defaults to nothing: an unconfigured store logs
// to a noop logger, traces to a noop provider and counts through a noop metrics
// provider. What it records is the lookup and nothing else — the credential half
// is the wrapped store's and is delegated uninstrumented, because a decorator
// that timed somebody else's methods would report them twice.
//
// Every [Store.GetClient] is one attempt. An empty client_id, a registration
// this registry never issued, one that has been withdrawn, and a registry that
// is down are four failures, counted the same and told apart in the span and
// the log line — which is the point of deciding them here, since all four reach
// an unauthenticated caller as the one answer that discloses nothing.
func NewStore(
	wrapped oauth2server.Store,
	registry oauth2clients.Store,
	client database.Client,
	opts ...StoreOption,
) (*Store, error) {
	if wrapped == nil {
		return nil, oauth2server.ErrNilStore
	}

	if registry == nil {
		return nil, oauth2clients.ErrNilStore
	}

	if client == nil {
		return nil, oauth2clients.ErrNilDatabaseClient
	}

	s := &Store{Store: wrapped, registry: registry, client: client}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(storeName, s.opts.logger, s.opts.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.opts.metricsProvider, storeName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating oauth2clients authserver store instruments")
	}

	s.instruments = instruments

	return s, nil
}

// GetClient resolves a registration out of the registry and renders it as the
// authorization server's own type.
//
// It grants nobody anything, and cannot: it has no subject. A registration that
// is absent, archived, or belonging to a registry the eventual subject is not in
// are three different situations, and only the first two can be decided here —
// the third is oauth2clients.Client.Admits, in the login seams. See this
// package's documentation.
//
// An archived registration is oauth2server.ErrNotFound. The registry returns the
// row rather than hiding it, precisely so this decision is made somewhere a
// reason can be recorded; what reaches an unauthenticated caller is still the
// answer that discloses nothing.
func (s *Store) GetClient(ctx context.Context, clientID string) (*oauth2server.Client, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(clientIDKey, clientID))
	defer op.End()

	s.instruments.Attempt(ctx)

	if clientID == "" {
		s.instruments.Failed(ctx)

		return nil, op.Error(oauth2server.ErrEmptyIdentifier, "resolving an oauth2 client")
	}

	registered, err := s.registry.ResolveClientID(ctx, s.client.Reader(), clientID)
	if err != nil {
		s.instruments.Failed(ctx)

		// The registry's sentinel becomes the protocol's, and anything else is
		// wrapped rather than passed through. Without this a broken database
		// reaches an unauthenticated caller as the driver's own text — which is
		// the defect this decorator exists to have already fixed.
		if platformerrors.Is(err, oauth2clients.ErrClientNotFound) {
			return nil, op.Error(platformerrors.Wrapf(oauth2server.ErrNotFound, "oauth2 client %q", clientID),
				"resolving oauth2 client %q", clientID)
		}

		return nil, op.Error(err, "resolving oauth2 client %q", clientID)
	}

	// The registry this client_id resolved to, recorded on the span. It is what
	// an operator needs to read the refusals the login seams make later in the
	// same request, and it is a fact no other layer of the authorization server
	// holds.
	op.SpanOnly(scopeKey, registered.Scope.String())

	if registered.Archived() {
		s.instruments.Failed(ctx)

		return nil, op.Error(platformerrors.Wrapf(oauth2server.ErrNotFound,
			"oauth2 client %q has been withdrawn", clientID), "resolving oauth2 client %q", clientID)
	}

	return protocolClient(registered), nil
}

// CreateClient refuses: registrations are administered through
// authentication/oauth2clients, not minted by an anonymous caller.
//
// This is defense in depth and not the control. The control is
// oauth2server.WithDynamicRegistration(false), which takes /register off the
// router and out of the discovery document, so a client never learns the
// endpoint exists. What this stops is a deployment that forgot that option,
// where the alternative is an anonymous writer adding rows to a table with a
// permissioned listing API in front of it.
func (s *Store) CreateClient(context.Context, *oauth2server.Client) error {
	return oauth2server.ErrRegistrationNotServed
}

// DeleteClient refuses, for the reason [Store.CreateClient] gives.
//
// Withdrawal is oauth2clients.Service.ArchiveClient, which soft-deletes and runs
// the consumer's hook. A hard delete here would also be the wrong operation: a
// client_id names tokens that may still be live.
func (s *Store) DeleteClient(context.Context, string) error {
	return oauth2server.ErrRegistrationNotServed
}

// protocolClient renders a registration as the authorization server's client.
//
// Four of the fields are constants rather than columns, and each is a fact about
// what this deployment implements rather than about the row:
//
// TokenEndpointAuthMethod is client_secret_post because every registration in
// this registry holds a secret — the registry models no public client.
//
// GrantTypes and ResponseTypes are the ones oauth2server implements. OAuth 2.1
// removed the implicit and password grants and that package will not serve them,
// so a column offering them would be a column the server ignores.
//
// ExpiresAt is the zero time, meaning a registration that does not lapse. The
// authorization server's own client table carries an expiry to bound a table an
// anonymous caller writes to; an administered registration is withdrawn by
// somebody instead, and the column that records it is archived_at.
func protocolClient(c *oauth2clients.Client) *oauth2server.Client {
	return &oauth2server.Client{
		CreatedAt:  c.CreatedAt,
		ID:         c.ClientID,
		SecretHash: c.SecretHash,
		Name:       c.Name,

		TokenEndpointAuthMethod: oauth2server.AuthMethodClientSecret,
		RedirectURIs:            c.RedirectURIs,
		GrantTypes: []string{
			oauth2server.GrantTypeAuthorizationCode,
			oauth2server.GrantTypeRefreshToken,
		},
		ResponseTypes: []string{oauth2server.ResponseTypeCode},
		Scopes:        c.Scopes,
	}
}
