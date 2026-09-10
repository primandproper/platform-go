package mediaregistry_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	"github.com/primandproper/platform-go/v14/mediaregistry/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/tenancy"
	"github.com/primandproper/primitives-go/uploads"
	"github.com/primandproper/primitives-go/uploads/objectstorage"
)

// Example shows the flow the package exists for: bytes into storage, a row
// beside them, and an access decision made from the row rather than from the
// bucket.
func Example() {
	ctx := context.Background()
	client, manager, store := exampleWiring()

	// One tenant, so the scope is global — the shape a single-tenant
	// application has, behaving exactly as it would without the column.
	scope := tenancy.Global()

	// An ObjectInput rather than an Object: what a caller supplies, with no
	// Scope and no stamps on it. The write's argument is what the row is filed
	// under, and the row the write hands back is where everything it settled
	// appears.
	input := mediaregistry.ObjectInput{
		Key:         "avatars/ada/original.png",
		ContentType: "image/png",
		OwnerID:     "user_ada",
		BelongsTo:   mediaregistry.Subject{Type: "user", ID: "user_ada"},
	}

	// StoreAndRecord writes the bytes, then the row, and answers with what was
	// registered — the id it minted, the size counted from what actually went
	// past. The row goes in on the caller's transaction, so a consumer with a
	// profile row to update writes it in this same function and the two commit
	// together. Here there is nothing to join.
	var recorded *mediaregistry.Object

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error
		recorded, txErr = mediaregistry.StoreAndRecord(ctx, tx, scope, manager, store, input,
			strings.NewReader("\x89PNG not really"))

		return txErr
	}); err != nil {
		panic(err)
	}

	fmt.Println("size:", recorded.Size)

	// Later, a request arrives holding the key rather than the row id. The row
	// is what says whether this caller may have the bytes. This read is outside
	// any transaction, so it runs on the client's reader.
	found, err := store.GetObjectByKey(ctx, client.Reader(), scope, "avatars/ada/original.png")
	switch {
	case errors.Is(err, mediaregistry.ErrObjectNotFound):
		fmt.Println("no such object in this tenant")
	case err != nil:
		panic(err)
	default:
		fmt.Println("owner:", found.OwnerID)
		fmt.Println("may user_bob read it:", found.OwnerID == "user_bob")
	}

	// Archiving is metadata-only: the row is hidden and the object stays in the
	// bucket until the consumer's retention policy removes it. The write hands
	// the row back because it is the last read that can see it — the key those
	// surviving bytes are at is on it, and a retention sweep is written from
	// exactly that.
	var archived *mediaregistry.Object

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error
		archived, txErr = store.ArchiveObject(ctx, tx, scope, recorded.ID)

		return txErr
	}); err != nil {
		panic(err)
	}

	fmt.Println("archived key:", archived.Key)

	_, err = store.GetObjectByKey(ctx, client.Reader(), scope, "avatars/ada/original.png")
	fmt.Println("archived reads as absent:", errors.Is(err, mediaregistry.ErrObjectNotFound))

	// The object is still there.
	exists, err := manager.Exists(ctx, "avatars/ada/original.png")
	if err != nil {
		panic(err)
	}

	fmt.Println("bytes still in the bucket:", exists)

	// Output:
	// size: 15
	// owner: user_ada
	// may user_bob read it: false
	// archived key: avatars/ada/original.png
	// archived reads as absent: true
	// bytes still in the bucket: true
}

// ExampleStore_listObjectsBySubject shows the page a consumer serves when it is
// asked what is attached to one of its own rows.
func ExampleStore_listObjectsBySubject() {
	ctx := context.Background()
	client, manager, store := exampleWiring()

	scope := tenancy.Global()
	invoice := mediaregistry.Subject{Type: "invoice", ID: "invoice_2026_01"}

	for _, name := range []string{"january-receipt.pdf", "january-statement.pdf"} {
		input := mediaregistry.ObjectInput{
			Key:         "invoices/" + name,
			ContentType: "application/pdf",
			OwnerID:     "user_ada",
			BelongsTo:   invoice,
		}

		if err := client.WithTransaction(ctx, func(tx database.Tx) error {
			_, txErr := mediaregistry.StoreAndRecord(ctx, tx, scope, manager, store, input, strings.NewReader(name))

			return txErr
		}); err != nil {
			panic(err)
		}
	}

	page, err := store.ListObjectsBySubject(ctx, client.Reader(), scope, invoice, nil)
	if err != nil {
		panic(err)
	}

	for _, o := range page.Data {
		fmt.Println(o.Key)
	}

	// Output:
	// invoices/january-receipt.pdf
	// invoices/january-statement.pdf
}

// exampleWiring stands up an in-memory bucket and a SQLite-backed registry over
// a temporary file. A real deployment migrates the table through its own
// migration run — see mediaregistry/migrations.
//
// The client comes back beside the two seams because the caller needs it: it is
// what opens the transaction the writes take and what supplies the executor the
// reads run on.
func exampleWiring() (database.Client, uploads.UploadManager, mediaregistry.Store) {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "uploads-registry-example")
	if err != nil {
		panic(err)
	}

	manager, err := objectstorage.NewUploadManager(ctx,
		&objectstorage.Config{Provider: objectstorage.MemoryProvider, BucketName: "example"})
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{
		connectionString: filepath.Join(dir, "registry.db"),
	})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, mediaregistry.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			panic(err)
		}
	}

	store, err := mediaregistry.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	return client, manager, store
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
