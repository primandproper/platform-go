package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/billing/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// Every table here currently takes a standard set, so StandardCRUD registers all
// four on its own — which is exactly why this is worth pinning. The day one of
// them stops taking a set, a consumer reading the registry back to truncate a
// database between integration tests would leave that table's rows behind, and
// the symptom would be a different test failing later.
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
// halves of "what tables does billing own": the canonical spelling here, which
// the registry and the store's prefix rendering both read, and the list
// migrations.Tables reads out of the DDL for a consumer.
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

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI.
//
// The .sql files are what sqlc is run over and what sqlc-gen-unison generates the
// querier from, and the whole value of running either is that they are the
// statements the store executes. A hand-edit to one — or a column list changed
// without regenerating — would leave sqlc checking SQL nobody runs.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			// The committed file carries the generated-code header, which is the
			// generator's rather than this function's.
			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_ScopeIsInEveryStatement is the tenancy obligation, checked rather
// than promised.
//
// The exceptions are the four created_at read-backs, which key on an id inside
// the transaction that just minted it — see createdAtReads. Everything else in
// this corpus binds the scope, and a statement that stopped would be a read
// crossing tenants with nothing reporting it.
func TestRender_ScopeIsInEveryStatement(T *testing.T) {
	T.Parallel()

	unscoped := map[string]bool{
		"GetProductCreatedAt":      true,
		"GetSubscriptionCreatedAt": true,
		"GetPurchaseCreatedAt":     true,
		"GetTransactionCreatedAt":  true,
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, statement := range statements(t, d) {
				if unscoped[name] {
					continue
				}

				test.StrContains(t, statement, "scope",
					test.Sprintf("%s does not bind the scope", name))
			}
		})
	}
}

// TestRender_ExternalIDReadsSeeArchivedRows is the property that makes those four
// statements the collision checks as well as the reads.
//
// Each unique index in this schema covers archived rows deliberately, so a check
// that filtered them out would report a provider id free and hand the write to
// the index — a driver error where the caller wanted ErrTransactionExists.
func TestRender_ExternalIDReadsSeeArchivedRows(T *testing.T) {
	T.Parallel()

	names := []string{
		"GetProductByExternalID",
		"GetSubscriptionByExternalID",
		"GetPurchaseByExternalID",
		"GetTransactionByExternalID",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := statements(t, d)

			for _, name := range names {
				statement, ok := rendered[name]
				must.True(t, ok, must.Sprintf("%s was not emitted", name))

				test.StrNotContains(t, statement, "archived_at IS NULL",
					test.Sprintf("%s filters archived rows and cannot be a collision check", name))

				// It still projects the column, which is what lets the store tell
				// a collision from a hit.
				test.StrContains(t, statement, "archived_at",
					test.Sprintf("%s does not project archived_at", name))
			}
		})
	}
}

// TestRender_StatusWritesGuardOnWhatTheyAssign is what makes a redelivered
// provider event write nothing.
//
// Both statements name the status column in the SET and again in an inequality,
// under one argument — `SET status = X WHERE status <> X`. Under two arguments
// the guard would be answerable, and under an equality it would be the opposite
// statement: one that only ever writes what is already there.
func TestRender_StatusWritesGuardOnWhatTheyAssign(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := statements(t, d)

			for _, name := range []string{"SetSubscriptionStatus", "SetTransactionStatus"} {
				statement, ok := rendered[name]
				must.True(t, ok, must.Sprintf("%s was not emitted", name))

				test.StrContains(t, statement, "status <> ",
					test.Sprintf("%s is not guarded on the status it assigns", name))
			}

			completion, ok := rendered["CompletePurchase"]
			must.True(t, ok, must.Sprintf("CompletePurchase was not emitted"))
			test.StrContains(t, completion, "completed_at IS NULL",
				test.Sprintf("CompletePurchase is not guarded on the purchase being outstanding"))
		})
	}
}

// TestRender_CurrentSubscriptionsBindTheirHorizon is why the read and
// Subscription.CurrentAt cannot disagree.
//
// The comparison is against a bound instant rather than CURRENT_TIMESTAMP,
// because current_period_end is written by the application from whatever clock
// the store was handed — so a server-clock comparison would be two clocks
// deciding one row, and under a test clock the two are years apart.
func TestRender_CurrentSubscriptionsBindTheirHorizon(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := statements(t, d)

			for _, name := range []string{"ListCurrentSubscriptions", "ListCurrentSubscriptionsDescending"} {
				statement, ok := rendered[name]
				must.True(t, ok, must.Sprintf("%s was not emitted", name))

				test.StrContains(t, statement, CurrentAsOfArg,
					test.Sprintf("%s does not bind its horizon", name))
			}
		})
	}
}

// TestTables_ColumnsAreDeclaredOnce is the drift check between the four column
// lists and the DDL they project.
//
// Every table here must carry the scope, the id and the three timestamp columns,
// because every statement this package emits derives a predicate from one of
// them.
func TestTables_ColumnsAreDeclaredOnce(t *testing.T) {
	t.Parallel()

	required := []string{
		querygen.IDColumn,
		ScopeColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	}

	for _, table := range Emitted {
		for _, column := range required {
			test.True(t, slices.Contains(table.Columns, column),
				test.Sprintf("%s does not declare %s", table.Name, column))
		}

		test.EqOp(t, len(table.Columns), len(slices.Compact(slices.Sorted(slices.Values(table.Columns)))),
			test.Sprintf("%s declares a column twice", table.Name))
	}
}

// TestTable_UnarchivedBlindColumns is the shape the four provider-id statements
// are rendered from: no id, no soft delete, everything else.
func TestTable_UnarchivedBlindColumns(t *testing.T) {
	t.Parallel()

	blind := Transactions.UnarchivedBlindColumns()

	test.False(t, slices.Contains(blind, querygen.IDColumn))
	test.False(t, slices.Contains(blind, querygen.ArchivedAtColumn))
	test.True(t, slices.Contains(blind, ExternalTransactionColumn))
	test.EqOp(t, len(Transactions.Columns)-2, len(blind))
}

// TestTable_UpdateColumns is the other half of Updatable: what a table does not
// let the standard update assign.
func TestTable_UpdateColumns(t *testing.T) {
	t.Parallel()

	// The account is immutable by omission from Updatable, which is what stops
	// one customer's payments settling another's bill.
	test.False(t, slices.Contains(Subscriptions.UpdateColumns(), AccountColumn))
	test.True(t, slices.Contains(Subscriptions.UpdateColumns(), StatusColumn))

	// The two record tables assign nothing at all through a standard update.
	test.SliceEmpty(t, Purchases.UpdateColumns())
	test.SliceEmpty(t, Transactions.UpdateColumns())
}

// statements splits one dialect's rendered corpus into its named statements.
func statements(tb testing.TB, d dialect.Dialect) map[string]string {
	tb.Helper()

	rendered := map[string]string{}

	for block := range strings.SplitSeq(Render(d), "-- name: ") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}

		name, _, found := strings.Cut(block, " ")
		must.True(tb, found, must.Sprintf("unparsable statement block %q", block))

		rendered[name] = block
	}

	return rendered
}
