package shredding_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/kms/local"
	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/cryptography/shredding/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/sqlite"
)

// The lifecycle in full: a column encrypted under its subject's own key, and
// the same column after that key has been destroyed.
//
// The associated data binds the ciphertext to the row and column it lives in, so
// a value lifted into another row fails to open instead of quietly decrypting.
// There is no Encrypted[T] wrapper implementing driver.Valuer and sql.Scanner,
// and deliberately never will be: neither method takes a context, and fetching a
// key is a network call. Doing it here costs two lines and keeps the fetch
// visible.
func Example() {
	ctx := context.Background()

	keys, err := exampleKeys(ctx)
	if err != nil {
		panic(err)
	}

	subject := shredding.Subject{Type: "user", ID: "user-1"}

	sealed, err := keys.Encrypt(ctx, subject, []byte("221B Baker Street"), []byte("users.address:user-1"))
	if err != nil {
		panic(err)
	}

	opened, err := keys.Decrypt(ctx, subject, sealed, []byte("users.address:user-1"))
	if err != nil {
		panic(err)
	}

	fmt.Printf("before: %s\n", opened)

	receipt, err := keys.Shred(ctx, subject)
	if err != nil {
		panic(err)
	}

	fmt.Printf("destroyed: %t\n", receipt.Destroyed)

	// Unreadable here, and equally unreadable in every backup taken before the
	// shred — which is the half a DELETE cannot reach.
	if _, err = keys.Decrypt(ctx, subject, sealed, []byte("users.address:user-1")); err != nil {
		fmt.Printf("after: %v\n", errors.Is(err, shredding.ErrSubjectShredded))
	}

	// Nothing writes about this subject again either.
	if _, err = keys.Encrypt(ctx, subject, []byte("new address"), nil); err != nil {
		fmt.Printf("rewrite refused: %v\n", errors.Is(err, shredding.ErrSubjectShredded))
	}

	// Output:
	// before: 221B Baker Street
	// destroyed: true
	// after: true
	// rewrite refused: true
}

// Rendering the DDL for a consumer's own migration run.
//
// Where it runs is the decision this feature rests on. A keys table backed up
// alongside the data it protects hands everything back on the first restore, so
// this belongs in a database with a shorter retention than that one.
func ExampleSQL() {
	ddl, err := migrations.SQL(dialect.Postgres, shredding.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	fmt.Println(ddl[:12])
	// Output: CREATE TABLE
}

// exampleKeys wires a Keys over a throwaway SQLite database and a locally held
// root key.
//
// The local wrapper is the weak one: the root key is in this process, so it is
// in a core dump. A real deployment passes cryptography/encryption/kms/gcp or
// /aws, where the root key never leaves the KMS.
func exampleKeys(ctx context.Context) (shredding.Keys, error) {
	dir, err := os.MkdirTemp("", "shredding-example")
	if err != nil {
		return nil, err
	}

	client, err := sqlite.NewDatabaseClient(ctx,
		&exampleClientConfig{connectionString: filepath.Join(dir, "keys.db")})
	if err != nil {
		return nil, err
	}

	stmts, err := migrations.Statements(dialect.SQLite, shredding.DefaultTablePrefix)
	if err != nil {
		return nil, err
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			return nil, err
		}
	}

	store, err := shredding.NewSQLStore(client)
	if err != nil {
		return nil, err
	}

	wrapper, err := exampleWrapper()
	if err != nil {
		return nil, err
	}

	return shredding.NewKeys(store, wrapper, shredding.WithKeyTTL(shredding.DefaultKeyTTL))
}

func exampleWrapper() (encryption.KeyWrapper, error) {
	cipher, err := aes.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		return nil, err
	}

	return local.NewKeyWrapper(cipher)
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
