package oauth2clients

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// serviceLayerName scopes the service's spans, logger and instruments.
const serviceLayerName = serviceName + "_service"

// Service is the orchestration over a [Store]: the operations that are more
// than one write, and the transaction they share with a consumer's own.
//
// # What it adds over the store
//
// Three things the store deliberately does not do. It mints the credentials, so
// that there is one place deciding how long a client secret is and what digests
// it. It owns the transaction, so a consumer's audit entry and outbox row commit
// with the registration or not at all — which is what [Hooks] is for. And it
// reads a row back before withdrawing it, so the hook recording the withdrawal
// can say what was withdrawn.
//
// Reads are not here. They are one store call with no hook and no transaction to
// own, so a service method over them would be a second name for the store's,
// and the transport holds both handles for that reason.
type Service struct {
	client   database.Client
	store    Store
	hooks    Hooks
	generate CredentialGenerator
	o11y     observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewService builds the orchestration layer over a Store.
//
// The client is kept, unlike the store's, because opening the transaction is
// what this layer is for: every operation runs inside Client.WithTransaction.
// The store is the seam the writes go through, and it is the interface rather
// than *SQLStore so that a consumer whose registry is not this schema still gets
// these operations.
//
// Hooks default to [NoopHooks] and credentials to crypto/rand, so a consumer
// with nothing to commit alongside a registration configures neither.
// Observability is optional and defaults to nothing.
func NewService(client database.Client, store Store, opts ...ServiceOption) (*Service, error) {
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if store == nil {
		return nil, ErrNilStore
	}

	s := &Service{
		client:   client,
		store:    store,
		hooks:    NoopHooks{},
		generate: generateCredentials,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serviceLayerName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serviceLayerName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating oauth2clients service instruments")
	}

	s.instruments = instruments

	return s, nil
}

// CreateClient registers a client and returns its secret, once.
//
// # What the caller decides and what this decides
//
// The caller names the registry and the owner, and both are arguments rather
// than fields on the input for the reason the tenancy convention gives: a scope
// read off a struct somebody assembled elsewhere is a scope nothing checked.
// Passing tenancy.Global() and an empty userID registers an administered client
// — one that belongs to no tenant and no person, and that [Client.Admits] will
// let authorize anybody. That is the arrangement a deployment mints operator
// credentials in, and it is why creating one sits behind a permission in the
// transport while creating your own does not.
//
// Everything else is this method's: the row id, the client_id, the secret and
// its digest, and the creation time, which is the database's.
//
// # The secret
//
// It is returned on the [IssuedClient] and stored only as a digest. There is no
// read that recovers it and no second call that reissues it — a caller who
// loses it archives the registration and mints another, which is the behavior
// that makes the digest worth storing in the first place.
func (s *Service) CreateClient(
	ctx context.Context,
	scope tenancy.Scope,
	ownerID string,
	input *CreationInput,
) (*IssuedClient, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(ownerKey, ownerID),
	)
	defer op.End()

	if input == nil {
		return nil, op.Error(ErrNilInput, "creating oauth2 client")
	}

	clientID, secret, err := s.generate()
	if err != nil {
		return nil, op.Error(err, "creating oauth2 client")
	}

	client := &Client{
		Scope:         scope,
		BelongsToUser: ownerID,
		ID:            identifiers.New(),
		ClientID:      clientID,
		SecretHash:    hashSecret(secret),
		Name:          input.Name,
		Description:   input.Description,
		RedirectURIs:  input.RedirectURIs,
		Scopes:        input.Scopes,
	}

	err = s.run(ctx, op, "create", func(tx database.Tx) error {
		if writeErr := s.store.CreateClient(ctx, tx, scope, client); writeErr != nil {
			return writeErr
		}

		return s.hooks.AfterCreateClient(ctx, tx, scope, client)
	})
	if err != nil {
		return nil, op.Error(err, "creating oauth2 client")
	}

	return &IssuedClient{Client: client, Secret: secret}, nil
}

// UpdateClient revises one registration's descriptive fields and returns the row
// as it now stands.
//
// It cannot rotate the secret or reassign the owner; see [UpdateInput] for why
// neither is an omission.
func (s *Service) UpdateClient(
	ctx context.Context,
	scope tenancy.Scope,
	id string,
	input *UpdateInput,
) (*Client, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(clientKey, id),
	)
	defer op.End()

	if input == nil {
		return nil, op.Error(ErrNilInput, "updating oauth2 client")
	}

	var updated *Client

	err := s.run(ctx, op, "update", func(tx database.Tx) error {
		if err := s.store.UpdateClient(ctx, tx, scope, id, input); err != nil {
			return err
		}

		// Read back on the transaction, not on a second connection: what the
		// hook and the caller see is what this operation wrote, including the
		// last_updated_at the statement stamped.
		client, err := s.store.GetClient(ctx, tx, scope, id)
		if err != nil {
			return err
		}

		updated = client

		return s.hooks.AfterUpdateClient(ctx, tx, scope, client)
	})
	if err != nil {
		return nil, op.Error(err, "updating oauth2 client %q", id)
	}

	return updated, nil
}

// ArchiveClient withdraws one registration.
//
// The row is read before it is archived, and the hook is handed what it read.
// That ordering is the whole reason this is a service operation rather than a
// store call: after the write the name and the redirect URIs are still there,
// but an audit entry written from the id alone would record that *something*
// was withdrawn, and the record of what the credential was for is exactly what
// somebody reading that entry later needs.
func (s *Service) ArchiveClient(ctx context.Context, scope tenancy.Scope, id string) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(clientKey, id),
	)
	defer op.End()

	err := s.run(ctx, op, "archive", func(tx database.Tx) error {
		client, err := s.store.GetClient(ctx, tx, scope, id)
		if err != nil {
			return err
		}

		if err = s.store.ArchiveClient(ctx, tx, scope, id); err != nil {
			return err
		}

		return s.hooks.AfterArchiveClient(ctx, tx, scope, client)
	})

	return op.Error(err, "archiving oauth2 client %q", id)
}

// run is the shape every operation here has, in one place: the instruments, the
// transaction, and the error the transaction is aborted with.
//
// It exists because the three can be got wrong separately and silently. An
// operation that forgot to count an attempt leaves a latency histogram with no
// denominator; one that opened no transaction leaves a consumer's hook writing
// on a connection of its own; one that swallowed the callback's error commits a
// half-finished operation.
func (s *Service) run(
	ctx context.Context,
	op observability.Operation,
	name string,
	fn func(tx database.Tx) error,
) (err error) {
	attr := operationAttr(name)

	s.instruments.Attempt(ctx, attr)

	defer op.Time(ctx, nil, s.instruments.Latency, attr)()

	defer func() {
		if err != nil {
			s.instruments.Failed(ctx, attr)
		}
	}()

	return s.client.WithTransaction(ctx, fn)
}

// operationAttr labels one operation's instruments, so the three share a trio
// rather than declaring one each.
func operationAttr(name string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(serviceName+".operation", name))
}
