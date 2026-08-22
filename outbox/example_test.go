package outbox_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing/fstest"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/migrate"
	"github.com/primandproper/platform-go/v13/database/sqlite"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/outbox"
	"github.com/primandproper/platform-go/v13/outbox/migrations"
)

type order struct {
	ID    string `json:"id"`
	Total int    `json:"total"`
}

// exampleClientConfig is the minimum database.ClientConfig a SQLite client
// needs. A real deployment builds this through database/config.
type exampleClientConfig struct {
	connectionString string
}

func (c *exampleClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *exampleClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *exampleClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *exampleClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *exampleClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// exampleDatabase stands in for the consumer's own database wiring: a client
// with the outbox table created.
func exampleDatabase(ctx context.Context) (database.Client, func(), error) {
	dir, err := os.MkdirTemp("", "outbox-example")
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() { _ = os.RemoveAll(dir) }

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{connectionString: filepath.Join(dir, "example.db")})
	if err != nil {
		cleanup()

		return nil, nil, err
	}

	stmts, err := migrations.Statements(dialect.SQLite, outbox.DefaultTablePrefix)
	if err != nil {
		cleanup()

		return nil, nil, err
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			cleanup()

			return nil, nil, err
		}
	}

	return client, cleanup, nil
}

// insertOrder stands in for the caller's own state change.
func insertOrder(context.Context, database.SQLQueryExecutor, order) error { return nil }

func pendingMessages(ctx context.Context, client database.Client) int {
	var n int
	if err := client.Reader().
		QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_messages").
		Scan(&n); err != nil {
		panic(err)
	}

	return n
}

// ExampleWriter_Enqueue shows the whole point of the package: the event is
// written by the same executor that wrote the state change, so it commits with
// it — and cannot be lost by a commit that succeeds.
func ExampleWriter_Enqueue() {
	ctx := context.Background()

	client, cleanup, err := exampleDatabase(ctx)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	writer, err := outbox.NewWriter(dialect.SQLite)
	if err != nil {
		panic(err)
	}

	o := order{ID: "order-1", Total: 4200}

	err = client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		if insertErr := insertOrder(ctx, q, o); insertErr != nil {
			return insertErr
		}

		// Same executor, same transaction. Nothing is published here — the
		// Relay does that once the transaction has committed.
		return writer.Enqueue(ctx, q, outbox.Message{
			Topic: "orders",
			// Messages sharing a Key publish in the order they were enqueued.
			Key:     o.ID,
			Payload: o,
		})
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("awaiting publication:", pendingMessages(ctx, client))
	// Output: awaiting publication: 1
}

// ExampleWriter_Enqueue_rollback shows the other half of the guarantee: an
// event enqueued by a transaction that later fails never reaches the broker,
// because it was never committed in the first place.
func ExampleWriter_Enqueue_rollback() {
	ctx := context.Background()

	client, cleanup, err := exampleDatabase(ctx)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	writer, err := outbox.NewWriter(dialect.SQLite)
	if err != nil {
		panic(err)
	}

	o := order{ID: "order-2", Total: 900}

	err = client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		if enqueueErr := writer.Enqueue(ctx, q, outbox.Message{Topic: "orders", Payload: o}); enqueueErr != nil {
			return enqueueErr
		}

		// Something later in the transaction fails. Returning an error is the
		// only way to abort, and it rolls the outbox row back too.
		return platformerrors.New("payment declined")
	})

	fmt.Println("transaction failed:", err != nil)
	fmt.Println("awaiting publication:", pendingMessages(ctx, client))
	// Output:
	// transaction failed: true
	// awaiting publication: 0
}

// orderChanged is the consumer's own message type — the payload a repository
// method enqueues when it writes an order. The outbox never looks inside one,
// which is what leaves a side effect free to assert it back out.
type orderChanged struct {
	OrderID string `json:"order_id"`
}

func messagesOnTopic(ctx context.Context, client database.Client, topic string) int {
	var n int
	if err := client.Reader().
		QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_messages WHERE topic = ?", topic).
		Scan(&n); err != nil {
		panic(err)
	}

	return n
}

// ExampleWithWriterSideEffect shows the obligation moving off the call site.
// The transaction below names one message and writes two: the index event is
// registered on the Writer rather than remembered by whoever wrote the
// repository method, which is what makes it impossible to omit — and an omitted
// index event is one nothing downstream would have reported missing.
func ExampleWithWriterSideEffect() {
	ctx := context.Background()

	client, cleanup, err := exampleDatabase(ctx)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	writer, err := outbox.NewWriter(dialect.SQLite,
		outbox.WithWriterSideEffect("orders-index",
			func(_ context.Context, _ database.SQLQueryExecutor, msgs []outbox.Message) ([]outbox.Message, error) {
				events := make([]outbox.Message, 0, len(msgs))

				for i := range msgs {
					changed, ok := msgs[i].Payload.(orderChanged)
					if !ok {
						continue
					}

					// Keyed by document ID, which is what buys per-document
					// ordering out of the relay. search/sync's Event.Message
					// does exactly this against a real index.
					events = append(events, outbox.Message{
						Topic:   "orders-index",
						Key:     changed.OrderID,
						Payload: changed,
					})
				}

				return events, nil
			}))
	if err != nil {
		panic(err)
	}

	o := order{ID: "order-3", Total: 1500}

	err = client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		if insertErr := insertOrder(ctx, q, o); insertErr != nil {
			return insertErr
		}

		// One message asked for. Both are written by this statement, inside
		// this transaction, and roll back together if it fails.
		return writer.Enqueue(ctx, q, outbox.Message{
			Topic:   "orders",
			Key:     o.ID,
			Payload: orderChanged{OrderID: o.ID},
		})
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("awaiting publication:", pendingMessages(ctx, client))
	fmt.Println("index events:", messagesOnTopic(ctx, client, "orders-index"))
	// Output:
	// awaiting publication: 2
	// index events: 1
}

// ExampleSQL shows the preferred way to create the table when you already run
// database/migrate: the DDL is rendered from code and placed in your own
// migration sequence at a version you choose, so nothing is copied into your
// repository and nothing drifts as this package evolves.
func ExampleSQL() {
	ddl, err := migrations.SQL(dialect.Postgres, outbox.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	// myMigrations is the embed.FS of your own numbered migration files. Pick a
	// version in your sequence and never change it — goose keys applied
	// migrations by version. A version a file on disk already uses fails here,
	// not mid-deploy.
	myMigrations := fstest.MapFS{}

	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(37, "create_outbox_messages", ddl),
	)
	if err != nil {
		panic(err)
	}

	// m.Migrate(ctx, db) now creates the outbox table alongside your own schema.
	_ = m

	fmt.Println("outbox DDL staged as a migration")
	// Output: outbox DDL staged as a migration
}

// ExampleStatements shows the same DDL as individually executable statements,
// for callers not using database/migrate.
func ExampleStatements() {
	stmts, err := migrations.Statements(dialect.Postgres, outbox.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	fmt.Println("statements:", len(stmts))
	// Output: statements: 4
}
