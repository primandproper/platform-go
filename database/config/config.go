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

	encryptioncfg "github.com/primandproper/platform-go/v4/cryptography/encryption/config"
	"github.com/primandproper/platform-go/v4/database"
	"github.com/primandproper/platform-go/v4/database/mysql"
	"github.com/primandproper/platform-go/v4/database/postgres"
	"github.com/primandproper/platform-go/v4/database/sqlite"
	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/XSAM/otelsql"
	validation "github.com/go-ozzo/ozzo-validation/v4"
	mysqldriver "github.com/go-sql-driver/mysql"
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
		_ struct{} `json:"-" yaml:"-"`

		Encryption               encryptioncfg.Config `env:"init"                        envPrefix:"ENCRYPTION_"         json:"encryption"               yaml:"encryption"`
		OAuth2TokenEncryptionKey string               `env:"OAUTH2_TOKEN_ENCRYPTION_KEY" json:"oauth2TokenEncryptionKey" yaml:"oauth2TokenEncryptionKey"`
		Provider                 string               `env:"PROVIDER"                    envDefault:"postgres"           json:"provider"                 yaml:"provider"`
		ReadConnection           ConnectionDetails    `envPrefix:"READ_CONNECTION_"      json:"readConnection"           yaml:"readConnection"`
		WriteConnection          ConnectionDetails    `envPrefix:"WRITE_CONNECTION_"     json:"writeConnection"          yaml:"writeConnection"`
		PingWaitPeriod           time.Duration        `env:"PING_WAIT_PERIOD"            envDefault:"1s"                 json:"pingWaitPeriod"           yaml:"pingWaitPeriod"`
		MaxPingAttempts          uint64               `env:"MAX_PING_ATTEMPTS"           json:"maxPingAttempts"          yaml:"maxPingAttempts"`
		ConnMaxLifetime          time.Duration        `env:"CONN_MAX_LIFETIME"           envDefault:"30m"                json:"connMaxLifetime"          yaml:"connMaxLifetime"`
		MaxIdleConns             uint16               `env:"MAX_IDLE_CONNS"              envDefault:"5"                  json:"maxIdleConns"             yaml:"maxIdleConns"`
		MaxOpenConns             uint16               `env:"MAX_OPEN_CONNS"              envDefault:"7"                  json:"maxOpenConns"             yaml:"maxOpenConns"`
		Debug                    bool                 `env:"DEBUG"                       json:"debug"                    yaml:"debug"`
		LogQueries               bool                 `env:"LOG_QUERIES"                 json:"logQueries"               yaml:"logQueries"`
		RunMigrations            bool                 `env:"RUN_MIGRATIONS"              json:"runMigrations"            yaml:"runMigrations"`
		EnableDatabaseMetrics    bool                 `env:"ENABLE_DATABASE_METRICS"     json:"enableDatabaseMetrics"    yaml:"enableDatabaseMetrics"`
	}

	ConnectionDetails struct {
		_ struct{} `json:"-" yaml:"-"`

		Username   string `env:"USERNAME"    json:"username"   yaml:"username"`
		Password   string `env:"PASSWORD"    json:"password"   yaml:"password"`
		Database   string `env:"DATABASE"    json:"database"   yaml:"database"`
		Host       string `env:"HOST"        json:"hostname"   yaml:"hostname"`
		Port       uint16 `env:"PORT"        json:"port"       yaml:"port"`
		DisableSSL bool   `env:"DISABLE_SSL" json:"disableSSL" yaml:"disableSSL"`
	}
)

const (
	defaultPingWaitPeriod  = 1 * time.Second
	defaultConnMaxLifetime = 30 * time.Minute
	defaultMaxIdleConns    = 5
	defaultMaxOpenConns    = 7
	defaultMaxPingAttempts = 50
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

	if cfg.MaxPingAttempts == 0 {
		cfg.MaxPingAttempts = defaultMaxPingAttempts
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
// Returns 50 when unset (zero) so IsReady retries rather than making a single attempt.
func (cfg *Config) GetMaxPingAttempts() uint64 {
	if cfg.MaxPingAttempts == 0 {
		return defaultMaxPingAttempts
	}
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

// GetLogQueries reports whether SQL query text should be recorded on database
// spans. The database client providers consume this via an optional interface
// assertion; when false (the default), otelsql is configured to suppress the
// db.statement attribute so raw SQL is not emitted into traces.
func (cfg *Config) GetLogQueries() bool {
	return cfg.LogQueries
}

// ValidateWithContext validates a Config. Connection requirements are
// provider-aware: SQLite only needs a database file path (on either the read or
// write connection), while Postgres and MySQL require a fully specified read
// connection. A write connection, when supplied, is validated regardless of
// provider.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if strings.TrimSpace(strings.ToLower(cfg.Provider)) == ProviderSQLite {
		if cfg.ReadConnection.Database == "" && cfg.WriteConnection.Database == "" {
			return errors.New("sqlite requires a database file path on the read or write connection")
		}
		return nil
	}

	if err := cfg.ReadConnection.ValidateWithContext(ctx); err != nil {
		return errors.Wrap(err, "validating read connection")
	}

	if cfg.WriteConnection != (ConnectionDetails{}) {
		if err := cfg.WriteConnection.ValidateWithContext(ctx); err != nil {
			return errors.Wrap(err, "validating write connection")
		}
	}

	return nil
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

// sslMode maps DisableSSL onto a libpq sslmode value. When SSL is not explicitly
// disabled we emit "prefer", which is pgx's own default (encrypt if the server
// offers it, otherwise fall back), so this is a no-op for existing deployments
// while making DisableSSL actually take effect.
func (x *ConnectionDetails) sslMode() string {
	if x.DisableSSL {
		return "disable"
	}
	return "prefer"
}

// quotePGConnValue single-quotes a libpq keyword/value connection-string value,
// backslash-escaping embedded backslashes and single quotes so a value containing
// a space, quote, or "key=value"-looking payload cannot corrupt or inject
// additional connection parameters.
func quotePGConnValue(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

func (x *ConnectionDetails) String() string {
	return strings.Join([]string{
		"user=" + quotePGConnValue(x.Username),
		"password=" + quotePGConnValue(x.Password),
		"database=" + quotePGConnValue(x.Database),
		"host=" + quotePGConnValue(x.Host),
		fmt.Sprintf("port=%d", x.Port),
		"sslmode=" + x.sslMode(),
	}, " ")
}

func (x *ConnectionDetails) URI() string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(x.Username, x.Password),
		Host:     net.JoinHostPort(x.Host, strconv.FormatUint(uint64(x.Port), 10)),
		Path:     "/" + x.Database,
		RawQuery: url.Values{"sslmode": {x.sslMode()}}.Encode(),
	}
	return u.String()
}

// MySQLDSN returns a MySQL DSN connection string. parseTime=true is required so the
// driver scans DATETIME/TIMESTAMP columns into time.Time rather than []byte, which
// the null-value helpers (e.g. TimeFromNullTime) depend on. The driver defaults loc
// to UTC, so times come back in UTC. The DSN is assembled via the driver's own
// Config so credentials/host values are escaped rather than concatenated.
func (x *ConnectionDetails) MySQLDSN() string {
	cfg := mysqldriver.NewConfig()
	cfg.User = x.Username
	cfg.Passwd = x.Password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(x.Host, strconv.FormatUint(uint64(x.Port), 10))
	cfg.DBName = x.Database
	cfg.ParseTime = true

	return cfg.FormatDSN()
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

// NewDatabase creates a database client based on the configured provider
// and optionally runs migrations if RunMigrations is true and a migrator is provided.
// If metricsProvider is non-nil and cfg.EnableDatabaseMetrics is true, the client will emit db.sql.* metrics
// (e.g. db_sql_latency_milliseconds). DB metrics are off by default to avoid high cardinality.
func NewDatabase(
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
		client, err = postgres.NewDatabaseClient(ctx, logger, tracerProvider, cfg, dbMetricsProvider)
	case ProviderMySQL:
		client, err = mysql.NewDatabaseClient(ctx, logger, tracerProvider, cfg, dbMetricsProvider)
	case ProviderSQLite:
		client, err = sqlite.NewDatabaseClient(ctx, logger, tracerProvider, cfg, dbMetricsProvider)
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
