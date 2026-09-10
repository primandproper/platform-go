package queries

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// statementName finds each statement's sqlc annotation.
var statementName = regexp.MustCompile(`(?m)^-- name: (\S+) `)

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// Neither table here takes querygen.Generator.StandardCRUD — which is what
// registers a table elsewhere in this module — so without the explicit
// registration a consumer reading the registry back to truncate a database
// between integration tests would find neither, and the symptom would be a
// different test failing later on rows the previous one left behind.
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
// halves of "what tables does notifications own": the canonical spelling here,
// which the registry and the store's prefix rendering both read, and the list
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

// TestRender_EmitsTheStatementsTheStoreExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the store does not run.
//
// Two of the single-row reads answer a write rather than a consumer.
// GetArchivedNotification is the archive's read-back and is the one statement
// here that asks for an archived row; GetDevice is the revocation's, and it runs
// before its write because the row will not be there afterwards.
func TestRender_EmitsTheStatementsTheStoreExecutes(T *testing.T) {
	T.Parallel()

	// Every paged read appears twice, under its name and that name plus
	// Descending: a sort direction is which way the ORDER BY runs and which way
	// the cursor comparison points, so it is answered by a second statement
	// rather than by a bound argument. ListDevicesByPrincipals is the one list
	// with a single entry, because it takes no filter to carry a direction.
	want := []string{
		"CreateNotification", "MarkNotificationRead", "MarkAllNotificationsRead", "ArchiveNotification",
		"GetNotification", "GetArchivedNotification",
		"ListNotifications", "ListNotificationsDescending",
		"ListUnreadNotifications", "ListUnreadNotificationsDescending",
		"RegisterDevice", "RevokeDevice", "DeleteDeviceToken",
		"GetDevice", "GetDeviceByToken", "ListDevices", "ListDevicesDescending", "ListDevicesByPrincipals",
	}

	slices.Sort(want)

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			got := statementNames(Render(d))
			slices.Sort(got)

			test.Eq(t, want, got)
		})
	}
}

// TestRender_ScopesEveryStatementButTheProviderHook is the tenancy doctrine
// asserted over the text rather than over the store.
//
// One statement in this corpus omits the scope, and it is the hook a provider's
// rejection reaches: what APNs and FCM hand back is a token, never a tenant, so
// a scoped variant would need the caller to know the answer the hook exists to
// supply. Everything a consumer calls names the scope, and this is what keeps a
// second exception from being added without a decision.
func TestRender_ScopesEveryStatementButTheProviderHook(T *testing.T) {
	T.Parallel()

	const unscoped = "DeleteDeviceToken"

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, body := range statements(Render(d)) {
				if name == unscoped {
					test.StrNotContains(t, body, ScopeColumn,
						test.Sprintf("%s is the deliberately unscoped statement", name))

					continue
				}

				// The writes that key on nothing — the insert, and the upsert's
				// insert half — carry the scope as a column they store rather than
				// as a predicate. Everything with a WHERE filters on it.
				test.StrContains(t, body, ScopeColumn,
					test.Sprintf("%s does not name the scope", name))

				if strings.Contains(body, "WHERE") {
					test.StrContains(t, body, ScopeColumn+" =",
						test.Sprintf("%s does not filter on the scope", name))
				}
			}
		})
	}
}

// TestRender_KeysEveryInboxReadOnThePrincipal is the other half of that
// doctrine, and the half only this schema has.
//
// A notification is addressed to one person inside a scope, so a read keyed on
// the scope alone would let any member of a tenant read any other member's inbox
// by id. Every inbox statement names the principal; the create names it because
// it stores it.
func TestRender_KeysEveryInboxReadOnThePrincipal(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, body := range statements(Render(d)) {
				if !strings.Contains(body, InboxTable) {
					continue
				}

				test.StrContains(t, body, PrincipalColumn,
					test.Sprintf("%s does not name the principal", name))
			}
		})
	}
}

// TestRender_GuardsTheReadStamp pins the predicate that makes marking read
// idempotent without moving the stamp. Losing it turns every list refresh into a
// write that says the notification was read just now.
func TestRender_GuardsTheReadStamp(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range []string{"MarkNotificationRead", "MarkAllNotificationsRead"} {
				body := statements(Render(d))[name]
				must.NotEq(t, "", body)

				test.StrContains(t, body, ReadAtColumn+" IS NULL",
					test.Sprintf("%s does not guard on the row being unread", name))
			}
		})
	}
}

// TestRender_RegistrationConvergesOnTheToken pins the conflict target. A key
// that included the principal would leave a handset that changed hands with two
// rows, and the previous owner's notifications arriving on the new owner's
// phone.
func TestRender_RegistrationConvergesOnTheToken(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			body := statements(Render(d))["RegisterDevice"]
			must.NotEq(t, "", body)

			// MySQL names no conflict target at all, so what is asserted there is
			// the converging verb rather than the columns.
			if d == dialect.MySQL {
				test.StrContains(t, body, "ON DUPLICATE KEY UPDATE")
			} else {
				test.StrContains(t, body, "ON CONFLICT ("+PlatformColumn+", "+TokenColumn+")")
			}

			test.StrContains(t, body, PrincipalColumn+" =",
				test.Sprintf("a re-registration must move the row to whoever is registering it"))
		})
	}
}

// TestDevicesCarryNoSoftDelete is the schema decision read back off the
// statements it produces.
//
// The registry's column list has no archived_at, so querygen emits no archive
// and renders no archived predicate. A column added there later would silently
// grow both, and the first symptom would be a dead token that a delivery path
// still finds.
func TestDevicesCarryNoSoftDelete(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for name, body := range statements(Render(d)) {
				if !strings.Contains(body, DevicesTable) {
					continue
				}

				test.StrNotContains(t, body, querygen.ArchivedAtColumn,
					test.Sprintf("%s carries a soft delete the registry does not have", name))
			}
		})
	}
}

func TestTable_ColumnsExcept(t *testing.T) {
	t.Parallel()

	// Leaving the id out is how a statement says it keys on something else, so
	// the order of what remains has to be the projection's — the generated
	// params struct follows it.
	test.Eq(t,
		[]string{ScopeColumn, PrincipalColumn, PlatformColumn, TokenColumn, LastSeenAtColumn, querygen.CreatedAtColumn},
		Devices.ColumnsExcept(querygen.IDColumn))

	test.Eq(t, Devices.Columns, Devices.ColumnsExcept())
}

func TestTable_InsertColumns(t *testing.T) {
	t.Parallel()

	// created_at is the database's, which is why the schema gives it a DEFAULT.
	test.SliceNotContains(t, Inbox.InsertColumns(), querygen.CreatedAtColumn)
	test.SliceNotContains(t, Inbox.InsertColumns(), querygen.LastUpdatedAtColumn)
	test.SliceNotContains(t, Inbox.InsertColumns(), querygen.ArchivedAtColumn)
	test.SliceContains(t, Inbox.InsertColumns(), ReadAtColumn)
}

func TestFileName(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "postgres_generated.sql", FileName(dialect.Postgres))
}

// statements splits a rendered corpus into its named statements.
func statements(rendered string) map[string]string {
	out := map[string]string{}

	matches := statementName.FindAllStringSubmatchIndex(rendered, -1)
	for i, m := range matches {
		end := len(rendered)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}

		out[rendered[m[2]:m[3]]] = rendered[m[0]:end]
	}

	return out
}

// statementNames is the names alone, for the set assertion.
func statementNames(rendered string) []string {
	names := make([]string, 0, len(statements(rendered)))
	for name := range statements(rendered) {
		names = append(names, name)
	}

	return names
}
