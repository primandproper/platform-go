package privacy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/migrations"
	"github.com/primandproper/platform-go/v14/audit/privacy"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a client needs.
type testClientConfig struct {
	connectionString string
	maxOpenConns     int
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return max(c.maxOpenConns, 1) }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// prefixCounter names a fresh pair of audit tables per subtest, so subtests
// sharing one server never read each other's chains.
var prefixCounter atomic.Uint64

// auditEnv is a live audit log at one prefix, with a Recorder and a Reader over
// it.
type auditEnv struct {
	client   database.Client
	recorder audit.Recorder
	reader   audit.Reader
	erasure  *audit.Erasure
}

func newAuditEnv(t *testing.T, client database.Client) *auditEnv {
	t.Helper()

	prefix := fmt.Sprintf("auditprivacy%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(client.Dialect(), prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	recorder, err := audit.NewRecorder(client.Dialect(), audit.WithRecorderTablePrefix(prefix))
	must.NoError(t, err)

	reader, err := audit.NewReader(client.Dialect(), audit.WithReaderTablePrefix(prefix))
	must.NoError(t, err)

	erasure, err := audit.NewErasure(client.Dialect(), audit.WithErasureTablePrefix(prefix))
	must.NoError(t, err)

	return &auditEnv{client: client, recorder: recorder, reader: reader, erasure: erasure}
}

// record appends one entry to a scope and returns the id the Recorder gave it.
func (e *auditEnv) record(t *testing.T, scope tenancy.Scope, actorID, resourceID string) string {
	t.Helper()

	recorded := &audit.Entry{
		EventType:    audit.EventUpdated,
		ResourceType: "article",
		ResourceID:   resourceID,
		Actor:        audit.Actor{ID: actorID, Type: audit.ActorUser, IP: "203.0.113.7"},
	}

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		return e.recorder.Record(t.Context(), tx, scope, recorded)
	}))

	return recorded.ID
}

// collect runs a collector over the log, resolving to the given scopes.
func (e *auditEnv) collect(t *testing.T, scopes ...tenancy.Scope) []audit.Entry {
	t.Helper()

	collector, err := privacy.NewCollector(e.reader, e.client.Reader(), privacy.FixedScopes(scopes...))
	must.NoError(t, err)

	fragment, err := collector.Collect(t.Context(), tenancy.Scope{}, subject)
	must.NoError(t, err)

	if fragment == nil {
		return nil
	}

	var collected []audit.Entry
	must.NoError(t, json.Unmarshal(fragment, &collected))

	return collected
}

// runDialectSuite is what the collector owes on every dialect: the reads it
// makes are the audit Reader's, and what a real server contributes is whether
// the two narrowings, the scope confinement and the paging behave as the
// in-memory mocks assume.
func runDialectSuite(t *testing.T, client database.Client) {
	t.Helper()

	third := tenancy.Of("acct_3")

	t.Run("exports what names the subject, in the scopes resolved", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t, client)

		acted := env.record(t, firstScope, subject.ID, "article_1")
		actedOn := env.record(t, firstScope, "admin_1", subject.ID)
		env.record(t, firstScope, "admin_1", "article_2")
		self := env.record(t, firstScope, subject.ID, subject.ID)
		elsewhere := env.record(t, secondScope, subject.ID, "article_3")
		unresolved := env.record(t, third, subject.ID, "article_4")

		collected := env.collect(t, firstScope, secondScope)

		ids := make([]string, 0, len(collected))
		for i := range collected {
			ids = append(ids, collected[i].ID)
		}

		// Chain order within each scope, scopes in resolver order, the entry the
		// subject acted on themselves once, and nothing from a scope the
		// resolver did not name.
		test.Eq(t, []string{acted, actedOn, self, elsewhere}, ids)
		test.SliceNotContains(t, ids, unresolved)

		must.SliceLen(t, 4, collected)
		test.EqOp(t, "203.0.113.7", collected[0].Actor.IP)
		test.EqOp(t, "admin_1", collected[1].Actor.ID)
		test.EqOp(t, "", collected[1].Actor.IP)
		test.EqOp(t, firstScope, collected[0].Scope)
		test.EqOp(t, secondScope, collected[3].Scope)
		test.NotEq(t, "", collected[0].Hash)
	})

	t.Run("exports every entry an erasure would report retained", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t, client)

		env.record(t, firstScope, subject.ID, "article_1")
		env.record(t, firstScope, "admin_1", subject.ID)
		env.record(t, secondScope, "admin_2", "article_2")
		env.record(t, third, subject.ID, subject.ID)

		// The predicate is audit.Erasure.CountMentions's, so a resolver naming
		// every scope the subject appears in exports exactly the entries that
		// count would put in front of them.
		mentions, err := env.erasure.CountMentions(t.Context(), client.Reader(), subject.ID)
		must.NoError(t, err)

		collected := env.collect(t, firstScope, secondScope, third)
		test.EqOp(t, mentions, int64(len(collected)))
		test.EqOp(t, int64(3), mentions)
	})

	t.Run("pages past one read's worth", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t, client)

		const recorded = 260

		// One transaction, one chain: the Recorder appends every entry it is
		// handed in order, and a page boundary falling mid-scope is the case
		// a collector that stopped after a page would get wrong.
		entries := make([]*audit.Entry, 0, recorded)
		for i := range recorded {
			entries = append(entries, &audit.Entry{
				EventType:    audit.EventUpdated,
				ResourceType: "article",
				ResourceID:   fmt.Sprintf("article_%d", i),
				Actor:        audit.Actor{ID: subject.ID, Type: audit.ActorUser},
			})
		}

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			return env.recorder.Record(t.Context(), tx, firstScope, entries...)
		}))

		collected := env.collect(t, firstScope)
		must.SliceLen(t, recorded, collected)

		for i := range collected {
			test.EqOp(t, int64(i), collected[i].Seq)
		}
	})

	t.Run("a subject in no entry holds nothing", func(t *testing.T) {
		t.Parallel()

		env := newAuditEnv(t, client)
		env.record(t, firstScope, "admin_1", "article_1")

		test.SliceEmpty(t, env.collect(t, firstScope))
	})
}

func TestCollector_SQLite(T *testing.T) {
	T.Parallel()

	client, err := sqlite.NewDatabaseClient(T.Context(),
		&testClientConfig{connectionString: filepath.Join(T.TempDir(), "audit.db")})
	must.NoError(T, err)
	T.Cleanup(func() { _ = client.Close() })

	must.EqOp(T, dialect.SQLite, client.Dialect())

	runDialectSuite(T, client)
}

func TestCollector_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client)
	})
}

func TestCollector_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client)
	})
}
