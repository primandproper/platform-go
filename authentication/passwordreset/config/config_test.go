package passwordresetcfg

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetmock "github.com/primandproper/platform-go/v14/authentication/passwordreset/mock"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const testEmailAddress = "person@example.com"

// newClient returns a database.Client that answers Dialect and nothing else,
// which is all NewStore reaches for.
func newClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
}

func testDBClient(t *testing.T) database.Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	client, err := databasecfg.NewDatabase(t.Context(), &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}, nil)
	must.NoError(t, err)

	return client
}

// knownDirectory is a passwordreset.Directory holding one user at
// testEmailAddress. Its password write is never reached by these tests.
type knownDirectory struct{}

func (knownDirectory) GetUserByEmailAddress(
	context.Context, database.SQLQueryExecutor, tenancy.Scope, string,
) (*identity.User, error) {
	return &identity.User{ID: "user", EmailAddress: testEmailAddress}, nil
}

func (knownDirectory) UpdateUserPassword(context.Context, database.Tx, tenancy.Scope, string, string) error {
	return nil
}

// recordingMailer counts what it was handed.
type recordingMailer struct {
	mails []*passwordreset.Mail
	mu    sync.Mutex
}

func (m *recordingMailer) SendPasswordReset(_ context.Context, mail *passwordreset.Mail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.mails = append(m.mails, mail)

	return nil
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills in every unset field", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, passwordreset.DefaultTablePrefix, cfg.TablePrefix)
		test.EqOp(t, passwordreset.DefaultTokenLifetime, cfg.TokenLifetime)
		must.NotNil(t, cfg.RequestFloor)
		test.EqOp(t, passwordreset.DefaultRequestFloor, *cfg.RequestFloor)
		must.NotNil(t, cfg.SweepInterval)
		test.EqOp(t, DefaultSweepInterval, *cfg.SweepInterval)
	})

	T.Run("leaves what was set, zero pointers included", func(t *testing.T) {
		t.Parallel()

		// A zero floor turns the padding off and a zero interval starts no
		// sweeper. Both are answers a deployment gives, and defaulting either
		// would take the answer away.
		cfg := &Config{
			TablePrefix:   "ddb",
			TokenLifetime: 15 * time.Minute,
			RequestFloor:  pointer.To(time.Duration(0)),
			SweepInterval: pointer.To(time.Duration(0)),
		}
		cfg.EnsureDefaults()

		test.EqOp(t, "ddb", cfg.TablePrefix)
		test.EqOp(t, 15*time.Minute, cfg.TokenLifetime)
		test.EqOp(t, time.Duration(0), *cfg.RequestFloor)
		test.EqOp(t, time.Duration(0), *cfg.SweepInterval)
	})
}

func TestConfig_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a zero config and a filled one", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
		must.NoError(t, (&Config{
			TablePrefix:   "ddb",
			TokenLifetime: time.Minute,
			RequestFloor:  pointer.To(time.Second),
			SweepInterval: pointer.To(time.Minute),
		}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix that cannot render", func(t *testing.T) {
		t.Parallel()

		must.Error(t, (&Config{TablePrefix: "has space"}).ValidateWithContext(t.Context()))
		must.Error(t, (&Config{TablePrefix: "trailing_"}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a negative lifetime", func(t *testing.T) {
		t.Parallel()

		must.Error(t, (&Config{TokenLifetime: -time.Minute}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a negative floor", func(t *testing.T) {
		t.Parallel()

		err := (&Config{RequestFloor: pointer.To(-time.Second)}).ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "requestFloor")
	})

	T.Run("refuses a negative sweep interval", func(t *testing.T) {
		t.Parallel()

		must.Error(t, (&Config{SweepInterval: pointer.To(-time.Second)}).ValidateWithContext(t.Context()))
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
		must.ErrorIs(t, err, passwordreset.ErrNilDatabaseClient)
		test.Nil(t, store)
	})

	T.Run("refuses an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, newClient(dialect.Dialect("oracle")))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("refuses an invalid config", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{TablePrefix: "has space"}, newClient(dialect.Postgres))
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("builds with the sweeper off", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{SweepInterval: pointer.To(time.Duration(0))},
			newClient(dialect.MySQL))
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
			WithStoreOptions(passwordreset.WithSecretBytes(passwordreset.MinimumSecretBytes)),
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

func TestNewService(T *testing.T) {
	T.Parallel()

	// issuing returns a store that records the lifetime each issuance asked for.
	issuing := func(ttls *[]time.Duration) *passwordresetmock.StoreMock {
		var mu sync.Mutex

		return &passwordresetmock.StoreMock{
			IssueFunc: func(
				_ context.Context, _ database.Tx, scope tenancy.Scope, userID string, ttl time.Duration,
			) (*passwordreset.Issuance, error) {
				mu.Lock()
				*ttls = append(*ttls, ttl)
				mu.Unlock()

				return &passwordreset.Issuance{
					Token:  &passwordreset.Token{UserID: userID, Scope: scope},
					Secret: "secret",
				}, nil
			},
		}
	}

	T.Run("issues links for the configured lifetime", func(t *testing.T) {
		t.Parallel()

		// The floor is off so the request answers at once; the lifetime is the
		// value read back, off the argument the store was handed.
		var ttls []time.Duration

		mailer := &recordingMailer{}
		svc, err := NewService(t.Context(),
			&Config{TokenLifetime: 7 * time.Minute, RequestFloor: pointer.To(time.Duration(0))},
			testDBClient(t), issuing(&ttls), knownDirectory{}, argon2.NewArgon2Authenticator(), mailer)
		must.NoError(t, err)

		must.NoError(t, svc.Request(t.Context(), tenancy.Global(), testEmailAddress))
		test.Eq(t, []time.Duration{7 * time.Minute}, ttls)
		test.SliceLen(t, 1, mailer.mails)
	})

	T.Run("applies explicit service options after the config's", func(t *testing.T) {
		t.Parallel()

		var ttls []time.Duration

		svc, err := NewService(t.Context(),
			&Config{TokenLifetime: 7 * time.Minute, RequestFloor: pointer.To(time.Duration(0))},
			testDBClient(t), issuing(&ttls), knownDirectory{}, argon2.NewArgon2Authenticator(), &recordingMailer{},
			WithServiceOptions(passwordreset.WithTokenLifetime(3*time.Minute)))
		must.NoError(t, err)

		must.NoError(t, svc.Request(t.Context(), tenancy.Global(), testEmailAddress))
		test.Eq(t, []time.Duration{3 * time.Minute}, ttls)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), nil, testDBClient(t), &passwordresetmock.StoreMock{},
			knownDirectory{}, argon2.NewArgon2Authenticator(), &recordingMailer{})
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, svc)
	})

	T.Run("refuses an invalid config", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), &Config{TokenLifetime: -time.Minute}, testDBClient(t),
			&passwordresetmock.StoreMock{}, knownDirectory{}, argon2.NewArgon2Authenticator(), &recordingMailer{})
		test.Error(t, err)
		test.Nil(t, svc)
	})

	T.Run("refuses a missing mailer", func(t *testing.T) {
		t.Parallel()

		// The leaf's refusal, reached through this constructor unchanged.
		svc, err := NewService(t.Context(), &Config{}, testDBClient(t), &passwordresetmock.StoreMock{},
			knownDirectory{}, argon2.NewArgon2Authenticator(), nil)
		test.ErrorIs(t, err, passwordreset.ErrNilMailer)
		test.Nil(t, svc)
	})

	T.Run("refuses a missing authenticator", func(t *testing.T) {
		t.Parallel()

		svc, err := NewService(t.Context(), &Config{}, testDBClient(t), &passwordresetmock.StoreMock{},
			knownDirectory{}, nil, &recordingMailer{})
		test.ErrorIs(t, err, passwordreset.ErrNilAuthenticator)
		test.Nil(t, svc)
	})
}
