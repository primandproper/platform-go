package databasecfg

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	encryptioncfg "github.com/primandproper/platform-go/v2/cryptography/encryption/config"
	"github.com/primandproper/platform-go/v2/database"
	"github.com/primandproper/platform-go/v2/database/mysql"
	"github.com/primandproper/platform-go/v2/database/postgres"
	"github.com/primandproper/platform-go/v2/database/sqlite"
	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/XSAM/otelsql"
	validation "github.com/go-ozzo/ozzo-validation/v4"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	ProviderPostgres = "postgres"
	ProviderMySQL    = "mysql"
	ProviderSQLite   = "sqlite"
)

type (
	// Config represents our database configuration.
	Config struct {
		_ struct{} `json:"-"`

		Encryption               encryptioncfg.Config `env:"init"                        envPrefix:"ENCRYPTION_"         json:"encryption"`
		OAuth2TokenEncryptionKey string               `env:"OAUTH2_TOKEN_ENCRYPTION_KEY" json:"oauth2TokenEncryptionKey"`
		Provider                 string               `env:"PROVIDER"                    envDefault:"postgres"           json:"provider"`
		ReadConnection           ConnectionDetails    `envPrefix:"READ_CONNECTION_"      json:"readConnection"`
		WriteConnection          ConnectionDetails    `envPrefix:"WRITE_CONNECTION_"     json:"writeConnection"`
		PingWaitPeriod           time.Duration        `env:"PING_WAIT_PERIOD"            envDefault:"1s"                 json:"pingWaitPeriod"`
		MaxPingAttempts          uint64               `env:"MAX_PING_ATTEMPTS"           json:"maxPingAttempts"`
		ConnMaxLifetime          time.Duration        `env:"CONN_MAX_LIFETIME"           envDefault:"30m"                json:"connMaxLifetime"`
		MaxIdleConns             uint16               `env:"MAX_IDLE_CONNS"              envDefault:"5"                  json:"maxIdleConns"`
		MaxOpenConns             uint16               `env:"MAX_OPEN_CONNS"              envDefault:"7"                  json:"maxOpenConns"`
		Debug                    bool                 `env:"DEBUG"                       json:"debug"`
		LogQueries               bool                 `env:"LOG_QUERIES"                 json:"logQueries"`
		RunMigrations            bool                 `env:"RUN_MIGRATIONS"              json:"runMigrations"`
		EnableDatabaseMetrics    bool                 `env:"ENABLE_DATABASE_METRICS"     json:"enableDatabaseMetrics"`
	}

	ConnectionDetails struct {
		_ struct{} `json:"-"`

		Username   string `env:"USERNAME"    json:"username"`
		Password   string `env:"PASSWORD"    json:"password"`
		Database   string `env:"DATABASE"    json:"database"`
		Host       string `env:"HOST"        json:"hostname"`
		Port       uint16 `env:"PORT"        json:"port"`
		DisableSSL bool   `env:"DISABLE_SSL" json:"disableSSL"`
	}
)

const (
	defaultPingWaitPeriod  = 1 * time.Second
	defaultConnMaxLifetime = 30 * time.Minute
	defaultMaxIdleConns    = 5
	defaultMaxOpenConns    = 7
)

var (
	_ validation.ValidatableWithContext = (*Config)(nil)
	_ database.ClientConfig             = (*Config)(nil)
)

// EnsureDefaults sets sensible defaults for zero-valued fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.Provider == "" {
		cfg.Provider = ProviderPostgres
	}

	if cfg.PingWaitPeriod == 0 {
		cfg.PingWaitPeriod = defaultPingWaitPeriod
	}

	if cfg.ConnMaxLifetime == 0 {
		cfg.ConnMaxLifetime = defaultConnMaxLifetime
	}

	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = defaultMaxIdleConns
	}

	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = defaultMaxOpenConns
	}
}

// GetReadConnectionString implements database.ClientConfig.
func (cfg *Config) GetReadConnectionString() string {
	return cfg.connectionStringForProvider(cfg.ReadConnection)
}

// GetWriteConnectionString implements database.ClientConfig.
func (cfg *Config) GetWriteConnectionString() string {
	return cfg.connectionStringForProvider(cfg.WriteConnection)
}

func (cfg *Config) connectionStringForProvider(cd ConnectionDetails) string {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ProviderMySQL:
		return cd.MySQLDSN()
	case ProviderSQLite:
		return cd.SQLiteDSN()
	default:
		return cd.String()
	}
}

// GetMaxPingAttempts implements database.ClientConfig.
func (cfg *Config) GetMaxPingAttempts() uint64 {
	return cfg.MaxPingAttempts
}

// GetPingWaitPeriod implements database.ClientConfig.
func (cfg *Config) GetPingWaitPeriod() time.Duration {
	return cfg.PingWaitPeriod
}

// GetMaxIdleConns implements database.ClientConfig.
// Returns 5 when unset (zero).
func (cfg *Config) GetMaxIdleConns() int {
	if cfg.MaxIdleConns == 0 {
		return 5
	}
	return int(cfg.MaxIdleConns)
}

// GetMaxOpenConns implements database.ClientConfig.
// Returns 7 when unset (zero).
func (cfg *Config) GetMaxOpenConns() int {
	if cfg.MaxOpenConns == 0 {
		return 7
	}
	return int(cfg.MaxOpenConns)
}

// GetConnMaxLifetime implements database.ClientConfig.
// Returns 30m when unset (zero).
func (cfg *Config) GetConnMaxLifetime() time.Duration {
	if cfg.ConnMaxLifetime <= 0 {
		return 30 * time.Minute
	}
	return cfg.ConnMaxLifetime
}

// ValidateWithContext validates an DatabaseSettings struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(
		ctx,
		cfg,
		validation.Field(&cfg.ReadConnection, validation.Required),
	)
}

// LoadConnectionDetailsFromURL wraps an inner function.
func (cfg *Config) LoadConnectionDetailsFromURL(u string) error {
	return cfg.ReadConnection.LoadFromURL(u)
}

func (cfg *Config) driverName() string {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ProviderMySQL:
		return "mysql"
	case ProviderSQLite:
		return "sqlite"
	default:
		return "pgx"
	}
}

func (cfg *Config) connectToDatabase(connStr string) (*sql.DB, error) {
	db, err := otelsql.Open(cfg.driverName(), connStr, otelsql.WithAttributes(
		attribute.KeyValue{
			Key:   semconv.ServiceNameKey,
			Value: attribute.StringValue("database"),
		},
	))
	if err != nil {
		return nil, errors.Wrapf(err, "connecting to %s database", cfg.Provider)
	}

	db.SetMaxIdleConns(cfg.GetMaxIdleConns())
	db.SetMaxOpenConns(cfg.GetMaxOpenConns())
	db.SetConnMaxLifetime(cfg.GetConnMaxLifetime())

	return db, nil
}

func (cfg *Config) ConnectToReadDatabase() (*sql.DB, error) {
	return cfg.connectToDatabase(cfg.GetReadConnectionString())
}

func (cfg *Config) ConnectToWriteDatabase() (*sql.DB, error) {
	return cfg.connectToDatabase(cfg.GetWriteConnectionString())
}

// ValidateWithContext validates an DatabaseSettings struct.
func (x *ConnectionDetails) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(
		ctx,
		x,
		validation.Field(&x.Host, validation.Required),
		validation.Field(&x.Database, validation.Required),
		validation.Field(&x.Username, validation.Required),
		validation.Field(&x.Password, validation.Required),
		validation.Field(&x.Port, validation.Required),
	)
}

var _ fmt.Stringer = (*ConnectionDetails)(nil)

func (x *ConnectionDetails) String() string {
	return fmt.Sprintf(
		"user=%s password=%s database=%s host=%s port=%d",
		x.Username,
		x.Password,
		x.Database,
		x.Host,
		x.Port,
	)
}

func (x *ConnectionDetails) URI() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s/%s?sslmode=disable",
		x.Username,
		x.Password,
		net.JoinHostPort(x.Host, strconv.FormatUint(uint64(x.Port), 10)),
		x.Database,
	)
}

// MySQLDSN returns a MySQL DSN connection string.
func (x *ConnectionDetails) MySQLDSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s)/%s",
		x.Username,
		x.Password,
		net.JoinHostPort(x.Host, strconv.FormatUint(uint64(x.Port), 10)),
		x.Database,
	)
}

// SQLiteDSN returns the database file path for SQLite.
func (x *ConnectionDetails) SQLiteDSN() string {
	return x.Database
}

// LoadFromURL accepts a Postgres connection string and parses it into the ConnectionDetails struct.
func (x *ConnectionDetails) LoadFromURL(u string) error {
	z, err := url.Parse(u)
	if err != nil {
		return err
	}

	port, err := strconv.ParseUint(z.Port(), 10, 64)
	if err != nil {
		return err
	}

	x.Username = z.User.Username()
	x.Password, _ = z.User.Password()
	x.Host = z.Hostname()
	x.Port = uint16(port)
	x.Database = strings.TrimPrefix(z.Path, "/")
	x.DisableSSL = z.Query().Get("sslmode") == "disable"

	return nil
}

// ProvideDatabase creates a database client based on the configured provider
// and optionally runs migrations if RunMigrations is true and a migrator is provided.
// If metricsProvider is non-nil and cfg.EnableDatabaseMetrics is true, the client will emit db.sql.* metrics
// (e.g. db_sql_latency_milliseconds). DB metrics are off by default to avoid high cardinality.
func ProvideDatabase(
	ctx context.Context,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	cfg *Config,
	migrator database.Migrator,
	metricsProvider metrics.Provider,
) (client database.Client, err error) {
	var dbMetricsProvider metrics.Provider
	if cfg.EnableDatabaseMetrics && metricsProvider != nil {
		dbMetricsProvider = metricsProvider
	}

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ProviderPostgres:
		client, err = postgres.ProvideDatabaseClient(ctx, logger, tracerProvider, cfg, dbMetricsProvider)
	case ProviderMySQL:
		client, err = mysql.ProvideDatabaseClient(ctx, logger, tracerProvider, cfg, dbMetricsProvider)
	case ProviderSQLite:
		client, err = sqlite.ProvideDatabaseClient(ctx, logger, tracerProvider, cfg, dbMetricsProvider)
	default:
		return nil, errors.Newf("invalid database provider: %q", cfg.Provider)
	}

	if err != nil {
		return nil, err
	}

	// Run migrations if enabled and migrator is provided
	if cfg.RunMigrations && migrator != nil {
		if err = migrator.Migrate(ctx, client.WriteDB()); err != nil {
			return nil, errors.Wrap(err, "running migrations")
		}
	}

	return client, nil
}
