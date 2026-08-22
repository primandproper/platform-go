// Package databasecfg selects and builds a database.Client — Postgres, MySQL, or
// SQLite — and owns the connection strings each of them wants.
//
// That second job is why this package is larger than the other selection seams.
// ConnectionDetails is parsed from discrete fields or from a URL and rendered as
// a libpq keyword string, a MySQL DSN, or a SQLite DSN, so the quoting and the
// SSL-mode handling live in one place rather than in each caller that assembles
// a connection string by hand.
//
// Postgres is the default provider, applied by EnsureDefaults before validation
// runs, so an unset provider is a configured deployment rather than a validation
// failure — and so the provider list this package validates against never has to
// carry the empty string.
package databasecfg

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/mysql"
	"github.com/primandproper/platform-go/v13/database/postgres"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	mysqldriver "github.com/go-sql-driver/mysql"
)

const (
	ProviderPostgres = "postgres"
	ProviderMySQL    = "mysql"
	ProviderSQLite   = "sqlite"
)

// providers are every provider this package implements. Validation and
// NewDatabase both read it. The empty string is absent because EnsureDefaults
// has already turned it into ProviderPostgres by the time either looks.
var providers = []string{ProviderPostgres, ProviderMySQL, ProviderSQLite}

type (
	// Config represents our database configuration.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		Provider              string            `env:"PROVIDER"                envDefault:"postgres"                  json:"provider,omitempty"              yaml:"provider,omitempty"`
		ReadConnection        ConnectionDetails `envPrefix:"READ_CONNECTION_"  json:"readConnection,omitzero"         yaml:"readConnection,omitempty"`
		WriteConnection       ConnectionDetails `envPrefix:"WRITE_CONNECTION_" json:"writeConnection,omitzero"        yaml:"writeConnection,omitempty"`
		PingWaitPeriod        time.Duration     `env:"PING_WAIT_PERIOD"        envDefault:"1s"                        json:"pingWaitPeriod,omitempty"        yaml:"pingWaitPeriod,omitempty"`
		MaxPingAttempts       uint64            `env:"MAX_PING_ATTEMPTS"       json:"maxPingAttempts,omitempty"       yaml:"maxPingAttempts,omitempty"`
		ConnMaxLifetime       time.Duration     `env:"CONN_MAX_LIFETIME"       envDefault:"30m"                       json:"connMaxLifetime,omitempty"       yaml:"connMaxLifetime,omitempty"`
		MaxIdleConns          uint16            `env:"MAX_IDLE_CONNS"          envDefault:"5"                         json:"maxIdleConns,omitempty"          yaml:"maxIdleConns,omitempty"`
		MaxOpenConns          uint16            `env:"MAX_OPEN_CONNS"          envDefault:"7"                         json:"maxOpenConns,omitempty"          yaml:"maxOpenConns,omitempty"`
		Debug                 bool              `env:"DEBUG"                   json:"debug,omitempty"                 yaml:"debug,omitempty"`
		LogQueries            bool              `env:"LOG_QUERIES"             json:"logQueries,omitempty"            yaml:"logQueries,omitempty"`
		RunMigrations         bool              `env:"RUN_MIGRATIONS"          json:"runMigrations,omitempty"         yaml:"runMigrations,omitempty"`
		EnableDatabaseMetrics bool              `env:"ENABLE_DATABASE_METRICS" json:"enableDatabaseMetrics,omitempty" yaml:"enableDatabaseMetrics,omitempty"`
	}

	ConnectionDetails struct {
		_ struct{} `json:"-" yaml:"-"`

		Username   string `env:"USERNAME"    json:"username,omitempty"   yaml:"username,omitempty"`
		Password   string `env:"PASSWORD"    json:"password,omitempty"   yaml:"password,omitempty"`
		Database   string `env:"DATABASE"    json:"database,omitempty"   yaml:"database,omitempty"`
		Host       string `env:"HOST"        json:"hostname,omitempty"   yaml:"hostname,omitempty"`
		Port       uint16 `env:"PORT"        json:"port,omitempty"       yaml:"port,omitempty"`
		DisableSSL bool   `env:"DISABLE_SSL" json:"disableSSL,omitempty" yaml:"disableSSL,omitempty"`
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
	switch cfgnorm.Provider(cfg.Provider) {
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
//
// The provider is checked normalized, matching dispatch, and against the same
// list NewDatabase reads — an unrecognized one used to reach the connection
// rules, pass them, and be refused only once a client was being built.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	// An unset provider reads as postgres, which is what EnsureDefaults and the
	// envDefault tag both make it: a parent validating a sub-config it did not
	// default should not be told the library's own default is unknown.
	provider := cfgnorm.Provider(cfg.Provider)
	if provider == "" {
		provider = ProviderPostgres
	}

	if !slices.Contains(providers, provider) {
		return errors.Wrapf(errors.ErrUnknownProvider, "database provider %q", cfg.Provider)
	}

	if provider == ProviderSQLite {
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
	cfg *Config,
	migrator database.Migrator,
	opts ...Option,
) (client database.Client, err error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	// Defaults first: a hand-built Config leaves Provider empty, and postgres is
	// the documented default rather than a validation failure. Only deployments
	// that parse the environment got that for free, from the envDefault tag.
	cfg.EnsureDefaults()

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "database provider")
	if err != nil {
		return nil, err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating database config")
	}

	var dbMetricsProvider metrics.Provider
	if cfg.EnableDatabaseMetrics && metricsProvider != nil {
		dbMetricsProvider = metricsProvider
	}

	switch provider {
	case ProviderPostgres:
		client, err = postgres.NewDatabaseClient(ctx, cfg,
			postgres.WithLogger(logger),
			postgres.WithTracerProvider(tracerProvider),
			postgres.WithMetricsProvider(dbMetricsProvider))
	case ProviderMySQL:
		client, err = mysql.NewDatabaseClient(ctx, cfg,
			mysql.WithLogger(logger),
			mysql.WithTracerProvider(tracerProvider),
			mysql.WithMetricsProvider(dbMetricsProvider))
	case ProviderSQLite:
		client, err = sqlite.NewDatabaseClient(ctx, cfg,
			sqlite.WithLogger(logger),
			sqlite.WithTracerProvider(tracerProvider),
			sqlite.WithMetricsProvider(dbMetricsProvider))
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "database provider %q", cfg.Provider)
	}

	if err != nil {
		return nil, err
	}

	// Run migrations if enabled and migrator is provided. Migrations need the concrete
	// *sql.DB, which lives behind the RawAccess capability rather than the safe Client
	// surface.
	if cfg.RunMigrations && migrator != nil {
		raw, ok := client.(database.RawAccess)
		if !ok {
			return nil, errors.New("configured database client does not expose raw access required for migrations")
		}
		if err = migrator.Migrate(ctx, raw.WriteDB()); err != nil {
			return nil, errors.Wrap(err, "running migrations")
		}
	}

	return client, nil
}
