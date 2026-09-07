/*
Package identitycfg assembles identity's three layers from environment
configuration: the Store, the Service over it, and the gRPC server over that.

There is very little to configure, which is why this package is smaller than
most of its siblings. The dialect comes from the database.Client so it cannot
disagree with the database the statements run against, and almost everything
else is either the schema's or an option.

Two things have to be here. The table prefix must match the prefix the
migrations were rendered with — a deployment sharing one database between
applications sets both from the same value. And the invitation lifetime is a
deployment's policy rather than a caller's, so it belongs beside the rest of
what an operator sets.

# What it does not build

The authorization requirements and the error mappings. Both are process-wide
statements a consumer makes once, not per-component construction, and neither
takes configuration: identitygrpc.Require declares the requirements onto a
builder the consumer owns, and errormappers.Register installs the mappings for
the whole domain tier. NewServer's documentation names both.
*/
package identitycfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/database"
	"github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles an identity Store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the six identity tables. It must match the prefix the
	// migrations were rendered with. Defaults to identity.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// InvitationTTL is how long an invitation lives when a client sends no
	// expiry of its own. Defaults to identitygrpc.DefaultInvitationTTL.
	//
	// It is configuration rather than a constant because the right answer
	// differs by deployment — a consumer-facing product and an internal tool
	// disagree — and it is here rather than left to the client because a link
	// that never expires is the answer a client gets by omitting a field.
	InvitationTTL time.Duration `env:"INVITATION_TTL" json:"invitationTTL,omitempty" yaml:"invitationTTL,omitempty"`

	// MaxInvitationTTL is the furthest ahead any invitation may expire, whether
	// the client named the expiry or the default above supplied it. Defaults to
	// identitygrpc.DefaultMaxInvitationTTL, and InvitationTTL may not exceed it.
	//
	// Without a ceiling the field above is only a default, and a client that
	// names its own expiry is the way around it.
	MaxInvitationTTL time.Duration `env:"MAX_INVITATION_TTL" json:"maxInvitationTTL,omitempty" yaml:"maxInvitationTTL,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = identity.DefaultTablePrefix
	}

	if cfg.InvitationTTL == 0 {
		cfg.InvitationTTL = identitygrpc.DefaultInvitationTTL
	}

	if cfg.MaxInvitationTTL == 0 {
		cfg.MaxInvitationTTL = identitygrpc.DefaultMaxInvitationTTL
	}
}

// ValidateWithContext validates a Config.
//
// The prefix is vetted against the identifiers it actually renders rather than
// against a pattern, so a prefix that is legal in isolation but produces an
// over-long index name fails here instead of at the first migration.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := validation.ValidateStructWithContext(ctx, cfg,
		// Negative is refused rather than clamped: an invitation expiring before
		// it is sent is a configuration mistake, and defaulting it would hide
		// the mistake behind links that quietly work.
		validation.Field(&cfg.InvitationTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.MaxInvitationTTL, validation.Min(time.Duration(0))),
	); err != nil {
		return err
	}

	// A default longer than the ceiling is refused here for the reason
	// identitygrpc.ErrInvitationTTLExceedsMaximum gives, and here rather than
	// only there so the mistake is reported as the configuration it is.
	if cfg.MaxInvitationTTL > 0 && cfg.InvitationTTL > cfg.MaxInvitationTTL {
		return errors.Wrapf(identitygrpc.ErrInvitationTTLExceedsMaximum,
			"invitationTTL %s exceeds maxInvitationTTL %s", cfg.InvitationTTL, cfg.MaxInvitationTTL)
	}

	return migrations.ValidatePrefix(cfg.TablePrefix)
}

// NewStore builds the Store. client must be the database holding the identity
// tables.
//
// The client is read at construction and not kept: it supplies the dialect and
// nothing else, because every write in the store takes the caller's
// database.Tx and every read takes the caller's database.SQLQueryExecutor. A
// consumer therefore holds the client itself — it is what opens the transaction
// the store's writes require, and what supplies Reader() for its reads.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *identity.SQLStore into
// a non-nil identity.Store on the error path, and a caller testing the result
// against nil would find a store that panics on first use.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (identity.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating identity config")
	}

	options := newOptions(opts)

	base := []identity.SQLStoreOption{
		identity.WithTablePrefix(cfg.TablePrefix),
		identity.WithStoreLogger(options.logger),
		identity.WithStoreTracerProvider(options.tracerProvider),
		identity.WithStoreMetricsProvider(options.metricsProvider),
	}

	store, storeErr := identity.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

// NewService builds the operations that are more than one write, over a Store.
//
// The store is a parameter rather than something this builds, because which
// Store the Service runs on is a decision: identity.Service takes the interface
// so that a consumer whose directory is not this schema still gets these
// operations. A consumer using this package's own store passes what NewStore
// returned.
//
// What commits alongside each operation is WithHooks, and it defaults to
// identity.NoopHooks — so an application with nothing to write beside an
// identity write configures nothing.
func NewService(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	store identity.Store,
	opts ...Option,
) (*identity.Service, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating identity config")
	}

	options := newOptions(opts)

	base := []identity.ServiceOption{
		identity.WithServiceLogger(options.logger),
		identity.WithServiceTracerProvider(options.tracerProvider),
		identity.WithServiceMetricsProvider(options.metricsProvider),
	}

	if options.hooks != nil {
		base = append(base, identity.WithHooks(options.hooks))
	}

	return identity.NewService(client, store, append(base, options.service...)...)
}

// NewServer builds the gRPC surface over a Service and a Store.
//
// principals is positional and required for the reason identitygrpc's own
// constructor gives: a directory server that cannot say who is calling has no
// scope to filter its reads on, and the only available default is one that
// resolves nobody. It is the consumer's, always — this module ships no session
// type.
//
// Two things this does not do, and a mount is not finished without them:
//
//   - The authorization requirements. identitygrpc.Require declares this
//     service's methods onto a builder, and the enforcer over the result is what
//     refuses a caller. Without it every RPC is undeclared, and authorization/grpc
//     denies an undeclared method.
//   - The error mappings. errormappers.Register installs the domain tier's,
//     identity's included; without it a taken username reaches a client as
//     codes.Unknown. It is one call for the whole tier rather than one per
//     component, which is why it is not made here.
//
// Both are process-wide statements rather than per-component construction, and
// neither takes configuration. See identitygrpc's package documentation for the
// assembled shape.
func NewServer(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	svc *identity.Service,
	store identity.Store,
	principals identitygrpc.PrincipalExtractor,
	opts ...Option,
) (*identitygrpc.Server, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating identity config")
	}

	options := newOptions(opts)

	base := []identitygrpc.Option{
		identitygrpc.WithLogger(options.logger),
		identitygrpc.WithTracerProvider(options.tracerProvider),
		identitygrpc.WithMetricsProvider(options.metricsProvider),
		identitygrpc.WithInvitationTTL(cfg.InvitationTTL),
		identitygrpc.WithMaxInvitationTTL(cfg.MaxInvitationTTL),
	}

	return identitygrpc.NewServer(client, svc, store, principals, append(base, options.server...)...)
}
