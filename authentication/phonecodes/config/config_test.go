package phonecodescfg

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/phonecodes"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newClient returns a database.Client that answers Dialect and nothing else,
// which is all NewStore reaches for.
func newClient(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{DialectFunc: func() dialect.Dialect { return d }}
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills every unset field", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, phonecodes.DefaultTablePrefix, cfg.TablePrefix)
		test.EqOp(t, phonecodes.DefaultCodeLength, cfg.CodeLength)
		test.EqOp(t, phonecodes.DefaultLifetime, cfg.Lifetime)
		test.EqOp(t, phonecodes.DefaultMaxAttempts, cfg.MaxAttempts)
		test.EqOp(t, phonecodes.DefaultRetention, cfg.Retention)
		must.NotNil(t, cfg.SweepInterval)
		test.EqOp(t, DefaultSweepInterval, *cfg.SweepInterval)
	})

	T.Run("leaves what a deployment set", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			TablePrefix:   "ddb",
			CodeLength:    8,
			Lifetime:      time.Minute,
			MaxAttempts:   3,
			Retention:     time.Hour,
			SweepInterval: pointer.To(time.Duration(0)),
		}
		cfg.EnsureDefaults()

		test.EqOp(t, "ddb", cfg.TablePrefix)
		test.EqOp(t, 8, cfg.CodeLength)
		test.EqOp(t, time.Minute, cfg.Lifetime)
		test.EqOp(t, 3, cfg.MaxAttempts)
		test.EqOp(t, time.Hour, cfg.Retention)
		test.EqOp(t, time.Duration(0), *cfg.SweepInterval)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts the zero value, which is unset", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses what the store would", func(t *testing.T) {
		t.Parallel()

		for name, cfg := range map[string]*Config{
			"a prefix that cannot render": {TablePrefix: "trailing_"},
			"a code too short":            {CodeLength: phonecodes.MinCodeLength - 1},
			"a code too long":             {CodeLength: phonecodes.MaxCodeLength + 1},
			"a negative lifetime":         {Lifetime: -time.Second},
			"a negative retention":        {Retention: -time.Second},
			"a negative attempt limit":    {MaxAttempts: -1},
			"a negative sweep interval":   {SweepInterval: pointer.To(-time.Second)},
		} {
			test.Error(t, cfg.ValidateWithContext(t.Context()), test.Sprint(name))
		}
	})
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{SweepInterval: pointer.To(time.Duration(0))}, newClient(dialect.Postgres))
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), nil, newClient(dialect.Postgres))
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
	})

	T.Run("refuses an invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), &Config{CodeLength: 2}, newClient(dialect.Postgres))
		test.Error(t, err)
	})

	// The nil-interface hazard: a failure has to come back as a nil Store, not
	// as a nil *phonecodes.SQLStore inside a non-nil interface.
	T.Run("a failed build is a nil store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(t.Context(), &Config{}, nil)
		test.ErrorIs(t, err, phonecodes.ErrNilDatabaseClient)
		test.True(t, store == nil)
	})

	T.Run("store options apply after the config's", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), &Config{SweepInterval: pointer.To(time.Duration(0))},
			newClient(dialect.Postgres),
			WithStoreOptions(phonecodes.WithCodeLength(1)))
		test.ErrorIs(t, err, phonecodes.ErrInvalidSetting)
	})
}
