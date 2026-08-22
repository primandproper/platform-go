// Package oauth2servercfg assembles an OAuth 2.1 authorization server, and the
// Store behind it, from environment configuration.
//
// The store lives either in memory — for tests and single-process development
// — or in a SQL table, which is what a deployment wants. The choice is not a
// performance one: with the memory provider an authorization code issued by one
// replica cannot be redeemed at another, so a fleet fails logins in proportion
// to how well its load balancer works.
//
// NewStore builds the store alone. NewServer builds the store and the server on
// top of it, and needs the things this package cannot configure: a
// SubjectAuthenticator that knows who the human is, and — optionally — a
// LoginRenderer that draws the form and a SubjectResolver that recognizes a
// resource owner who is already signed in. The optional two arrive through
// WithServerOptions.
package oauth2servercfg

import (
	"context"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	oauth2database "github.com/primandproper/platform-go/v13/authentication/oauth2server/database"
	oauth2memory "github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderMemory keeps every record in maps. For tests and single-process
	// development; see the memory package for what it cannot do.
	ProviderMemory = "memory"

	// ProviderDatabase keeps every record in SQL tables. The answer for any
	// deployment with more than one replica, which under this protocol is any
	// deployment where logins have to work.
	ProviderDatabase = "database"
)

// Config assembles an authorization server from environment configuration.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// Provider selects where the records live: memory or database.
	Provider string `env:"PROVIDER" envDefault:"database" json:"provider,omitempty" yaml:"provider,omitempty"`

	// Issuer is this server's identity: an https URL with no query or
	// fragment, no trailing slash. Every endpoint in the discovery document is
	// derived from it, and a client compares it against the "iss" in an
	// authorization response.
	//
	// http is accepted only for a loopback host, which is what a development
	// server runs on.
	Issuer string `env:"ISSUER" json:"issuer,omitempty" yaml:"issuer,omitempty"`

	// ServiceDocumentation is an optional URL advertised in the discovery
	// document.
	ServiceDocumentation string `env:"SERVICE_DOCUMENTATION" json:"serviceDocumentation,omitempty" yaml:"serviceDocumentation,omitempty"`

	// Database configures the store when Provider is database. The dialect
	// comes from the database.Client rather than from here.
	Database oauth2database.Config `env:",init" envPrefix:"DATABASE_" json:"database,omitzero" yaml:"database,omitempty"`

	// Scopes are the scopes this server issues. An authorization request for
	// anything outside the set is refused rather than narrowed; leaving it
	// empty accepts whatever a client registered for.
	//
	// Comma-separated in the environment, which is env's default for a slice
	// and so carries no envSeparator tag: spelling the default out is what put
	// the tag formatter and the tag linter in disagreement over where it goes.
	Scopes []string `env:"SCOPES" json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// Resources are the RFC 8707 resource indicators this server mints tokens
	// for, and which become an access token's audience. Leaving it empty
	// accepts whatever a client asks for, which still binds the token — to
	// something the client chose rather than something this server asserted.
	Resources []string `env:"RESOURCES" json:"resources,omitempty" yaml:"resources,omitempty"`

	// AuthorizationCodeTTL is how long an authorization code is redeemable.
	AuthorizationCodeTTL time.Duration `env:"AUTHORIZATION_CODE_TTL" json:"authorizationCodeTTL,omitempty" yaml:"authorizationCodeTTL,omitempty"`

	// AccessTokenTTL is how long an access token is usable.
	//
	// Raising it does not lengthen a session — the refresh token decides that.
	// It lengthens how long a leaked token works.
	AccessTokenTTL time.Duration `env:"ACCESS_TOKEN_TTL" json:"accessTokenTTL,omitempty" yaml:"accessTokenTTL,omitempty"`

	// RefreshTokenTTL is how long a refresh token is exchangeable.
	RefreshTokenTTL time.Duration `env:"REFRESH_TOKEN_TTL" json:"refreshTokenTTL,omitempty" yaml:"refreshTokenTTL,omitempty"`

	// ClientRegistrationTTL is how long a dynamically registered client lasts
	// before it must register again. Zero means registrations never lapse,
	// which leaves an unauthenticated endpoint writing rows nothing removes.
	ClientRegistrationTTL time.Duration `env:"CLIENT_REGISTRATION_TTL" json:"clientRegistrationTTL,omitempty" yaml:"clientRegistrationTTL,omitempty"`

	// SweepInterval is how often the store removes records past their
	// deadlines.
	//
	// A non-positive value starts no sweeper, which is right when a scheduler
	// calls Sweep instead — one sweep for the fleet rather than one per replica
	// — and wrong when nothing else does, since the tables then grow with every
	// login attempt and every anonymous registration.
	SweepInterval time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`

	// DisableDynamicRegistration stops this server serving RFC 7591 dynamic
	// client registration: /register is not routed, and the discovery document
	// leaves registration_endpoint out rather than naming an endpoint that
	// answers 404.
	//
	// It is spelled as a disable rather than an enable so that an unset
	// environment produces the protocol's own behavior — a client that
	// discovered this server at runtime can register. Turn it off for a
	// deployment whose clients are administered elsewhere, where an anonymous
	// endpoint writing to the same client table is a way around whatever
	// administers them.
	DisableDynamicRegistration bool `env:"DISABLE_DYNAMIC_REGISTRATION" json:"disableDynamicRegistration,omitempty" yaml:"disableDynamicRegistration,omitempty"`

	// DisableRefreshReuseDetection turns off revoking a token family when an
	// already-redeemed refresh token is presented.
	//
	// It is spelled as a disable rather than an enable so that the safe
	// behavior is what an unset environment produces. Turning it off turns
	// rotation into bookkeeping: the replay is refused and the copy the
	// attacker is using keeps working.
	DisableRefreshReuseDetection bool `env:"DISABLE_REFRESH_REUSE_DETECTION" json:"disableRefreshReuseDetection,omitempty" yaml:"disableRefreshReuseDetection,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
//
// The lifetimes are defaulted here as well as in the Server so that a Config
// reads as what it will actually do, rather than as a set of zeroes whose
// meaning is somewhere else.
func (cfg *Config) EnsureDefaults() {
	if cfg.Provider == "" {
		cfg.Provider = ProviderDatabase
	}
	if cfg.AuthorizationCodeTTL == 0 {
		cfg.AuthorizationCodeTTL = oauth2server.DefaultAuthorizationCodeTTL
	}
	if cfg.AccessTokenTTL == 0 {
		cfg.AccessTokenTTL = oauth2server.DefaultAccessTokenTTL
	}
	if cfg.RefreshTokenTTL == 0 {
		cfg.RefreshTokenTTL = oauth2server.DefaultRefreshTokenTTL
	}
	if cfg.ClientRegistrationTTL == 0 {
		cfg.ClientRegistrationTTL = oauth2server.DefaultClientRegistrationTTL
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = oauth2server.DefaultSweepInterval
	}
}

// ValidateWithContext validates a Config struct.
//
// The database sub-config is skipped under the memory provider rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves it populated either way.
//
// The issuer is not validated here beyond being present. Whether it is a legal
// issuer is oauth2server.NewServer's answer, and duplicating the rule would
// create a second place for it to be wrong — NewStore does not need one at all.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(ProviderMemory, ProviderDatabase)),
		validation.Field(&cfg.Database,
			validation.Skip.When(cfg.provider() != ProviderDatabase),
			validation.By(func(any) error { return cfg.Database.ValidateWithContext(ctx) })),
		validation.Field(&cfg.AuthorizationCodeTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.AccessTokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.RefreshTokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.ClientRegistrationTTL, validation.Min(time.Duration(0))),
	)
}

// provider normalizes the configured provider name, so that trailing whitespace
// out of an environment file is not a different provider.
func (cfg *Config) provider() string {
	return strings.TrimSpace(strings.ToLower(cfg.Provider))
}

// NewStore builds the authorization server's Store from configuration.
//
// db is required only when the provider is database; pass nil otherwise.
func NewStore(ctx context.Context, cfg *Config, db database.Client, opts ...Option) (oauth2server.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	o := newOptions(opts)

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating oauth2 server config")
	}

	// Every branch builds into store and returns it only once err is known to
	// be nil. Returning a concrete-typed constructor's result straight through
	// would convert a nil *database.Store into a non-nil oauth2server.Store on
	// the error path — a value that passes a caller's nil check and panics on
	// first use.
	var (
		store oauth2server.Store
		err   error
	)

	switch cfg.provider() {
	case ProviderMemory:
		store = oauth2memory.NewStore(append([]oauth2memory.Option{
			oauth2memory.WithLogger(o.logger),
			oauth2memory.WithTracerProvider(o.tracerProvider),
			oauth2memory.WithSweeper(ctx, cfg.SweepInterval),
		}, o.memoryStore...)...)
	case ProviderDatabase:
		store, err = oauth2database.NewStore(&cfg.Database, db, append([]oauth2database.Option{
			oauth2database.WithLogger(o.logger),
			oauth2database.WithTracerProvider(o.tracerProvider),
			oauth2database.WithMetricsProvider(o.metricsProvider),
			// Bound to the caller's context: the sweep stops when whatever
			// scope owns this store does.
			oauth2database.WithSweeper(ctx, cfg.SweepInterval),
		}, o.databaseStore...)...)
	default:
		// An unrecognized provider is an error rather than a working-looking
		// default. Falling back to memory would produce an authorization server
		// that signs users in, fails their next login on another replica, and
		// looks configured the whole time.
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "oauth2 store provider %q", cfg.Provider)
	}

	if err != nil {
		return nil, err
	}

	return store, nil
}

// NewServer builds an authorization server and the Store behind it.
//
// authenticator is a parameter rather than an option because it is the one
// thing no configuration can supply: it is how this deployment identifies a
// human, and a default would be a server that issues authorization codes to
// whoever asks. The login form does have a default, so it is an option —
// WithLoginRenderer.
//
// db is required only when the provider is database; pass nil otherwise.
func NewServer(
	ctx context.Context,
	cfg *Config,
	db database.Client,
	authenticator oauth2server.SubjectAuthenticator,
	opts ...Option,
) (*oauth2server.Server, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	o := newOptions(opts)

	store, err := NewStore(ctx, cfg, db, opts...)
	if err != nil {
		return nil, err
	}

	return oauth2server.NewServer(cfg.Issuer, store, authenticator, cfg.serverOptions(o)...)
}

// serverOptions renders the Config as server options, ahead of whatever the
// caller passed.
func (cfg *Config) serverOptions(o *options) []oauth2server.Option {
	opts := []oauth2server.Option{
		oauth2server.WithAuthorizationCodeTTL(cfg.AuthorizationCodeTTL),
		oauth2server.WithAccessTokenTTL(cfg.AccessTokenTTL),
		oauth2server.WithRefreshTokenTTL(cfg.RefreshTokenTTL),
		oauth2server.WithClientRegistrationTTL(cfg.ClientRegistrationTTL),
		oauth2server.WithRefreshReuseDetection(!cfg.DisableRefreshReuseDetection),
		oauth2server.WithDynamicRegistration(!cfg.DisableDynamicRegistration),
		oauth2server.WithServiceDocumentation(cfg.ServiceDocumentation),
		oauth2server.WithScopes(cfg.Scopes...),
		oauth2server.WithResources(cfg.Resources...),
		oauth2server.WithLogger(o.logger),
		oauth2server.WithTracerProvider(o.tracerProvider),
		oauth2server.WithMetricsProvider(o.metricsProvider),
	}

	return append(opts, o.server...)
}
