package audit_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/audit/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
)

type recipe struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OwnerID  string `json:"ownerID"`
	Servings int    `json:"servings"`
}

// Example shows the shape every write site takes: the audit entry goes into the
// same transaction as the change it describes, so the two cannot disagree — and
// then the chain over what was written is verified.
func Example() {
	ctx := context.Background()

	client := exampleDatabase(ctx)
	defer func() { _ = client.Close() }()

	recorder, err := audit.NewRecorder(dialect.SQLite)
	if err != nil {
		panic(err)
	}

	reader, err := audit.NewReader(client)
	if err != nil {
		panic(err)
	}

	var (
		before = &recipe{ID: "r1", Name: "Soup", OwnerID: "acct_1", Servings: 2}
		after  = &recipe{ID: "r1", Name: "Stew", OwnerID: "acct_1", Servings: 4}
	)

	entry := &audit.Entry{
		EventType:    audit.EventUpdated,
		ResourceType: "recipe",
		ResourceID:   after.ID,
		Scope:        after.OwnerID,
		Actor:        audit.Actor{ID: "user_123", Type: audit.ActorUser, IP: "203.0.113.7"},
	}

	if err = client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		// ... the caller's own UPDATE, using the same q ...

		if entry.Changes, err = audit.Diff(before, after); err != nil {
			return err
		}

		return recorder.Record(ctx, q, entry)
	}); err != nil {
		panic(err)
	}

	result, err := reader.Verify(ctx, after.OwnerID, time.Time{}, time.Time{})
	if err != nil {
		panic(err)
	}

	fmt.Println("position:", entry.Seq)
	fmt.Println("changed:", len(entry.Changes), "fields")
	fmt.Println("intact:", result.Intact())

	// Output:
	// position: 0
	// changed: 2 fields
	// intact: true
}

// ExampleDiff shows what a before/after pair produces, including how a nil side
// reads.
func ExampleDiff() {
	before := &recipe{ID: "r1", Name: "Soup", Servings: 2}
	after := &recipe{ID: "r1", Name: "Stew", Servings: 4}

	changes, err := audit.Diff(before, after)
	if err != nil {
		panic(err)
	}

	fmt.Println("name:", changes["name"].Old, "->", changes["name"].New)
	fmt.Println("servings:", changes["servings"].Old, "->", changes["servings"].New)

	created, err := audit.Diff(nil, after)
	if err != nil {
		panic(err)
	}

	fmt.Println("on create, old is:", created["name"].Old)

	// Output:
	// name: Soup -> Stew
	// servings: 2 -> 4
	// on create, old is: <nil>
}

// ExampleWithRedaction shows a field being kept out of the table entirely and
// another being reduced to a digest, both decided at the write rather than at
// the query.
func ExampleWithRedaction() {
	ctx := context.Background()

	client := exampleDatabase(ctx)
	defer func() { _ = client.Close() }()

	recorder, err := audit.NewRecorder(dialect.SQLite,
		// Applies to every resource type: this is a rule about the field name.
		audit.WithRedaction("", audit.Redaction{Drop: []string{"password"}}),
		// Rotating an API key is a real event; the new key is not a thing to
		// write down, but "is it the same one as before" still is.
		audit.WithRedaction("api_key", audit.Redaction{Hash: []string{"secret"}}),
	)
	if err != nil {
		panic(err)
	}

	entry := &audit.Entry{
		EventType:    audit.EventUpdated,
		ResourceType: "api_key",
		ResourceID:   "key_1",
		Actor:        audit.Actor{ID: "user_123", Type: audit.ActorUser},
		Changes: map[string]audit.Change{
			"password": {New: "hunter2"},
			"secret":   {New: "sk_live_abcdef"},
			"label":    {Old: "old label", New: "new label"},
		},
	}

	if err = client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		return recorder.Record(ctx, q, entry)
	}); err != nil {
		panic(err)
	}

	_, recorded := entry.Changes["password"]
	fmt.Println("password recorded:", recorded)
	fmt.Printf("secret recorded as: %.7s...\n", entry.Changes["secret"].New)
	fmt.Println("label recorded as:", entry.Changes["label"].New)

	// Output:
	// password recorded: false
	// secret recorded as: sha256:...
	// label recorded as: new label
}

// ExampleSQL shows the DDL a consumer hands to their own migration run.
func ExampleSQL() {
	ddl, err := migrations.SQL(dialect.Postgres, audit.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	// In a real application this goes to database/migrate at a version you
	// choose, so the tables are created by your normal migration run:
	//
	//	m, err := migrate.New(dialect.Postgres, myMigrations,
	//		migrate.WithGeneratedMigration(39, "create_audit_tables", ddl),
	//	)
	fmt.Println(ddl[:len("CREATE TABLE IF NOT EXISTS audit_log_entries")])

	// Output: CREATE TABLE IF NOT EXISTS audit_log_entries
}

// exampleDatabase stands up a throwaway SQLite database with the audit tables
// already created.
func exampleDatabase(ctx context.Context) database.Client {
	dir, err := os.MkdirTemp("", "audit-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{
		connectionString: filepath.Join(dir, "audit.db"),
	})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, audit.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			panic(err)
		}
	}

	return client
}

type exampleClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*exampleClientConfig)(nil)

func (c *exampleClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *exampleClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *exampleClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *exampleClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *exampleClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }
