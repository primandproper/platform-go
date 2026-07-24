package databasecfg

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v6/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var errStubMigrator = errors.New("stub migrator error")

type stubMigrator struct {
	err    error
	called bool
}

func (m *stubMigrator) Migrate(_ context.Context, _ *sql.DB) error {
	m.called = true
	return m.err
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			ReadConnection: ConnectionDetails{
				Host:     "localhost",
				Username: "root",
				Password: "password",
				Port:     1234,
				Database: "test",
			},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConnectionDetails_LoadFromURL(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		exampleURI := "postgres://dbuser:hunter2@pgdatabase:5432/database?sslmode=disable"

		d := &ConnectionDetails{}

		test.NoError(t, d.LoadFromURL(exampleURI))

		test.EqOp(t, d.Username, "dbuser")
		test.EqOp(t, d.Password, "hunter2")
		test.EqOp(t, d.Host, "pgdatabase")
		test.EqOp(t, d.Database, "database")
		test.EqOp(t, d.DisableSSL, true)
	})

	T.Run("with invalid port", func(t *testing.T) {
		t.Parallel()

		exampleURI := "postgres://dbuser:hunter2@pgdatabase:5432_yo_2345/database?sslmode=disable"

		d := &ConnectionDetails{}

		test.Error(t, d.LoadFromURL(exampleURI))
	})

	T.Run("with invalid URL", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{}

		test.Error(t, d.LoadFromURL("://not-a-url"))
	})

	T.Run("with missing port", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{}

		test.Error(t, d.LoadFromURL("postgres://dbuser:hunter2@pgdatabase/database"))
	})

	T.Run("without sslmode disable", func(t *testing.T) {
		t.Parallel()

		exampleURI := "postgres://dbuser:hunter2@pgdatabase:5432/database"

		d := &ConnectionDetails{}
		must.NoError(t, d.LoadFromURL(exampleURI))

		test.False(t, d.DisableSSL)
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("sets all defaults on zero-value config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, ProviderPostgres, cfg.Provider)
		test.EqOp(t, defaultPingWaitPeriod, cfg.PingWaitPeriod)
		test.EqOp(t, defaultConnMaxLifetime, cfg.ConnMaxLifetime)
		test.EqOp(t, uint16(defaultMaxIdleConns), cfg.MaxIdleConns)
		test.EqOp(t, uint16(defaultMaxOpenConns), cfg.MaxOpenConns)
	})

	T.Run("does not override set values", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:        "custom",
			PingWaitPeriod:  5 * time.Second,
			ConnMaxLifetime: 1 * time.Hour,
			MaxIdleConns:    10,
			MaxOpenConns:    20,
		}
		cfg.EnsureDefaults()

		test.EqOp(t, "custom", cfg.Provider)
		test.EqOp(t, 5*time.Second, cfg.PingWaitPeriod)
		test.EqOp(t, 1*time.Hour, cfg.ConnMaxLifetime)
		test.EqOp(t, uint16(10), cfg.MaxIdleConns)
		test.EqOp(t, uint16(20), cfg.MaxOpenConns)
	})
}

func TestConfig_GetReadConnectionString(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ReadConnection: ConnectionDetails{
				Username: "user",
				Password: "pass",
				Database: "db",
				Host:     "localhost",
				Port:     5432,
			},
		}

		expected := "user='user' password='pass' database='db' host='localhost' port=5432 sslmode=prefer"
		test.EqOp(t, expected, cfg.GetReadConnectionString())
	})
}

func TestConfig_GetWriteConnectionString(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			WriteConnection: ConnectionDetails{
				Username: "writer",
				Password: "secret",
				Database: "mydb",
				Host:     "writehost",
				Port:     5433,
			},
		}

		expected := "user='writer' password='secret' database='mydb' host='writehost' port=5433 sslmode=prefer"
		test.EqOp(t, expected, cfg.GetWriteConnectionString())
	})
}

func TestConfig_GetMaxPingAttempts(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{MaxPingAttempts: 42}
		test.EqOp(t, uint64(42), cfg.GetMaxPingAttempts())
	})

	T.Run("returns default when zero", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.EqOp(t, uint64(defaultMaxPingAttempts), cfg.GetMaxPingAttempts())
	})
}

func TestConfig_GetPingWaitPeriod(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{PingWaitPeriod: 3 * time.Second}
		test.EqOp(t, 3*time.Second, cfg.GetPingWaitPeriod())
	})
}

func TestConfig_GetMaxIdleConns(T *testing.T) {
	T.Parallel()

	T.Run("returns default when zero", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.EqOp(t, 5, cfg.GetMaxIdleConns())
	})

	T.Run("returns set value", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{MaxIdleConns: 12}
		test.EqOp(t, 12, cfg.GetMaxIdleConns())
	})
}

func TestConfig_GetMaxOpenConns(T *testing.T) {
	T.Parallel()

	T.Run("returns default when zero", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.EqOp(t, 7, cfg.GetMaxOpenConns())
	})

	T.Run("returns set value", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{MaxOpenConns: 15}
		test.EqOp(t, 15, cfg.GetMaxOpenConns())
	})
}

func TestConfig_GetConnMaxLifetime(T *testing.T) {
	T.Parallel()

	T.Run("returns default when zero", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.EqOp(t, 30*time.Minute, cfg.GetConnMaxLifetime())
	})

	T.Run("returns default when negative", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{ConnMaxLifetime: -1 * time.Second}
		test.EqOp(t, 30*time.Minute, cfg.GetConnMaxLifetime())
	})

	T.Run("returns set value", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{ConnMaxLifetime: 1 * time.Hour}
		test.EqOp(t, 1*time.Hour, cfg.GetConnMaxLifetime())
	})
}

func TestConfig_GetLogQueries(T *testing.T) {
	T.Parallel()

	T.Run("defaults to false", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.EqOp(t, false, cfg.GetLogQueries())
	})

	T.Run("returns set value", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{LogQueries: true}
		test.EqOp(t, true, cfg.GetLogQueries())
	})
}

func TestConfig_ValidateWithContext_additional(T *testing.T) {
	T.Parallel()

	T.Run("valid with all fields", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ReadConnection: ConnectionDetails{
				Host:     "localhost",
				Username: "root",
				Password: "password",
				Port:     5432,
				Database: "test",
			},
			WriteConnection: ConnectionDetails{
				Host:     "localhost",
				Username: "root",
				Password: "password",
				Port:     5432,
				Database: "test",
			},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("sqlite validates with only a database file path", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:       ProviderSQLite,
			ReadConnection: ConnectionDetails{Database: "/tmp/test.db"},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("sqlite requires a database file path", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderSQLite}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an incomplete write connection", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			ReadConnection: ConnectionDetails{
				Host:     "localhost",
				Username: "root",
				Password: "password",
				Port:     5432,
				Database: "test",
			},
			// Missing everything but Host — a supplied write connection is validated.
			WriteConnection: ConnectionDetails{Host: "writehost"},
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestConfig_LoadConnectionDetailsFromURL(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, cfg.LoadConnectionDetailsFromURL("postgres://u:p@h:1234/d"))

		test.EqOp(t, "u", cfg.ReadConnection.Username)
		test.EqOp(t, "p", cfg.ReadConnection.Password)
		test.EqOp(t, "h", cfg.ReadConnection.Host)
		test.EqOp(t, uint16(1234), cfg.ReadConnection.Port)
		test.EqOp(t, "d", cfg.ReadConnection.Database)
	})

	T.Run("with invalid URL", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.Error(t, cfg.LoadConnectionDetailsFromURL("://bad"))
	})
}

func TestConnectionDetails_String(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username: "admin",
			Password: "secret",
			Database: "mydb",
			Host:     "dbhost",
			Port:     5432,
		}

		expected := "user='admin' password='secret' database='mydb' host='dbhost' port=5432 sslmode=prefer"
		test.EqOp(t, expected, d.String())
	})

	T.Run("quotes and escapes values so they cannot inject parameters", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username: "admin",
			Password: "p'w s host=evil",
			Database: "mydb",
			Host:     "dbhost",
			Port:     5432,
		}

		// The password's space, single-quote, and "host=evil" payload must stay
		// inside the quoted value rather than adding a second host= parameter.
		expected := `user='admin' password='p\'w s host=evil' database='mydb' host='dbhost' port=5432 sslmode=prefer`
		test.EqOp(t, expected, d.String())
	})

	T.Run("DisableSSL emits sslmode=disable", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username:   "admin",
			Password:   "secret",
			Database:   "mydb",
			Host:       "dbhost",
			Port:       5432,
			DisableSSL: true,
		}

		test.StrContains(t, d.String(), "sslmode=disable")
	})
}

func TestConnectionDetails_URI(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username: "admin",
			Password: "secret",
			Database: "mydb",
			Host:     "dbhost",
			Port:     5432,
		}

		expected := "postgres://admin:secret@dbhost:5432/mydb?sslmode=prefer"
		test.EqOp(t, expected, d.URI())
	})

	T.Run("DisableSSL emits sslmode=disable", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username:   "admin",
			Password:   "secret",
			Database:   "mydb",
			Host:       "dbhost",
			Port:       5432,
			DisableSSL: true,
		}

		test.EqOp(t, "postgres://admin:secret@dbhost:5432/mydb?sslmode=disable", d.URI())
	})

	T.Run("escapes credentials with special characters", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username: "ad@min",
			Password: "p@ss/word?x",
			Database: "mydb",
			Host:     "dbhost",
			Port:     5432,
		}

		// The URL round-trips: parsing the result must recover the exact password.
		parsed, err := url.Parse(d.URI())
		must.NoError(t, err)
		pw, ok := parsed.User.Password()
		test.True(t, ok)
		test.EqOp(t, "p@ss/word?x", pw)
	})
}

func TestConnectionDetails_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("valid", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username: "user",
			Password: "pass",
			Database: "db",
			Host:     "host",
			Port:     5432,
		}

		test.NoError(t, d.ValidateWithContext(t.Context()))
	})

	T.Run("missing fields", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{}
		test.Error(t, d.ValidateWithContext(t.Context()))
	})
}

func TestConnectionDetails_MySQLDSN(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username: "admin",
			Password: "secret",
			Database: "mydb",
			Host:     "dbhost",
			Port:     3306,
		}

		expected := "admin:secret@tcp(dbhost:3306)/mydb?parseTime=true"
		got := d.MySQLDSN()
		test.EqOp(t, expected, got)
		// parseTime=true is required so DATETIME/TIMESTAMP scan into time.Time.
		test.StrContains(t, got, "parseTime=true")
	})

	T.Run("escapes a password with special characters", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Username: "admin",
			Password: "p@ss/w:rd",
			Database: "mydb",
			Host:     "dbhost",
			Port:     3306,
		}

		// The driver's own DSN parser must recover the exact credentials, proving the
		// '@', '/', and ':' in the password didn't corrupt the DSN.
		parsed, err := mysqldriver.ParseDSN(d.MySQLDSN())
		must.NoError(t, err)
		test.EqOp(t, "admin", parsed.User)
		test.EqOp(t, "p@ss/w:rd", parsed.Passwd)
		test.EqOp(t, "dbhost:3306", parsed.Addr)
		test.EqOp(t, "mydb", parsed.DBName)
		test.True(t, parsed.ParseTime)
	})
}

func TestConnectionDetails_SQLiteDSN(T *testing.T) {
	T.Parallel()

	T.Run("file path", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Database: "/tmp/test.db",
		}

		test.EqOp(t, "/tmp/test.db", d.SQLiteDSN())
	})

	T.Run("memory", func(t *testing.T) {
		t.Parallel()

		d := &ConnectionDetails{
			Database: ":memory:",
		}

		test.EqOp(t, ":memory:", d.SQLiteDSN())
	})
}

func TestConfig_GetReadConnectionString_ProviderAware(T *testing.T) {
	T.Parallel()

	T.Run("postgres provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderPostgres,
			ReadConnection: ConnectionDetails{
				Username: "user",
				Password: "pass",
				Database: "db",
				Host:     "localhost",
				Port:     5432,
			},
		}

		expected := "user='user' password='pass' database='db' host='localhost' port=5432 sslmode=prefer"
		test.EqOp(t, expected, cfg.GetReadConnectionString())
	})

	T.Run("mysql provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderMySQL,
			ReadConnection: ConnectionDetails{
				Username: "user",
				Password: "pass",
				Database: "db",
				Host:     "localhost",
				Port:     3306,
			},
		}

		expected := "user:pass@tcp(localhost:3306)/db?parseTime=true"
		test.EqOp(t, expected, cfg.GetReadConnectionString())
	})

	T.Run("sqlite provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSQLite,
			ReadConnection: ConnectionDetails{
				Database: "/tmp/test.db",
			},
		}

		test.EqOp(t, "/tmp/test.db", cfg.GetReadConnectionString())
	})
}

func TestConfig_GetWriteConnectionString_ProviderAware(T *testing.T) {
	T.Parallel()

	T.Run("mysql provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderMySQL,
			WriteConnection: ConnectionDetails{
				Username: "writer",
				Password: "secret",
				Database: "mydb",
				Host:     "writehost",
				Port:     3306,
			},
		}

		expected := "writer:secret@tcp(writehost:3306)/mydb?parseTime=true"
		test.EqOp(t, expected, cfg.GetWriteConnectionString())
	})

	T.Run("sqlite provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSQLite,
			WriteConnection: ConnectionDetails{
				Database: ":memory:",
			},
		}

		test.EqOp(t, ":memory:", cfg.GetWriteConnectionString())
	})
}

func TestConfig_driverName(T *testing.T) {
	T.Parallel()

	T.Run("postgres default", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderPostgres}
		test.EqOp(t, "pgx", cfg.driverName())
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderMySQL}
		test.EqOp(t, "mysql", cfg.driverName())
	})

	T.Run("sqlite", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderSQLite}
		test.EqOp(t, "sqlite", cfg.driverName())
	})

	T.Run("unknown falls back to pgx", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: "unknown"}
		test.EqOp(t, "pgx", cfg.driverName())
	})
}

func TestConfig_ConnectToReadDatabase(T *testing.T) {
	T.Parallel()

	T.Run("sqlite in-memory", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Provider: ProviderSQLite,
			ReadConnection: ConnectionDetails{
				Database: ":memory:",
			},
		}

		db, err := cfg.ConnectToReadDatabase()
		must.NoError(t, err)
		must.NotNil(t, db)
		must.NoError(t, db.Close())
	})

	T.Run("postgres lazy open", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Provider: ProviderPostgres,
			ReadConnection: ConnectionDetails{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "db",
			},
		}

		db, err := cfg.ConnectToReadDatabase()
		must.NoError(t, err)
		must.NotNil(t, db)
		must.NoError(t, db.Close())
	})

	T.Run("mysql with bogus DSN returns error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Provider: ProviderMySQL,
		}
		db, err := cfg.connectToDatabase("not a valid mysql dsn")
		test.Nil(t, db)
		test.Error(t, err)
	})
}

func TestConfig_ConnectToWriteDatabase(T *testing.T) {
	T.Parallel()

	T.Run("sqlite in-memory", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Provider: ProviderSQLite,
			WriteConnection: ConnectionDetails{
				Database: ":memory:",
			},
		}

		db, err := cfg.ConnectToWriteDatabase()
		must.NoError(t, err)
		must.NotNil(t, db)
		must.NoError(t, db.Close())
	})
}

func TestNewDatabase(T *testing.T) {
	T.Parallel()

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "invalid_provider",
		}

		client, err := NewDatabase(t.Context(), nil, nil, cfg, nil, nil)
		test.Nil(t, client)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "invalid database provider")
	})

	T.Run("postgres lazy open", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderPostgres,
			ReadConnection: ConnectionDetails{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "db",
			},
			WriteConnection: ConnectionDetails{
				Host:     "localhost",
				Port:     5432,
				Username: "user",
				Password: "pass",
				Database: "db",
			},
		}

		client, err := NewDatabase(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, client)
	})

	T.Run("mysql lazy open", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderMySQL,
			ReadConnection: ConnectionDetails{
				Host:     "localhost",
				Port:     3306,
				Username: "user",
				Password: "pass",
				Database: "db",
			},
			WriteConnection: ConnectionDetails{
				Host:     "localhost",
				Port:     3306,
				Username: "user",
				Password: "pass",
				Database: "db",
			},
		}

		client, err := NewDatabase(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, client)
	})

	T.Run("sqlite in-memory", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSQLite,
			ReadConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
			WriteConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
		}

		client, err := NewDatabase(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, client)
	})

	T.Run("sqlite with enable database metrics and nil metrics provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:              ProviderSQLite,
			EnableDatabaseMetrics: true,
			ReadConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
			WriteConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
		}

		client, err := NewDatabase(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, client)
	})

	T.Run("sqlite with enable database metrics and noop metrics provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:              ProviderSQLite,
			EnableDatabaseMetrics: true,
			ReadConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
			WriteConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
		}

		client, err := NewDatabase(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil, metricsnoop.NewMetricsProvider())
		must.NoError(t, err)
		must.NotNil(t, client)
	})

	T.Run("sqlite with migrations", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:      ProviderSQLite,
			RunMigrations: true,
			ReadConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
			WriteConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
		}

		migrator := &stubMigrator{}
		client, err := NewDatabase(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, migrator, nil)
		must.NoError(t, err)
		must.NotNil(t, client)
		test.True(t, migrator.called)
	})

	T.Run("sqlite with bad path returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSQLite,
			ReadConnection: ConnectionDetails{
				Database: "/nonexistent/directory/that/cannot/exist/foo.db",
			},
			WriteConnection: ConnectionDetails{
				Database: "/nonexistent/directory/that/cannot/exist/foo.db",
			},
		}

		client, err := NewDatabase(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil, nil)
		test.Nil(t, client)
		test.Error(t, err)
	})

	T.Run("sqlite with migrations error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:      ProviderSQLite,
			RunMigrations: true,
			ReadConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
			WriteConnection: ConnectionDetails{
				Database: filepath.Join(t.TempDir(), "test.db"),
			},
		}

		migrator := &stubMigrator{err: errStubMigrator}
		client, err := NewDatabase(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, migrator, nil)
		test.Nil(t, client)
		test.Error(t, err)
		test.True(t, migrator.called)
	})
}
