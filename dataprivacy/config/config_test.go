package dataprivacycfg

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/dataprivacy/auditerasure"
	"github.com/primandproper/platform-go/v14/dataprivacy/migrations"
	"github.com/primandproper/platform-go/v14/operations"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"

	"github.com/primandproper/primitives-go/compression"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	"github.com/primandproper/primitives-go/tenancy"
	"github.com/primandproper/primitives-go/uploads/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig(T *testing.T) {
	T.Parallel()

	T.Run("fills defaults", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Dialect: dialect.Postgres}
		cfg.EnsureDefaults()

		test.EqOp(t, dataprivacy.DefaultTablePrefix, cfg.TablePrefix)
		test.EqOp(t, audit.DefaultTablePrefix, cfg.AuditErasure.TablePrefix)
		test.EqOp(t, auditerasure.DefaultRetentionBasis, cfg.AuditErasure.RetentionBasis)

		// The audit eraser is registered unless an operator turns it off: an
		// erasure that silently skipped a store of personal data would be the
		// more surprising default.
		test.False(t, cfg.AuditErasure.Disabled)

		// Encryption is off unless configured, because an artifact encrypted at
		// rest is only as recoverable as its key.
		test.False(t, cfg.Packaging.Encrypted)

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("requires a valid dialect", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Dialect: dialect.Dialect("oracle")}
		cfg.EnsureDefaults()

		// ozzo collects field errors into a map, which does not forward
		// errors.Is to the causes underneath — so this asserts on the rendering.
		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "unsupported SQL dialect")
	})

	T.Run("validates the nested configs", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Dialect: dialect.Postgres}
		cfg.EnsureDefaults()

		// ozzo dereferences a struct-value field before checking
		// ValidatableWithContext, so these are reached through By closures — a
		// regression here would silently stop validating them.
		cfg.Fulfiller.CollectorTimeout = cfg.Fulfiller.FulfillmentTimeout

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "must exceed collector timeout")
	})
}

func TestRegisterAuditEraser(T *testing.T) {
	T.Parallel()

	T.Run("registers by default", func(t *testing.T) {
		t.Parallel()

		registry := dataprivacy.NewRegistry()

		registered, err := RegisterAuditEraser(t.Context(), &Config{Dialect: dialect.SQLite}, registry)
		must.NoError(t, err)

		test.True(t, registered)
		test.Eq(t, []string{auditerasure.DefaultKey}, registry.EraserKeys())
	})

	T.Run("Disabled leaves the audit log untouched", func(t *testing.T) {
		t.Parallel()

		registry := dataprivacy.NewRegistry()

		cfg := &Config{Dialect: dialect.SQLite}
		cfg.AuditErasure.Disabled = true

		registered, err := RegisterAuditEraser(t.Context(), cfg, registry)
		must.NoError(t, err)

		test.False(t, registered)
		test.SliceEmpty(t, registry.EraserKeys())
	})

	T.Run("refuses a nil registry", func(t *testing.T) {
		t.Parallel()

		_, err := RegisterAuditEraser(t.Context(), &Config{Dialect: dialect.SQLite}, nil)
		test.Error(t, err)
	})

	T.Run("passes an eraser option through, after the config-derived ones", func(t *testing.T) {
		t.Parallel()

		// The prefix arrives from configuration and is then overridden by the
		// pass-through, which is what "applied after" means. An invalid one is
		// the observable end of that: it can only have been refused if the
		// option reached auditerasure.New at all.
		_, err := RegisterAuditEraser(t.Context(), &Config{Dialect: dialect.SQLite}, dataprivacy.NewRegistry(),
			WithAuditEraserOptions(auditerasure.WithTablePrefix("drop table;--")))
		test.ErrorIs(t, err, auditerasure.ErrInvalidTablePrefix)
	})

	T.Run("takes the same option slice as every other constructor", func(t *testing.T) {
		t.Parallel()

		registry := dataprivacy.NewRegistry()

		// The point of the variadic being this package's Option: one wiring
		// site's options go to whichever of these functions it calls, and the
		// ones this function has no use for are ignored rather than refused.
		registered, err := RegisterAuditEraser(t.Context(), &Config{Dialect: dialect.SQLite}, registry,
			WithLogger(loggingnoop.NewLogger()),
			WithStoreOptions(nil),
			nil,
		)
		must.NoError(t, err)

		test.True(t, registered)
		test.Eq(t, []string{auditerasure.DefaultKey}, registry.EraserKeys())
	})

	T.Run("propagates a bad audit table prefix", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Dialect: dialect.SQLite}
		cfg.AuditErasure.TablePrefix = "drop table;--"

		_, err := RegisterAuditEraser(t.Context(), cfg, dataprivacy.NewRegistry())
		test.ErrorIs(t, err, auditerasure.ErrInvalidTablePrefix)
	})
}

func TestEnsurePackaging(T *testing.T) {
	T.Parallel()

	T.Run("supplies nothing when nothing is configured", func(t *testing.T) {
		t.Parallel()

		workerOpts, serviceOpts, err := EnsurePackaging(&Config{Dialect: dialect.SQLite}, nil, nil)
		must.NoError(t, err)

		test.SliceEmpty(t, workerOpts)
		test.SliceEmpty(t, serviceOpts)
	})
}

func TestEnsurePackaging_Declaration(T *testing.T) {
	T.Parallel()

	T.Run("refuses an encryptor a config did not declare", func(t *testing.T) {
		t.Parallel()

		encryptorDecryptor, err := newTestEncryptorDecryptor([]byte("0123456789abcdef0123456789abcdef"))
		must.NoError(t, err)

		// The expensive direction: undeclared encryption mails the subject a
		// link to ciphertext, and the failure surfaces at the subject weeks
		// later rather than at startup.
		fulfillerOpts, serviceOpts, err := EnsurePackaging(&Config{Dialect: dialect.SQLite}, nil, encryptorDecryptor)
		test.ErrorIs(t, err, ErrPackagingDeclarationMismatch)

		test.SliceEmpty(t, fulfillerOpts)
		test.SliceEmpty(t, serviceOpts)
	})

	T.Run("refuses a declaration no encryptor backs", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Dialect: dialect.SQLite}
		cfg.Packaging.Encrypted = true

		// The quiet direction: a deployment that declared encryption and wired
		// none withholds every download link forever, and nothing says so.
		_, _, err := EnsurePackaging(cfg, nil, nil)
		test.ErrorIs(t, err, ErrPackagingDeclarationMismatch)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		_, _, err := EnsurePackaging(nil, nil, nil)
		test.Error(t, err)
	})
}

func TestConstructors(T *testing.T) {
	T.Parallel()

	T.Run("refuse a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewStore(t.Context(), nil, nil)
		test.Error(t, err)

		_, err = NewService(t.Context(), nil, nil, nil, nil)
		test.Error(t, err)

		_, err = NewFulfiller(t.Context(), nil, nil, nil, nil, nil, nil)
		test.Error(t, err)

		_, err = NewSweeper(t.Context(), nil, nil, nil)
		test.Error(t, err)
	})

	T.Run("assemble every part from one config", func(t *testing.T) {
		t.Parallel()

		env := newConfigEnv(t)
		cfg := &Config{Dialect: dialect.SQLite, TablePrefix: env.prefix}

		store, err := NewStore(
			t.Context(),
			cfg,
			env.client,
		)
		must.NoError(t, err)
		must.NotNil(t, store)

		svc, err := NewService(
			t.Context(),
			cfg,
			env.client,
			store,
			stubOperations(),
		)
		must.NoError(t, err)
		must.NotNil(t, svc)

		domains := dataprivacy.NewRegistry()
		must.NoError(t, domains.RegisterEraser("identity", dataprivacy.EraserFunc(
			func(context.Context, database.Tx, tenancy.Scope, dataprivacy.Subject) (dataprivacy.ErasureOutcome, error) {
				return dataprivacy.ErasureOutcome{}, nil
			},
		)))

		kinds := operations.NewRegistry()

		fulfiller, err := NewFulfiller(
			t.Context(),
			cfg,
			env.client,
			store,
			domains,
			kinds,
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, fulfiller)

		// Registering as it builds is what keeps the two halves of the wiring
		// from drifting: a Fulfiller nobody registered is a set of runners
		// nothing calls.
		test.Eq(t, []string{dataprivacy.KindErasure}, kinds.Kinds())

		sweeper, err := NewSweeper(
			t.Context(),
			cfg,
			store,
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, sweeper)

		// The whole point of one Config: the prefix the Service writes to is by
		// construction the one the Fulfiller reads from.
		req, err := svc.Submit(t.Context(), tenancy.Of("account-1"), dataprivacy.Subject{ID: "user-1"}, dataprivacy.RequestErasure)
		must.NoError(t, err)

		read, err := svc.Get(t.Context(), nil, req.ID)
		must.NoError(t, err)
		test.EqOp(t, req.ID, read.ID)
	})

	T.Run("propagate a bad dialect", func(t *testing.T) {
		t.Parallel()

		env := newConfigEnv(t)
		cfg := &Config{Dialect: dialect.Dialect("oracle"), TablePrefix: env.prefix}

		_, err := NewStore(t.Context(), cfg, env.client)
		test.Error(t, err)

		_, err = NewService(t.Context(), cfg, env.client, nil, stubOperations())
		test.Error(t, err)

		_, err = NewFulfiller(t.Context(), cfg, env.client, nil, dataprivacy.NewRegistry(),
			operations.NewRegistry(), nil)
		test.Error(t, err)

		_, err = NewSweeper(t.Context(), cfg, nil, nil)
		test.Error(t, err)
	})

	T.Run("a fulfiller with an uploader gets a URL signer", func(t *testing.T) {
		t.Parallel()

		env := newConfigEnv(t)
		cfg := &Config{Dialect: dialect.SQLite, TablePrefix: env.prefix}

		store, err := NewStore(
			t.Context(),
			cfg,
			env.client,
		)
		must.NoError(t, err)

		domains := dataprivacy.NewRegistry()
		must.NoError(t, domains.RegisterCollector("identity", dataprivacy.CollectorFunc(
			func(context.Context, tenancy.Scope, dataprivacy.Subject) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		)))

		// Supplying the uploader is what satisfies the export runner's storage
		// requirement and wires the signer in one step.
		fulfiller, err := NewFulfiller(t.Context(), cfg, env.client, store, domains, operations.NewRegistry(),
			noop.NewUploadManager())
		must.NoError(t, err)
		test.NotNil(t, fulfiller)
	})

	T.Run("the encryption declaration comes from the config rather than the caller", func(t *testing.T) {
		t.Parallel()

		env := newConfigEnv(t)

		cfg := &Config{Dialect: dialect.SQLite, TablePrefix: env.prefix}
		cfg.Packaging.Encrypted = true

		store, err := NewStore(t.Context(), cfg, env.client)
		must.NoError(t, err)

		domains := dataprivacy.NewRegistry()
		must.NoError(t, domains.RegisterCollector("identity", dataprivacy.CollectorFunc(
			func(context.Context, tenancy.Scope, dataprivacy.Subject) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		)))

		// What the declaration decides is whether the signer this builds ever
		// returns a URL; that it declines under encryption is
		// dataprivacy.NewArtifactURLSigner's own test. What is asserted here is
		// that the fact now travels in the Config, so there is no longer a
		// positional bool for a call site to get wrong.
		fulfiller, err := NewFulfiller(t.Context(), cfg, env.client, store, domains, operations.NewRegistry(),
			noop.NewUploadManager())
		must.NoError(t, err)
		test.NotNil(t, fulfiller)
	})
}

// configEnv is a SQLite database with a uniquely prefixed request table.
type configEnv struct {
	client database.Client
	prefix string
}

var configPrefixCounter atomic.Uint64

func newConfigEnv(t *testing.T) *configEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "dataprivacy.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	prefix := fmt.Sprintf("cfg_%d", configPrefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return &configEnv{client: client, prefix: prefix}
}

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

func TestRegisterAuditEraser_Failures(T *testing.T) {
	T.Parallel()

	T.Run("propagates a config that will not validate", func(t *testing.T) {
		t.Parallel()

		_, err := RegisterAuditEraser(t.Context(), &Config{Dialect: dialect.Dialect("oracle")},
			dataprivacy.NewRegistry())
		test.Error(t, err)
	})

	T.Run("propagates a registry that already holds the key", func(t *testing.T) {
		t.Parallel()

		registry := dataprivacy.NewRegistry()
		must.NoError(t, registry.RegisterEraser(auditerasure.DefaultKey, dataprivacy.EraserFunc(
			func(context.Context, database.Tx, tenancy.Scope, dataprivacy.Subject) (dataprivacy.ErasureOutcome, error) {
				return dataprivacy.ErasureOutcome{}, nil
			},
		)))

		// An application that registered its own audit eraser and also left the
		// built-in one enabled is told, rather than having one silently win.
		_, err := RegisterAuditEraser(t.Context(), &Config{Dialect: dialect.SQLite}, registry)
		test.ErrorIs(t, err, dataprivacy.ErrDuplicateKey)
	})
}

func TestEnsurePackaging_Supplied(T *testing.T) {
	T.Parallel()

	T.Run("pairs the worker and service codecs", func(t *testing.T) {
		t.Parallel()

		compressor, err := compression.NewCompressor(compression.AlgorithmZstd)
		must.NoError(t, err)

		encryptorDecryptor, err := newTestEncryptorDecryptor([]byte("0123456789abcdef0123456789abcdef"))
		must.NoError(t, err)

		cfg := &Config{Dialect: dialect.SQLite}
		cfg.Packaging.Encrypted = true

		// The pairing is the point: an artifact written with one compressor and
		// read with another is unreadable, and the failure would surface at the
		// subject rather than at startup.
		workerOpts, serviceOpts, err := EnsurePackaging(cfg, compressor, encryptorDecryptor)
		must.NoError(t, err)

		test.SliceLen(t, 2, workerOpts)
		test.SliceLen(t, 2, serviceOpts)
	})

	T.Run("a compressor alone pairs one option each", func(t *testing.T) {
		t.Parallel()

		compressor, err := compression.NewCompressor(compression.AlgorithmS2)
		must.NoError(t, err)

		workerOpts, serviceOpts, err := EnsurePackaging(&Config{Dialect: dialect.SQLite}, compressor, nil)
		must.NoError(t, err)

		test.SliceLen(t, 1, workerOpts)
		test.SliceLen(t, 1, serviceOpts)
	})
}

// stubOperations is an operations.Service that records nothing and runs nothing.
//
// A real one cannot be built here: operations is Postgres-only, and this suite
// is SQLite. What these tests are about is that one Config assembles four parts
// against one table, and the operations service is a dependency of that
// assembly rather than a subject of it — the end-to-end run through a real
// worker is in dataprivacy's container tests.
func stubOperations() operations.Service {
	return &operationsmock.ServiceMock{
		StartInTransactionFunc: func(
			_ context.Context,
			_ database.Tx,
			kind string,
			_ any,
			_ ...operations.StartOption,
		) (*operations.Operation, error) {
			return &operations.Operation{ID: "op-1", Kind: kind, State: operations.StatePending}, nil
		},
		EnqueueFunc: func(context.Context, string, ...operations.StartOption) error { return nil },
	}
}
