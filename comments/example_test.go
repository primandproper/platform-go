package comments_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The application's own vocabulary, declared as constants so the catalog below
// and every call site agree by type rather than by spelling.
const (
	recipeTarget comments.TargetType = "recipe"
	mealTarget   comments.TargetType = "meal"
)

// A discussion is one write per comment and two reads: the target's roots, then
// one root's replies.
//
// The writes take the caller's transaction and the reads take an executor, so a
// caller with nothing to commit alongside opens one with Client.WithTransaction
// and reads afterwards on the client.
func Example() {
	ctx := context.Background()

	client := exampleClient(ctx)

	store, err := comments.NewSQLStore(client,
		comments.WithTargets(comments.Targets{
			recipeTarget: {Description: "a recipe"},
			mealTarget:   {Description: "a meal"},
		}))
	if err != nil {
		panic(err)
	}

	scope := tenancy.Of("acct_1")
	recipe := comments.Target{Type: recipeTarget, ID: "recipe_1"}

	// Each write answers with the row it wrote — the identifier it minted, the
	// creation time the database assigned — rather than filling those in on the
	// value it was handed.
	var root, answer *comments.Comment

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		written, txErr := store.CreateComment(ctx, tx, scope, &comments.Comment{
			Target: recipe,
			Author: "user_1",
			Body:   "halved the sugar and it was still too sweet",
		})
		if txErr != nil {
			return txErr
		}

		root = written

		// A reply names its parent and nothing else about where it goes: its
		// target is its parent's, and the store fills it in on the row it hands
		// back. Written in the same transaction as its parent, it resolves it —
		// the parent read runs on the executor the write was handed.
		answer, txErr = store.CreateComment(ctx, tx, scope, &comments.Comment{
			ParentID: root.ID,
			Author:   "user_2",
			Body:     "try two thirds of the syrup as well",
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	fmt.Println("reply is about:", answer.Target.Type, answer.Target.ID)

	// The top of the discussion. The count beside the page is of every root on
	// the target rather than of the page, so a client asking for ten still knows
	// how many there are.
	roots, err := store.ListRootComments(ctx, client.Reader(), scope, recipe, nil)
	if err != nil {
		panic(err)
	}

	fmt.Println("roots:", roots.FilteredCount)

	replies, err := store.ListReplies(ctx, client.Reader(), scope, recipe, root.ID, nil)
	if err != nil {
		panic(err)
	}

	fmt.Println("replies to the first:", replies.FilteredCount)

	// Output:
	// reply is about: recipe recipe_1
	// roots: 1
	// replies to the first: 1
}

// The catalog is what stops a comment being written where nothing will list it.
// A target type nobody registered is refused at the write rather than stored and
// discovered as an absence.
func ExampleWithTargets() {
	ctx := context.Background()

	client := exampleClient(ctx)

	store, err := comments.NewSQLStore(client,
		comments.WithTargets(comments.Targets{
			recipeTarget: {Description: "a recipe"},
		}))
	if err != nil {
		panic(err)
	}

	misspelled := &comments.Comment{
		Target: comments.Target{Type: "recipies", ID: "recipe_1"},
		Author: "user_1",
		Body:   "this would have been stored under a type nothing lists",
	}

	refused := client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.CreateComment(ctx, tx, tenancy.Of("acct_1"), misspelled)

		return txErr
	})

	fmt.Println("refused:", refused != nil)
	fmt.Println("what can be commented on:", store.TargetTypes())

	// Output:
	// refused: true
	// what can be commented on: [recipe]
}

// A comment outlives the thing it is about, and the consumer's delete is what
// removes it. The sweep runs in the transaction that removes the target, so
// there is no window in which one is gone and the other is not.
func ExampleStore_DeleteCommentsForTarget() {
	ctx := context.Background()

	client := exampleClient(ctx)

	store, err := comments.NewSQLStore(client,
		comments.WithTargets(comments.Targets{recipeTarget: {Description: "a recipe"}}))
	if err != nil {
		panic(err)
	}

	scope := tenancy.Of("acct_1")
	recipe := comments.Target{Type: recipeTarget, ID: "recipe_1"}

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		root, txErr := store.CreateComment(ctx, tx, scope,
			&comments.Comment{Target: recipe, Author: "user_1", Body: "lovely"})
		if txErr != nil {
			return txErr
		}

		_, txErr = store.CreateComment(ctx, tx, scope, &comments.Comment{
			ParentID: root.ID, Author: "user_2", Body: "agreed",
		})

		return txErr
	}); err != nil {
		panic(err)
	}

	var swept int64

	// In the real thing, deleting the recipe happens in this transaction too.
	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		swept, err = store.DeleteCommentsForTarget(ctx, tx, scope, recipe)

		return err
	}); err != nil {
		panic(err)
	}

	fmt.Println("swept:", swept)

	// Output:
	// swept: 2
}

// A comment is rarely the only row a write produces, which is why the write takes
// the caller's transaction rather than opening one. Here that transaction also
// carries an audit entry naming who said it, so neither can land without the
// other.
func ExampleStore_CreateComment() {
	ctx := context.Background()

	client := exampleClient(ctx)

	// The consumer's own table, standing in for whatever a real application
	// writes beside a comment: an audit entry, a data change event on an outbox.
	if _, err := client.Writer().ExecContext(ctx,
		`CREATE TABLE audit_log (comment_id TEXT NOT NULL, actor TEXT NOT NULL)`); err != nil {
		panic(err)
	}

	store, err := comments.NewSQLStore(client,
		comments.WithTargets(comments.Targets{recipeTarget: {Description: "a recipe"}}))
	if err != nil {
		panic(err)
	}

	var comment *comments.Comment

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		// The entry names the row the write left, which is why the write hands
		// it back: the identifier it is keyed on is the one the store minted.
		written, txErr := store.CreateComment(ctx, tx, tenancy.Of("acct_1"), &comments.Comment{
			Target: comments.Target{Type: recipeTarget, ID: "recipe_1"},
			Author: "user_1",
			Body:   "this wants more salt",
		})
		if txErr != nil {
			return txErr
		}

		comment = written

		// A failure here takes the comment back with it, which is the whole
		// reason this is one transaction rather than two.
		_, txErr = tx.ExecContext(ctx,
			`INSERT INTO audit_log (comment_id, actor) VALUES (?, ?)`,
			comment.ID, comment.Author)

		return txErr
	}); err != nil {
		panic(err)
	}

	var actor string
	if err = client.Reader().QueryRowContext(ctx,
		`SELECT actor FROM audit_log WHERE comment_id = ?`, comment.ID).Scan(&actor); err != nil {
		panic(err)
	}

	fmt.Println("audited:", actor)

	// Output:
	// audited: user_1
}

// exampleClient is a throwaway SQLite database with the comments table in it, so
// the examples above run as written.
func exampleClient(ctx context.Context) database.Client {
	dir, err := os.MkdirTemp("", "comments-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx,
		&exampleClientConfig{connectionString: filepath.Join(dir, "comments.db")})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, comments.DefaultTablePrefix)
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

func (c *exampleClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *exampleClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *exampleClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *exampleClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *exampleClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }
