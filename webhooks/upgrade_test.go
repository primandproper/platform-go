package webhooks

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/webhooks/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// legacySchema is webhooks_endpoints and webhooks_subscriptions as this package
// shipped them before a subscription was a row: no id, no timestamps on the
// mapping table, and no name or created_by on the endpoint.
//
// It is written out rather than derived from the current DDL because that is the
// point — the upgrade has to move a schema nothing in this repository creates any
// more, and a fixture generated from today's files would drift into agreeing with
// them.
var legacySchema = map[dialect.Dialect]string{
	dialect.Postgres: `
CREATE TABLE {{PREFIX}}webhooks_endpoints (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    url             TEXT NOT NULL,
    content_type    TEXT NOT NULL,
    secret_current  BYTEA NOT NULL,
    secret_previous BYTEA,
    headers         BYTEA NOT NULL,
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);
CREATE TABLE {{PREFIX}}webhooks_subscriptions (
    endpoint_id TEXT NOT NULL REFERENCES {{PREFIX}}webhooks_endpoints (id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    PRIMARY KEY (endpoint_id, event_type)
);
CREATE INDEX {{PREFIX}}webhooks_subscriptions_event_idx
    ON {{PREFIX}}webhooks_subscriptions (event_type, endpoint_id);
`,
	dialect.MySQL: `
CREATE TABLE {{PREFIX}}webhooks_endpoints (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    url             TEXT NOT NULL,
    content_type    VARCHAR(255) NOT NULL,
    secret_current  VARBINARY(512) NOT NULL,
    secret_previous VARBINARY(512),
    headers         BLOB NOT NULL,
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6)
);
CREATE TABLE {{PREFIX}}webhooks_subscriptions (
    endpoint_id VARCHAR(64) NOT NULL,
    event_type  VARCHAR(255) NOT NULL,
    PRIMARY KEY (endpoint_id, event_type),
    CONSTRAINT {{PREFIX}}webhooks_subscriptions_endpoint_fk
        FOREIGN KEY (endpoint_id) REFERENCES {{PREFIX}}webhooks_endpoints (id) ON DELETE CASCADE
);
CREATE INDEX {{PREFIX}}webhooks_subscriptions_event_idx
    ON {{PREFIX}}webhooks_subscriptions (event_type, endpoint_id);
`,
	dialect.SQLite: `
CREATE TABLE {{PREFIX}}webhooks_endpoints (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    url             TEXT NOT NULL,
    content_type    TEXT NOT NULL,
    secret_current  BLOB NOT NULL,
    secret_previous BLOB,
    headers         BLOB NOT NULL,
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);
CREATE TABLE {{PREFIX}}webhooks_subscriptions (
    endpoint_id TEXT NOT NULL REFERENCES {{PREFIX}}webhooks_endpoints (id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    PRIMARY KEY (endpoint_id, event_type)
);
CREATE INDEX {{PREFIX}}webhooks_subscriptions_event_idx
    ON {{PREFIX}}webhooks_subscriptions (event_type, endpoint_id);
`,
}

// legacyEndpointCreatedAt is when the fixture's endpoint was registered. The
// upgrade backfills each subscription's created_at from its endpoint, so this is
// the value the migrated rows must carry — not the moment the migration ran.
var legacyEndpointCreatedAt = time.Date(2026, time.March, 1, 8, 30, 0, 0, time.UTC)

// TestSchemaUpgrade_SQLite runs the upgrade suite against SQLite. The same suite
// runs against real Postgres and MySQL in containers_test.go.
func TestSchemaUpgrade_SQLite(T *testing.T) {
	T.Parallel()

	runUpgradeSuite(T, newSQLiteEnv(T))
}

// runUpgradeSuite stands up the schema as it shipped, fills it with flat
// subscription rows, migrates, and then asks the current store to work against
// it.
//
// It is the acceptance test for the migration rather than for the DDL: that the
// ALTERs are accepted is the easy half, and that a consumer's existing
// subscriptions survive as identified, archivable rows is the half a consumer
// actually depends on.
func runUpgradeSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("migrates flat subscriptions into rows", func(t *testing.T) {
		t.Parallel()

		client, prefix := migrateLegacy(t, env)

		store, err := NewSQLStore(client, WithTablePrefix(prefix))
		must.NoError(t, err)

		got, err := store.GetEndpoint(t.Context(), readerOf(t, store), testScope, "legacy-endpoint")
		must.NoError(t, err)

		// The subscription set is unchanged, and the endpoint still reads.
		test.Eq(t, []EventType{orderCreated, orderUpdated}, got.EventTypes())
		test.EqOp(t, "https://93.184.216.34/hooks/legacy", got.URL)

		// Every migrated subscription is now identified, and no two share an ID.
		must.SliceLen(t, 2, got.Subscriptions)

		ids := map[string]struct{}{}
		for i := range got.Subscriptions {
			subscription := &got.Subscriptions[i]

			test.NotEqOp(t, "", subscription.ID)
			test.EqOp(t, "legacy-endpoint", subscription.EndpointID)
			test.Nil(t, subscription.ArchivedAt)

			// Backfilled from the endpoint, which is when it was really
			// subscribed — not the moment the migration ran.
			test.EqOp(t, legacyEndpointCreatedAt, subscription.CreatedAt)

			ids[subscription.ID] = struct{}{}
		}

		test.MapLen(t, 2, ids)

		// The endpoint's new metadata reads as unset rather than as garbage.
		test.EqOp(t, "", got.Name)
		test.ErrorIs(t, got.CreatedBy.Validate(), ErrNoScope)
	})

	// The fan-out query gained a predicate on a column that did not exist before
	// the upgrade. A migrated row must still be resolved by it.
	t.Run("migrated subscriptions still resolve for fan-out", func(t *testing.T) {
		t.Parallel()

		client, prefix := migrateLegacy(t, env)

		store, err := NewSQLStore(client, WithTablePrefix(prefix))
		must.NoError(t, err)

		test.Eq(t, []string{"legacy-endpoint"}, idsOf(endpointsFor(t, store, orderCreated)))
	})

	t.Run("a migrated subscription can be archived on its own", func(t *testing.T) {
		t.Parallel()

		client, prefix := migrateLegacy(t, env)

		store, err := NewSQLStore(client, WithTablePrefix(prefix))
		must.NoError(t, err)

		got, err := store.GetEndpoint(t.Context(), readerOf(t, store), testScope, "legacy-endpoint")
		must.NoError(t, err)
		must.SliceLen(t, 2, got.Subscriptions)

		retired := got.Subscriptions[0]
		must.NoError(t, archiveSubscription(t, store, testScope, retired.ID))

		after, err := store.GetEndpoint(t.Context(), readerOf(t, store), testScope, "legacy-endpoint")
		must.NoError(t, err)

		test.Eq(t, []EventType{orderUpdated}, after.EventTypes())
		test.SliceEmpty(t, endpointsFor(t, store, orderCreated))

		// And the row is still readable, which is the point of archiving it.
		archived, err := store.GetSubscription(t.Context(), readerOf(t, store), testScope, retired.ID)
		must.NoError(t, err)
		test.NotNil(t, archived.ArchivedAt)
	})
}

// migrateLegacy creates the shipped-as-it-was schema under a fresh prefix, seeds
// it with an endpoint and two flat subscription rows, and runs the upgrade.
func migrateLegacy(t *testing.T, env *storeEnv) (client database.Client, prefix string) {
	t.Helper()

	client = env.clientFor(t)
	prefix = fmt.Sprintf("wh_legacy_%d", prefixCounter.Add(1))
	qualified := ddl.Qualify(prefix)

	body, ok := legacySchema[env.dialect]
	must.True(t, ok, must.Sprintf("no legacy schema for dialect %q", env.dialect))

	for _, stmt := range dialect.SplitStatements(strings.ReplaceAll(body, ddl.Placeholder, qualified)) {
		_, err := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, err, must.Sprintf("executing %q", stmt))
	}

	// The three tables the upgrade does not touch come from the current DDL. The
	// two it does are already here in their old shape, and MySQL has no
	// CREATE INDEX IF NOT EXISTS to make re-creating their indexes a no-op.
	stmts, err := migrations.Statements(env.dialect, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		if strings.Contains(stmt, qualified+"webhooks_endpoints") ||
			strings.Contains(stmt, qualified+"webhooks_subscriptions") {
			continue
		}

		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	seedLegacyRows(t, env, client, qualified)

	upgrades, err := migrations.UpgradeStatements(env.dialect, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, upgrades)

	for _, stmt := range upgrades {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return client, prefix
}

// seedLegacyRows writes one endpoint and its two flat subscriptions, in the
// columns the old schema had.
func seedLegacyRows(t *testing.T, env *storeEnv, client database.Client, qualified string) {
	t.Helper()

	d := env.dialect

	endpoint := fmt.Sprintf(
		"INSERT INTO %swebhooks_endpoints (id, scope, url, content_type, secret_current, headers, created_at) VALUES (%s)",
		qualified, d.Placeholders(1, 7),
	)

	_, err := client.Writer().ExecContext(t.Context(), endpoint,
		"legacy-endpoint", testScope, "https://93.184.216.34/hooks/legacy", DefaultContentType,
		[]byte("legacy-secret"), []byte("{}"), legacyEndpointCreatedAt,
	)
	must.NoError(t, err)

	for _, eventType := range []EventType{orderCreated, orderUpdated} {
		subscription := fmt.Sprintf(
			"INSERT INTO %swebhooks_subscriptions (endpoint_id, event_type) VALUES (%s)",
			qualified, d.Placeholders(1, 2),
		)

		_, subErr := client.Writer().ExecContext(t.Context(), subscription, "legacy-endpoint", eventType.String())
		must.NoError(t, subErr)
	}
}
