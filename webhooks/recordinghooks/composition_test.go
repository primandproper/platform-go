package recordinghooks

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	auditmock "github.com/primandproper/platform-go/v14/audit/mock"
	"github.com/primandproper/platform-go/v14/outbox"
	"github.com/primandproper/platform-go/v14/recording"
	"github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/migrations"
	webhooksmock "github.com/primandproper/platform-go/v14/webhooks/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

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

// newSQLiteClient opens a migrated webhooks database of the test's own.
func newSQLiteClient(t *testing.T) database.Client {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "webhooks.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, webhooks.DefaultTablePrefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return client
}

// fanoutLog is the store under the Emitter's dispatcher, recording which
// endpoints each fan-out enqueued for before writing the rows.
type fanoutLog struct {
	webhooks.Store

	enqueued map[webhooks.EventType][][]string
	mu       sync.Mutex
}

func (f *fanoutLog) Enqueue(
	ctx context.Context,
	tx database.Tx,
	delivery *webhooks.Delivery,
	endpointIDs []string,
	now time.Time,
) error {
	f.mu.Lock()
	f.enqueued[delivery.EventType] = append(f.enqueued[delivery.EventType], endpointIDs)
	f.mu.Unlock()

	return f.Store.Enqueue(ctx, tx, delivery, endpointIDs, now)
}

func (f *fanoutLog) recipients(eventType webhooks.EventType) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.enqueued[eventType]
}

// composition is the wiring the package documentation gives: a hookless store
// under the dispatcher the Emitter fans out through, and the hooked store
// every endpoint write goes through.
type composition struct {
	client  database.Client
	store   *webhooks.SQLStore
	fanout  *fanoutLog
	entries []*audit.Entry
	mu      sync.Mutex
}

func newComposition(t *testing.T) *composition {
	t.Helper()

	c := &composition{client: newSQLiteClient(t)}

	hookless, err := webhooks.NewSQLStore(c.client)
	must.NoError(t, err)

	c.fanout = &fanoutLog{Store: hookless, enqueued: map[webhooks.EventType][][]string{}}

	catalog := webhooks.Catalog{}
	maps.Copy(catalog, webhooks.EventCatalog())

	fanoutDispatcher, err := webhooks.NewDispatcher(c.fanout, c.client.Reader(), webhooks.WithCatalog(catalog))
	must.NoError(t, err)

	enqueuer := &webhooksmock.EnqueuerMock{
		EnqueueFunc: func(context.Context, database.Tx, ...outbox.Message) error { return nil },
	}

	emitter, err := webhooks.NewEmitter(enqueuer, fanoutDispatcher, "events")
	must.NoError(t, err)

	auditRecorder := &auditmock.RecorderMock{
		RecordFunc: func(_ context.Context, _ database.Tx, _ tenancy.Scope, entries ...*audit.Entry) error {
			c.mu.Lock()
			defer c.mu.Unlock()

			c.entries = append(c.entries, entries...)

			return nil
		},
	}

	recorder, err := recording.New(auditRecorder, emitter, operatorPrincipal)
	must.NoError(t, err)

	hooks, err := NewRecordingHooks(recorder)
	must.NoError(t, err)

	c.store, err = webhooks.NewSQLStore(c.client, webhooks.WithHooks(hooks))
	must.NoError(t, err)

	return c
}

func (c *composition) save(t *testing.T, id string, events ...webhooks.EventType) {
	t.Helper()

	must.NoError(t, c.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		_, err := c.store.SaveEndpoint(t.Context(), tx, testScope, &webhooks.Endpoint{
			ID:            id,
			URL:           "https://93.184.216.34/" + id,
			ContentType:   webhooks.DefaultContentType,
			Secret:        webhooks.Secret{Current: []byte("signing-key-" + id)},
			Subscriptions: webhooks.SubscribeTo(events...),
		})

		return err
	}))
}

func TestRecordingHooks_Composition(T *testing.T) {
	T.Parallel()

	T.Run("an archived endpoint is not told about its own archival", func(t *testing.T) {
		t.Parallel()

		c := newComposition(t)
		c.save(t, "retiring", webhooks.EventEndpointArchived)
		c.save(t, "watching", webhooks.EventEndpointArchived)

		must.NoError(t, c.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			archived, err := c.store.ArchiveEndpoint(t.Context(), tx, testScope, "retiring")
			if err == nil {
				must.NotNil(t, archived)
			}

			return err
		}))

		test.Eq(t, [][]string{{"watching"}}, c.fanout.recipients(webhooks.EventEndpointArchived))

		c.mu.Lock()
		defer c.mu.Unlock()

		must.SliceNotEmpty(t, c.entries)
		last := c.entries[len(c.entries)-1]
		test.EqOp(t, audit.EventArchived, last.EventType)
		test.EqOp(t, "retiring", last.ResourceID)
	})

	T.Run("an endpoint is told about its own creation", func(t *testing.T) {
		t.Parallel()

		c := newComposition(t)
		c.save(t, "first", webhooks.EventEndpointCreated)
		c.save(t, "second", webhooks.EventEndpointCreated)

		test.Eq(t, [][]string{{"first"}, {"first", "second"}}, sortedEach(c.fanout.recipients(webhooks.EventEndpointCreated)))
	})

	T.Run("a fan-out about an endpoint causes nothing further", func(t *testing.T) {
		t.Parallel()

		c := newComposition(t)
		c.save(t, "everything", webhooks.EventCatalog().EventTypes()...)

		// One registration: one fan-out of the creation, to the endpoint itself,
		// and nothing else. A loop would show up here as a second event type or a
		// second fan-out.
		c.fanout.mu.Lock()
		defer c.fanout.mu.Unlock()

		test.Eq(t, map[webhooks.EventType][][]string{
			webhooks.EventEndpointCreated: {{"everything"}},
		}, c.fanout.enqueued)
	})
}

// sortedEach sorts each fan-out's recipients, which the store answers in an
// order it does not promise.
func sortedEach(fanouts [][]string) [][]string {
	out := make([][]string, 0, len(fanouts))
	for _, ids := range fanouts {
		sorted := append([]string(nil), ids...)
		slices.Sort(sorted)
		out = append(out, sorted)
	}

	return out
}
