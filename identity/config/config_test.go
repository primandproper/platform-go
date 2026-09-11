package identitycfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newClient returns a database.Client that answers Dialect and nothing else,
// which is all NewStore reaches for.
func newClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
}

func TestConfig_EnsureDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.EnsureDefaults()
	test.EqOp(t, identity.DefaultTablePrefix, cfg.TablePrefix)

	set := &Config{TablePrefix: "ddb"}
	set.EnsureDefaults()
	test.EqOp(t, "ddb", set.TablePrefix)
}

func TestConfig_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a renderable prefix", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
		must.NoError(t, (&Config{TablePrefix: "ddb"}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		// Vetted against the identifiers it actually produces, so a prefix that
		// is legal alone and yields an over-long index name fails at
		// construction rather than at the first migration.
		must.Error(t, (&Config{TablePrefix: "has space"}).ValidateWithContext(t.Context()))
		must.Error(t, (&Config{TablePrefix: "trailing_"}).ValidateWithContext(t.Context()))
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Postgres))
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), nil, newClient(dialect.Postgres))
		must.ErrorIs(t, err, errors.ErrNilInputParameter)

		// The interface must be nil, not a non-nil interface holding a nil
		// pointer — a caller testing the result against nil would otherwise find
		// a store that panics on first use.
		test.Nil(t, store)
	})

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, nil)
		must.ErrorIs(t, err, identity.ErrNilDatabaseClient)
		test.Nil(t, store)
	})

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Dialect("oracle")))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("refuses an invalid prefix", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "has space"}, newClient(dialect.Postgres))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("applies explicit options after the config's", func(t *testing.T) {
		t.Parallel()

		// A caller can override anything the config derived, the table prefix
		// included.
		store, err := NewStore(t.Context(), &Config{TablePrefix: "ddb"}, newClient(dialect.Postgres),
			WithStoreOptions(identity.WithTablePrefix("override")))
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("takes no observability at all", func(t *testing.T) {
		t.Parallel()

		// Absent means noop: a caller wanting none of the three names none.
		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.SQLite),
			WithPillars(nil),
			WithLogger(nil),
			WithTracerProvider(nil),
			WithMetricsProvider(nil),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("takes pillars", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.MySQL),
			WithPillars(&observability.Pillars{}))
		must.NoError(t, err)
		must.NotNil(t, store)
	})
}

func TestConfig_EnsureDefaultsFillsTheInvitationLifetime(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.EnsureDefaults()
	test.EqOp(t, identitygrpc.DefaultInvitationTTL, cfg.InvitationTTL)

	set := &Config{InvitationTTL: time.Hour}
	set.EnsureDefaults()
	test.EqOp(t, time.Hour, set.InvitationTTL)
}

// TestConfig_ValidateRefusesANegativeInvitationLifetime covers the one field
// here that can be set to something actively harmful. A negative lifetime is an
// invitation that expired before it was sent, and defaulting it would hide the
// mistake behind links that quietly work.
func TestConfig_ValidateRefusesANegativeInvitationLifetime(t *testing.T) {
	t.Parallel()

	must.Error(t, (&Config{InvitationTTL: -time.Hour}).ValidateWithContext(t.Context()))
}

func TestNewService(T *testing.T) {
	T.Parallel()

	T.Run("builds a service over the store it is given", func(t *testing.T) {
		t.Parallel()

		client := newClient(dialect.Postgres)

		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{}, client, store)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("defaults to no hooks", func(t *testing.T) {
		t.Parallel()

		// An application with nothing to commit beside an identity write
		// configures nothing, so a Service built without WithHooks has to be
		// usable rather than nil-hooked.
		client := newClient(dialect.Postgres)

		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{}, client, store, WithHooks(nil))
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("applies caller options after the ones it derives", func(t *testing.T) {
		t.Parallel()

		// Hooks are an option this constructor sets from WithHooks, so a
		// caller's own identity.WithHooks passed through WithServiceOptions has
		// to be applied after it — that ordering is what makes the pass-through
		// an override rather than a suggestion.
		client := newClient(dialect.Postgres)

		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{}, client, store,
			WithHooks(identity.NoopHooks{}),
			WithServiceOptions(identity.WithHooks(identity.NoopHooks{})))
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), nil, newClient(dialect.Postgres), nil)
		test.Nil(t, svc)
		must.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("refuses a config that does not validate", func(t *testing.T) {
		t.Parallel()

		client := newClient(dialect.Postgres)

		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{TablePrefix: "has space"}, client, store)
		test.Nil(t, svc)
		must.Error(t, err)
	})
}

func TestNewServer(T *testing.T) {
	T.Parallel()

	principal := func(context.Context) (identitygrpc.Principal, bool) { return nil, false }

	build := func(t *testing.T, cfg *Config, opts ...Option) (*identitygrpc.Server, error) {
		t.Helper()

		client := newClient(dialect.Postgres)

		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{}, client, store)
		must.NoError(t, err)

		return NewServer(t.Context(), cfg, svc, store, client, principal, opts...)
	}

	T.Run("builds a server", func(t *testing.T) {
		t.Parallel()

		srv, err := build(t, &Config{})
		must.NoError(t, err)
		test.NotNil(t, srv)
	})

	T.Run("refuses a nil principal extractor", func(t *testing.T) {
		t.Parallel()

		// The one dependency with no safe default: a server that cannot resolve
		// a caller has no scope to filter its reads on, and the zero scope is a
		// real directory rather than an empty one.
		client := newClient(dialect.Postgres)

		store, err := NewStore(t.Context(), &Config{}, client)
		must.NoError(t, err)

		svc, err := NewService(t.Context(), &Config{}, client, store)
		must.NoError(t, err)

		srv, err := NewServer(t.Context(), &Config{}, svc, store, client, nil)
		test.Nil(t, srv)
		must.Error(t, err)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		srv, err := build(t, nil)
		test.Nil(t, srv)
		must.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("applies caller options after the ones it derives", func(t *testing.T) {
		t.Parallel()

		// The configured lifetime is an option this constructor sets, so a
		// caller's own WithInvitationTTL has to win — that ordering is what
		// makes WithServerOptions an override rather than a suggestion.
		srv, err := build(t, &Config{InvitationTTL: time.Hour},
			WithServerOptions(identitygrpc.WithInvitationTTL(2*time.Hour)))
		must.NoError(t, err)
		test.NotNil(t, srv)
	})
}

func TestConfig_EnsureDefaultsFillsTheMaximumInvitationLifetime(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.EnsureDefaults()
	test.EqOp(t, identitygrpc.DefaultMaxInvitationTTL, cfg.MaxInvitationTTL)

	set := &Config{MaxInvitationTTL: time.Hour}
	set.EnsureDefaults()
	test.EqOp(t, time.Hour, set.MaxInvitationTTL)
}

// TestConfig_ValidateRefusesADefaultBeyondTheMaximum: a default lifetime longer
// than the ceiling is a server that would issue, on a request naming nothing,
// an invitation it would have refused the request for naming. It is reported
// here as the configuration mistake it is rather than only by the constructor.
func TestConfig_ValidateRefusesADefaultBeyondTheMaximum(t *testing.T) {
	t.Parallel()

	err := (&Config{InvitationTTL: 2 * time.Hour, MaxInvitationTTL: time.Hour}).ValidateWithContext(t.Context())
	test.ErrorIs(t, err, identitygrpc.ErrInvitationTTLExceedsMaximum)

	must.Error(t, (&Config{MaxInvitationTTL: -time.Hour}).ValidateWithContext(t.Context()))
	must.NoError(t, (&Config{InvitationTTL: time.Hour, MaxInvitationTTL: time.Hour}).ValidateWithContext(t.Context()))
}
