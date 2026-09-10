package issuereports_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/issuereports/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/tenancy"
)

// Taking a report is one write, and working the queue is one read plus one
// guarded move.
//
// The writes take the caller's transaction and the reads take an executor, so a
// caller with nothing to commit alongside opens one with Client.WithTransaction
// and reads afterwards on the client.
func Example() {
	ctx := context.Background()

	client := exampleClient(ctx)

	store, err := issuereports.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Of("acct_1")

	// What the form collected. Kind is the application's own category; the store
	// stores it and never interprets it, and the subject is how the application
	// names the thing being reported on.
	report := &issuereports.Report{
		Reporter:    "user_1",
		Kind:        "bug",
		Details:     "the save button does nothing on the recipe editor",
		SubjectType: "recipes",
		SubjectID:   "recipe_1",
	}

	var filed *issuereports.Report

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error
		filed, txErr = store.CreateReport(ctx, tx, scope, report)

		return txErr
	}); err != nil {
		panic(err)
	}

	// The write answers with the row it wrote: the id it minted, the status a
	// report is born in, the creation time the database assigned. The value
	// handed in is untouched.
	fmt.Println("filed as:", filed.Status)

	// The triage queue. The count beside the page is of everything open rather
	// than of the page, so a console asking for ten reports still knows how many
	// there are.
	queue, err := store.ListReportsByStatus(ctx, client.Reader(), scope, issuereports.StatusOpen, nil)
	if err != nil {
		panic(err)
	}

	fmt.Println("open reports:", queue.FilteredCount)

	// Working it. The status the triager read goes over with the one they are
	// moving to, so a second triager holding the same view loses rather than
	// silently overwriting this note.
	var resolved *issuereports.Report

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error
		resolved, txErr = store.TransitionReport(ctx, tx, scope, filed.ID,
			issuereports.StatusOpen, issuereports.StatusResolved, "fixed in 1.4")

		return txErr
	}); err != nil {
		panic(err)
	}

	fmt.Println("resolved:", resolved.Closed(), "-", resolved.Resolution)

	// Output:
	// filed as: open
	// open reports: 1
	// resolved: true - fixed in 1.4
}

// The guard is what makes the queue safe for more than one triager. The second
// of two people acting on the same view of a report is told so rather than
// overwriting the first one's decision — and each of them is in their own
// transaction, which is where the entry naming who decided it goes.
func ExampleStore_TransitionReport() {
	ctx := context.Background()

	client := exampleClient(ctx)

	store, err := issuereports.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Of("acct_1")

	report := &issuereports.Report{
		Reporter: "user_1",
		Kind:     "billing",
		Details:  "charged twice for one order",
	}

	var filed *issuereports.Report

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error
		filed, txErr = store.CreateReport(ctx, tx, scope, report)

		return txErr
	}); err != nil {
		panic(err)
	}

	// Both triagers read the report while it was open.
	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.TransitionReport(ctx, tx, scope, filed.ID,
			issuereports.StatusOpen, issuereports.StatusResolved, "refunded")

		return txErr
	}); err != nil {
		panic(err)
	}

	err = client.WithTransaction(ctx, func(tx database.Tx) error {
		_, txErr := store.TransitionReport(ctx, tx, scope, filed.ID,
			issuereports.StatusOpen, issuereports.StatusDeclined, "duplicate")

		return txErr
	})

	fmt.Println("second triager:", err != nil)

	current, err := store.GetReport(ctx, client.Reader(), scope, filed.ID)
	if err != nil {
		panic(err)
	}

	fmt.Println("still says:", current.Status, "-", current.Resolution)

	// Output:
	// second triager: true
	// still says: resolved - refunded
}

// A decision on a report is rarely the only row it produces, and this is what
// the transaction argument is for: the move and its companions — here an audit
// entry naming who decided it — are one transaction, so neither can land without
// the other.
func ExampleStore_TransitionReport_withAnAuditEntry() {
	ctx := context.Background()

	client := exampleClient(ctx)

	// The consumer's own table, standing in for whatever a real application
	// writes beside a report: an audit entry, a data change event on an outbox.
	if _, err := client.Writer().ExecContext(ctx,
		`CREATE TABLE audit_log (report_id TEXT NOT NULL, actor TEXT NOT NULL, outcome TEXT NOT NULL)`); err != nil {
		panic(err)
	}

	store, err := issuereports.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Of("acct_1")

	report := &issuereports.Report{
		Reporter: "user_1",
		Kind:     "billing",
		Details:  "charged twice for one order",
	}

	var filed *issuereports.Report

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error
		filed, txErr = store.CreateReport(ctx, tx, scope, report)

		return txErr
	}); err != nil {
		panic(err)
	}

	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		resolved, txErr := store.TransitionReport(ctx, tx, scope, filed.ID,
			issuereports.StatusOpen, issuereports.StatusResolved, "refunded")
		if txErr != nil {
			return txErr
		}

		// A failure here takes the decision back with it, which is the whole
		// reason this is one transaction rather than two. The row handed back is
		// the one this transaction wrote, so the entry describes what will
		// commit.
		_, txErr = tx.ExecContext(ctx,
			`INSERT INTO audit_log (report_id, actor, outcome) VALUES (?, ?, ?)`,
			resolved.ID, "triager_1", resolved.Status.String())

		return txErr
	}); err != nil {
		panic(err)
	}

	var actor, outcome string
	if err = client.Reader().QueryRowContext(ctx,
		`SELECT actor, outcome FROM audit_log WHERE report_id = ?`, filed.ID).Scan(&actor, &outcome); err != nil {
		panic(err)
	}

	fmt.Println("audited:", actor, "-", outcome)

	// Output:
	// audited: triager_1 - resolved
}

// exampleClient is a throwaway SQLite database with the issue reports table in
// it, so the examples above run as written.
func exampleClient(ctx context.Context) database.Client {
	dir, err := os.MkdirTemp("", "issuereports-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx,
		&exampleClientConfig{connectionString: filepath.Join(dir, "issuereports.db")})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, issuereports.DefaultTablePrefix)
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
