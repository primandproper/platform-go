package auditcfg

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// sqliteClientConfig is the minimum database.ClientConfig a SQLite file needs.
type sqliteClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*sqliteClientConfig)(nil)

func (c *sqliteClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *sqliteClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *sqliteClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *sqliteClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *sqliteClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *sqliteClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *sqliteClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// recordThrough records one entry carrying a credential field through a
// Recorder built from cfg, and returns the entry as the Recorder rewrote it —
// which is what it wrote.
func recordThrough(t *testing.T, cfg *Config) *audit.Entry {
	t.Helper()

	ctx := t.Context()

	client, err := sqlite.NewDatabaseClient(ctx, &sqliteClientConfig{connectionString: filepath.Join(t.TempDir(), "audit.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, audit.DefaultTablePrefix)
	must.NoError(t, err)
	for _, stmt := range stmts {
		_, err = client.Writer().ExecContext(ctx, stmt)
		must.NoError(t, err)
	}

	recorder, err := NewRecorder(ctx, cfg)
	must.NoError(t, err)

	entry := &audit.Entry{
		EventType:    audit.EventUpdated,
		ResourceType: "user",
		ResourceID:   "user_1",
		Actor:        audit.Actor{ID: "user_1", Type: audit.ActorUser},
		Changes:      map[string]audit.Change{"secret": {New: "s3cret"}},
	}

	must.NoError(t, client.WithTransaction(ctx, func(tx database.Tx) error {
		return recorder.Record(ctx, tx, tenancy.Global(), entry)
	}))

	return entry
}

func TestNewRecorder_credentialRedaction(T *testing.T) {
	T.Parallel()

	T.Run("is installed when the config says nothing", func(t *testing.T) {
		t.Parallel()

		entry := recordThrough(t, validConfig())

		test.MapNotContainsKey(t, entry.Changes, "secret")
	})

	T.Run("is left out when the config disables it", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.CredentialRedactionDisabled = true

		entry := recordThrough(t, cfg)

		test.EqOp(t, "s3cret", entry.Changes["secret"].New)
	})
}
