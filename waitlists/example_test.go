package waitlists_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Example shows the flow this package exists for: a list is opened, somebody
// joins it from a form, and they are invited when it is their turn.
//
// Every write runs inside a transaction the caller owns, which is where the
// consumer's own audit entry and outbox row go. Here there is nothing to commit
// alongside, so each one opens a transaction of its own.
func Example() {
	ctx := context.Background()
	store, client := exampleWiring()

	// One catalog, so the scope is global — the shape a single-tenant
	// application has, behaving exactly as it would without the column.
	scope := tenancy.Global()

	// The administrative half. Written once, by whoever decides the launch is
	// happening, rather than on a request path.
	var list *waitlists.List

	err := client.WithTransaction(ctx, func(tx database.Tx) (createErr error) {
		list, createErr = store.CreateList(ctx, tx, scope, &waitlists.List{
			Name:        "Launch",
			Description: "early access to the beta",
			ClosesAt:    time.Now().Add(30 * 24 * time.Hour),
		})

		return createErr
	})
	if err != nil {
		panic(err)
	}

	// The request path. The contact is stored as it was given and digested as
	// Normalize renders it, so two capitalizations of one address are one
	// person.
	var signup *waitlists.Signup

	err = client.WithTransaction(ctx, func(tx database.Tx) (joinErr error) {
		signup, joinErr = store.Join(ctx, tx, scope, list.ID, &waitlists.Signup{
			Contact: "Ada@Example.com",
			Notes:   "asked about the API",
		})

		return joinErr
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("joined as:", signup.Status)

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		_, joinErr := store.Join(ctx, tx, scope, list.ID, &waitlists.Signup{Contact: "ada@example.com"})

		return joinErr
	}); err != nil {
		fmt.Println("already on the list:", errors.Is(err, waitlists.ErrAlreadySignedUp))
	}

	// When it is their turn. The move is a guarded write, so a second
	// invitation loses rather than sending a second email — and the one that
	// won hands back the signup it moved, which is what the invitation is
	// written from.
	var invited *waitlists.Signup

	invite := func() error {
		return client.WithTransaction(ctx, func(tx database.Tx) error {
			moved, inviteErr := store.Invite(ctx, tx, scope, list.ID, signup.ID)
			if inviteErr != nil {
				return inviteErr
			}

			invited = moved

			return nil
		})
	}

	if err = invite(); err != nil {
		panic(err)
	}

	if err = invite(); err != nil {
		fmt.Println("invited once:", errors.Is(err, waitlists.ErrWrongStatus))
	}

	fmt.Println("now:", invited.Status)

	// Output:
	// joined as: waiting
	// already on the list: true
	// invited once: true
	// now: invited
}

// Example_withdrawal shows the obligation this package is shaped around: an
// address that asks to come off a list stays off it, and the row that remembers
// that no longer holds the address.
func Example_withdrawal() {
	ctx := context.Background()
	store, client := exampleWiring()
	scope := tenancy.Global()

	var (
		list   *waitlists.List
		signup *waitlists.Signup
		left   *waitlists.Signup
	)

	// The list, the signup and the withdrawal, each in a transaction a consumer
	// would be sharing with its own writes.
	err := client.WithTransaction(ctx, func(tx database.Tx) (txErr error) {
		if list, txErr = store.CreateList(ctx, tx, scope, &waitlists.List{
			Name:     "Launch",
			ClosesAt: time.Now().Add(30 * 24 * time.Hour),
		}); txErr != nil {
			return txErr
		}

		if signup, txErr = store.Join(ctx, tx, scope, list.ID,
			&waitlists.Signup{Contact: "ada@example.com"}); txErr != nil {
			return txErr
		}

		left, txErr = store.Withdraw(ctx, tx, scope, list.ID, signup.ID)

		return txErr
	})
	if err != nil {
		panic(err)
	}

	// The withdrawal answers with the row as it stood before the blanking, which
	// is where a consumer's own record of who came off the list is written from.
	fmt.Printf("left: %q\n", left.Contact)

	// What is left in the table is the digest and the fact of the withdrawal.
	// The address, the notes and the subject reference are gone.
	withdrawn, err := store.GetSignupByContact(ctx, client.Reader(), scope, list.ID, "ada@example.com")
	if err != nil {
		panic(err)
	}

	fmt.Printf("status %s, contact %q\n", withdrawn.Status, withdrawn.Contact)

	// And a later signup from the same address is refused rather than quietly
	// re-subscribing somebody who asked to be left alone.
	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		_, joinErr := store.Join(ctx, tx, scope, list.ID, &waitlists.Signup{Contact: "ADA@example.com"})

		return joinErr
	}); err != nil {
		fmt.Println("stays off the list:", errors.Is(err, waitlists.ErrContactWithdrawn))
	}

	// Output:
	// left: "ada@example.com"
	// status withdrawn, contact ""
	// stays off the list: true
}

// exampleWiring builds a throwaway SQLite-backed store, and hands back the client
// beside it: every write takes a transaction, and the client is what opens one. A
// real application hands migrations.SQL to its own migration run and builds the
// store over the database it already has.
func exampleWiring() (waitlists.Store, database.Client) {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "waitlists-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{
		connectionString: filepath.Join(dir, "waitlists.db"),
	})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, waitlists.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			panic(err)
		}
	}

	store, err := waitlists.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	return store, client
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
