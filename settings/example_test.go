package settings_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/settings/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"
)

// Example shows the flow this package exists for: an administrator defines a
// setting, a person answers it, and the request path reads the answer back with
// the default standing in for everyone who has not.
func Example() {
	ctx := context.Background()
	client, store := exampleWiring(ctx)

	// One catalog, so the scope is global — the shape a single-tenant
	// application has, behaving exactly as it would without the column.
	scope := tenancy.Global()

	// The administrative half. Written once, by a migration or an admin console,
	// rather than on a request path — and inside a transaction, because every
	// write here takes the caller's.
	var definition *settings.Definition

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error
		definition, txErr = store.CreateDefinition(ctx, tx, scope, &settings.Definition{
			Name:        "notifications.digest",
			Description: "how often a digest email is sent",
			Kind:        settings.KindString,
			Default:     pointer.To("weekly"),
			Enumeration: []string{"daily", "weekly", "never"},
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	ada := settings.Subject{Type: settings.SubjectUser, ID: "user-ada"}
	grace := settings.Subject{Type: settings.SubjectUser, ID: "user-grace"}

	// The request path. The value is checked against the definition inside the
	// write, so a setting can only hold what it admits.
	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.SetValue(ctx, tx, scope, ada, definition.Name, "daily")

		return txErr
	}); err != nil {
		panic(err)
	}

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.SetValue(ctx, tx, scope, ada, definition.Name, "hourly")

		return txErr
	}); err != nil {
		fmt.Println("refused:", errors.Is(err, settings.ErrNotEnumerated))
	}

	// Reading it back, on the client's reader because there is nothing to join.
	// Ada chose; Grace did not and gets the default.
	subjects := []settings.Subject{ada, grace}
	for i := range subjects {
		subject := subjects[i]

		resolved, resolveErr := store.Resolve(ctx, client.Reader(), scope, subject, definition.Name)
		if resolveErr != nil {
			panic(resolveErr)
		}

		digest, digestErr := resolved.String()
		if digestErr != nil {
			panic(digestErr)
		}

		fmt.Printf("%s: %s (%s)\n", subject.ID, digest, resolved.Source)
	}

	// Output:
	// refused: true
	// user-ada: daily (subject)
	// user-grace: weekly (default)
}

// Example_unset shows the third answer a resolution can give, and why it is a
// sentinel rather than a fallback parameter.
//
// A setting with no value and no default has not been decided by anybody, and a
// getter taking a default would answer it with whatever the caller guessed —
// leaving the caller unable to tell "nobody has said" from "somebody said this".
func Example_unset() {
	ctx := context.Background()
	client, store := exampleWiring(ctx)
	scope := tenancy.Global()

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.CreateDefinition(ctx, tx, scope, &settings.Definition{
			Name: "retention.days",
			Kind: settings.KindInt,
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	resolved, err := store.Resolve(ctx, client.Reader(), scope,
		settings.Subject{Type: settings.SubjectAccount, ID: "account-1"}, "retention.days")
	if err != nil {
		panic(err)
	}

	switch days, readErr := resolved.Int(); {
	case errors.Is(readErr, settings.ErrSettingUnset):
		fmt.Println("nobody has decided; the caller's own policy applies")
	case readErr != nil:
		panic(readErr)
	default:
		fmt.Println("retain for", days, "days")
	}

	// Output:
	// nobody has decided; the caller's own policy applies
}

// A preference change is rarely the only row a write produces. SetValue takes the
// transaction that carries its companions — here an audit entry naming who
// changed what — so neither can land without the other.
//
// Resolve runs on the same transaction, which is what lets a handler answer with
// the new effective value rather than with what the subject had before the
// request.
func ExampleStore_SetValue() {
	ctx := context.Background()
	client := exampleDatabase(ctx)
	scope := tenancy.Global()

	// The consumer's own table, standing in for whatever a real application
	// writes beside a setting: an audit entry, a data change event on an outbox.
	if _, err := client.Writer().ExecContext(ctx,
		`CREATE TABLE audit_log (subject_id TEXT NOT NULL, setting TEXT NOT NULL, value TEXT NOT NULL)`); err != nil {
		panic(err)
	}

	store, err := settings.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.CreateDefinition(ctx, tx, scope, &settings.Definition{
			Name:        "notifications.digest",
			Kind:        settings.KindString,
			Default:     pointer.To("weekly"),
			Enumeration: []string{"daily", "weekly", "never"},
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	ada := settings.Subject{Type: settings.SubjectUser, ID: "user-ada"}

	var effective string

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		value, txErr := store.SetValue(ctx, tx, scope, ada, "notifications.digest", "daily")
		if txErr != nil {
			return txErr
		}

		// The resolution the handler answers with, read on the transaction that
		// wrote the override. On the client's reader it would still say
		// "weekly (default)".
		resolved, txErr := store.Resolve(ctx, tx, scope, ada, "notifications.digest")
		if txErr != nil {
			return txErr
		}

		effective = fmt.Sprintf("%s (%s)", resolved.Raw, resolved.Source)

		// A failure here takes the value back with it, which is the whole
		// reason this is one transaction rather than two.
		_, txErr = tx.ExecContext(ctx,
			`INSERT INTO audit_log (subject_id, setting, value) VALUES (?, ?, ?)`,
			value.Subject.ID, "notifications.digest", value.Raw)

		return txErr
	}); err != nil {
		panic(err)
	}

	var audited string
	if err = client.Reader().QueryRowContext(ctx,
		`SELECT value FROM audit_log WHERE subject_id = ?`, ada.ID).Scan(&audited); err != nil {
		panic(err)
	}

	fmt.Println("effective:", effective)
	fmt.Println("audited:", audited)

	// Output:
	// effective: daily (subject)
	// audited: daily
}

// exampleWiring builds a throwaway SQLite-backed store and hands back the client
// beside it, because every write takes a transaction the caller opens. A real
// application hands migrations.SQL to its own migration run and builds the store
// over the database it already has.
func exampleWiring(ctx context.Context) (database.Client, settings.Store) {
	client := exampleDatabase(ctx)

	store, err := settings.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	return client, store
}

// exampleDatabase is a throwaway SQLite database with the settings tables in it.
// ExampleStore_ClearValue shows the reason a clearing answers with a row.
//
// The audit entry beside the write is about the answer that was taken back, and
// after the archive there is nowhere left to read it: every read here reaches
// live rows only, so a resolution says "weekly (default)" and GetValue says the
// subject has not answered. Reading it before the write instead would describe
// the row a statement earlier, and would not be inside the guard that decides
// whether the clearing happened at all.
func ExampleStore_ClearValue() {
	ctx := context.Background()
	client := exampleDatabase(ctx)
	scope := tenancy.Global()

	// The consumer's own table, standing in for whatever a real application
	// writes beside a setting.
	if _, err := client.Writer().ExecContext(ctx,
		`CREATE TABLE audit_log (subject_id TEXT NOT NULL, setting TEXT NOT NULL, value TEXT NOT NULL)`); err != nil {
		panic(err)
	}

	store, err := settings.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	ada := settings.Subject{Type: settings.SubjectUser, ID: "user-ada"}

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		if _, txErr := store.CreateDefinition(ctx, tx, scope, &settings.Definition{
			Name:        "notifications.digest",
			Kind:        settings.KindString,
			Default:     pointer.To("weekly"),
			Enumeration: []string{"daily", "weekly", "never"},
		}); txErr != nil {
			return txErr
		}

		_, txErr := store.SetValue(ctx, tx, scope, ada, "notifications.digest", "daily")

		return txErr
	}); err != nil {
		panic(err)
	}

	var effective string

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		cleared, txErr := store.ClearValue(ctx, tx, scope, ada, "notifications.digest")
		if txErr != nil {
			return txErr
		}

		// What the subject had chosen, from the row the statement moved. The
		// entry and the clearing land together or not at all.
		if _, txErr = tx.ExecContext(ctx,
			`INSERT INTO audit_log (subject_id, setting, value) VALUES (?, ?, ?)`,
			cleared.Subject.ID, "notifications.digest", cleared.Raw); txErr != nil {
			return txErr
		}

		resolved, txErr := store.Resolve(ctx, tx, scope, ada, "notifications.digest")
		if txErr != nil {
			return txErr
		}

		effective = fmt.Sprintf("%s (%s)", resolved.Raw, resolved.Source)

		return nil
	}); err != nil {
		panic(err)
	}

	// And the answer is unreadable through this package now, which is why the
	// return above was the last place it existed.
	_, err = store.GetValue(ctx, client.Reader(), scope, ada, "notifications.digest")

	fmt.Println("audited:", exampleAudited(ctx, client, ada))
	fmt.Println("effective:", effective)
	fmt.Println("readable:", !errors.Is(err, settings.ErrValueNotFound))

	// Output:
	// audited: daily
	// effective: weekly (default)
	// readable: false
}

// exampleAudited reads back the one row the example above wrote beside the
// clearing.
func exampleAudited(ctx context.Context, client database.Client, subject settings.Subject) string {
	var audited string

	if err := client.Reader().QueryRowContext(ctx,
		`SELECT value FROM audit_log WHERE subject_id = ?`, subject.ID).Scan(&audited); err != nil {
		panic(err)
	}

	return audited
}

func exampleDatabase(ctx context.Context) database.Client {
	dir, err := os.MkdirTemp("", "settings-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{
		connectionString: filepath.Join(dir, "settings.db"),
	})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, settings.DefaultTablePrefix)
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

// exampleClientConfig is the minimum database.ClientConfig a SQLite client
// needs.
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
