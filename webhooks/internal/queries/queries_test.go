package queries

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/webhooks/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI.
//
// The .sql files are what sqlc is run over, and the whole value of running it is
// that they are the statements the store executes. A hand-edit to one — or a
// column list changed without regenerating — would leave sqlc checking SQL
// nobody runs, which is a green check over an unchecked store.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			// The committed file carries the generated-code header, which is
			// the generator's rather than this function's.
			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// Nothing here emits a standard set, so nothing registers a table as a side
// effect of emitting for it — Render registers the whole list explicitly, and
// this pins that it does. A consumer reading the registry back to truncate a
// database between integration tests otherwise leaves rows behind, and the
// symptom is a different test failing later on rows the previous one left.
func TestRender_RegistersEveryTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestTableNames_AreTheTablesTheDDLCreates is the cross-check between the two
// halves of "what tables does webhooks own": the canonical spelling here, which
// every emitted statement interpolates, and the list migrations.Tables reads out
// of the DDL for a consumer.
//
// Neither derives from the other on purpose — one is a Go constant a statement
// interpolates, the other is read from the schema that creates the table — so
// this is where a table added to one and not the other stops being invisible.
func TestTableNames_AreTheTablesTheDDLCreates(t *testing.T) {
	t.Parallel()

	created, err := migrations.Tables("")
	must.NoError(t, err)

	declared := slices.Clone(TableNames)
	slices.Sort(declared)

	test.Eq(t, created, declared)
}

// TestTables_ColumnsAreTheColumnsTheDDLDeclares pins the other half of the
// schema-as-data claim, and it is the half a rendering test cannot make: every
// column list above is what the emitted SELECTs project and what the generated
// row types are built from, so a column renamed in a migration and not here
// yields a corpus that sqlc rejects — but a column *missing* from a list yields
// one it accepts, over a projection quietly narrower than the table.
func TestTables_ColumnsAreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, table := range Tables {
				declared := columnsOf(t, ddl, table.Name)
				must.SliceNotEmpty(t, declared, must.Sprintf("no CREATE TABLE for %s", table.Name))

				want := slices.Clone(table.Columns)
				slices.Sort(want)
				slices.Sort(declared)

				test.Eq(t, want, declared, test.Sprintf("%s columns", table.Name))
			}
		})
	}
}

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
//
// Every paged read appears twice, under its name and that name plus Descending:
// a sort direction is which way the ORDER BY runs and which way the cursor
// comparison points, so it is answered by a second statement rather than by a
// bound argument. The two unpaged lists have one entry each, because neither
// takes a filter to carry a direction.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		"UpsertEndpoint", "GetEndpoint", "GetEndpointScope",
		"ListEndpoints", "ListEndpointsDescending", "ArchiveEndpoint", "ListEndpointsForEvent",
		"UpsertSubscription", "GetSubscriptionByPair", "ListSubscriptionsForEndpoint",
		"ListSubscriptions", "ListSubscriptionsDescending",
		"GetSubscription", "ArchiveSubscriptionByPair", "ArchiveSubscription",
		"InsertDelivery",
		"InsertDispatch", "SelectClaimableDispatches", "ClaimDispatches", "FetchClaimedDispatches",
		"MarkDispatchDelivered", "RecordDispatchFailure", "RequeueDispatch",
		"DispatchBacklog", "ReapDispatches", "ReapDeliveries",
		"InsertAttempt", "ListAttempts", "ListAttemptsDescending", "ReapAttempts",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			test.Eq(t, want, statementNames(Render(d)))
		})
	}
}

// TestRender_ScopesEveryConsumerRead is the tenancy roster, checked rather than
// asserted in a comment.
//
// webhooks is this module's tenancy worked example, and the claim it makes is
// that no read path omits the scope. The statements that do omit it are
// enumerated here, each with the reason it is allowed to — the three that are
// the second half of a read already scoped in the same transaction, and the
// worker's own machinery, which drains one queue for every tenant. A statement
// added without a scope predicate and without a line here fails this.
func TestRender_ScopesEveryConsumerRead(T *testing.T) {
	T.Parallel()

	unscoped := map[string]string{
		// Scoped one statement earlier, inside the same transaction, on an
		// endpoint id that came out of that scoped read rather than off the wire.
		"UpsertSubscription":           "writes a pair on an endpoint the caller has already read within a scope",
		"GetSubscriptionByPair":        "reads back what AddSubscription just wrote to an endpoint it read within a scope",
		"ListSubscriptionsForEndpoint": "fills an endpoint the caller has already read within a scope",
		"ArchiveSubscriptionByPair":    "retires a pair of an endpoint the save has already scoped",

		// The collision check, whose whole question is whose the row already is.
		"GetEndpointScope": "reads the scope rather than filtering on it",

		// The delivery worker's own machinery. One worker drains one queue for a
		// whole deployment; each of these addresses a row it is already holding,
		// or the queue entire.
		"InsertDelivery":            "writes the scope it was handed",
		"InsertDispatch":            "fans out within a delivery already scoped",
		"SelectClaimableDispatches": "the worker's claim, across every tenant",
		"ClaimDispatches":           "leases the ids the claim selected",
		"FetchClaimedDispatches":    "reads back the ids the worker just leased",
		"MarkDispatchDelivered":     "retires a dispatch the worker is holding",
		"RecordDispatchFailure":     "reschedules a dispatch the worker is holding",
		"RequeueDispatch":           "an operator's re-drive of a named pair",
		"DispatchBacklog":           "the queue's depth and age, across every tenant",
		"ReapDispatches":            "the retention sweep, across every tenant",
		"ReapDeliveries":            "the retention sweep, across every tenant",
		"ReapAttempts":              "the retention sweep, across every tenant",
		"InsertAttempt":             "appends to the log of a delivery already scoped",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, body := range statements(Render(d)) {
				if reason, allowed := unscoped[name]; allowed {
					test.NotEqOp(t, "", reason)

					continue
				}

				test.StrContains(t, body, "sqlc.arg("+ScopeColumn+")",
					test.Sprintf("%s binds no scope and is not on the exemption roster", name))
			}
		})
	}
}

// statementNames lists the query names a rendered file declares, in the order
// it declares them — which is the order Render assembles the groups in, and so
// the order a reader of the .sql meets them.
func statementNames(rendered string) []string {
	var ordered []string

	for line := range strings.SplitSeq(rendered, "\n") {
		if match := annotation.FindStringSubmatch(line); match != nil {
			ordered = append(ordered, match[1])
		}
	}

	return ordered
}

// statements splits a rendered file into its named statements.
func statements(rendered string) map[string]string {
	found := map[string]string{}

	var (
		name string
		body strings.Builder
	)

	flush := func() {
		if name != "" {
			found[name] = body.String()
		}

		body.Reset()
	}

	for line := range strings.SplitSeq(rendered, "\n") {
		if match := annotation.FindStringSubmatch(line); match != nil {
			flush()

			name = match[1]

			continue
		}

		body.WriteString(line)
		body.WriteString("\n")
	}

	flush()

	return found
}

// annotation matches the `-- name: X :type` line sqlc reads above a statement.
var annotation = regexp.MustCompile(`^-- name: (\w+) :`)

// columnsOf reads one table's column names out of rendered DDL.
//
// It parses the CREATE TABLE body rather than asking a database, so the check
// runs with nothing installed: what it needs is the first identifier of every
// line that declares a column, and the constraint lines this schema writes are
// the ones it skips.
func columnsOf(t *testing.T, ddl, table string) []string {
	t.Helper()

	open := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS "+table+" (")
	if open < 0 {
		return nil
	}

	body := ddl[open:]
	body = body[strings.Index(body, "(")+1 : strings.Index(body, "\n);")]

	var columns []string

	for line := range strings.SplitSeq(body, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}

		switch strings.ToUpper(fields[0]) {
		case "PRIMARY", "UNIQUE", "FOREIGN", "CONSTRAINT", "CHECK", "KEY", "INDEX":
			continue
		}

		columns = append(columns, fields[0])
	}

	return columns
}
