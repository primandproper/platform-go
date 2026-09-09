package schemaconvention_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	auditmigrations "github.com/primandproper/platform-go/v14/audit/migrations"
	oauth2migrations "github.com/primandproper/platform-go/v14/authentication/oauth2server/database/migrations"
	webauthnmigrations "github.com/primandproper/platform-go/v14/authentication/webauthn/database/migrations"
	authzmigrations "github.com/primandproper/platform-go/v14/authorization/database/migrations"
	commentsmigrations "github.com/primandproper/platform-go/v14/comments/migrations"
	shreddingmigrations "github.com/primandproper/platform-go/v14/cryptography/shredding/migrations"
	dataprivacymigrations "github.com/primandproper/platform-go/v14/dataprivacy/migrations"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"
	issuereportsmigrations "github.com/primandproper/platform-go/v14/issuereports/migrations"
	meteringmigrations "github.com/primandproper/platform-go/v14/metering/migrations"
	notificationsmigrations "github.com/primandproper/platform-go/v14/notifications/migrations"
	operationsmigrations "github.com/primandproper/platform-go/v14/operations/migrations"
	outboxmigrations "github.com/primandproper/platform-go/v14/outbox/migrations"
	sagamigrations "github.com/primandproper/platform-go/v14/saga/migrations"
	sessionsmigrations "github.com/primandproper/platform-go/v14/sessions/database/migrations"
	timersmigrations "github.com/primandproper/platform-go/v14/timers/migrations"
	uploadsregistrymigrations "github.com/primandproper/platform-go/v14/uploads/registry/migrations"
	webhooksmigrations "github.com/primandproper/platform-go/v14/webhooks/migrations"
	workqueuemigrations "github.com/primandproper/platform-go/v14/workqueue/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// renderer is the one function every schema-shipping package exposes: the DDL
// for a dialect, split into statements. A package that does not claim a dialect
// answers dialect.ErrUnsupported, which is how this test discovers which
// dialects a table has to satisfy the convention in rather than being told.
type renderer func(dialect.Dialect, string) ([]string, error)

// conventional is every table in the module that stores consumer rows.
//
// A table belongs here or in exempt, and being in neither is the failure this
// test exists to catch: a table nobody classified is a table whose columns
// nobody decided.
var conventional = map[string]renderer{
	"identity_users":         identitymigrations.Statements,
	"issue_reports":          issuereportsmigrations.Statements,
	"comments":               commentsmigrations.Statements,
	"identity_accounts":      identitymigrations.Statements,
	"identity_memberships":   identitymigrations.Statements,
	"identity_invitations":   identitymigrations.Statements,
	"authz_roles":            authzmigrations.Statements,
	"authz_permissions":      authzmigrations.Statements,
	"operations":             operationsmigrations.Statements,
	"webhooks_endpoints":     webhooksmigrations.Statements,
	"webhooks_subscriptions": webhooksmigrations.Statements,
	"webhooks_deliveries":    webhooksmigrations.Statements,
	"webhooks_dispatches":    webhooksmigrations.Statements,
	"webhooks_attempts":      webhooksmigrations.Statements,
	"shredding_subject_keys": shreddingmigrations.Statements,
	"dataprivacy_requests":   dataprivacymigrations.Statements,
	"saga_instances":         sagamigrations.Statements,
	"scheduled_timers":       timersmigrations.Statements,
	"metering_totals":        meteringmigrations.Statements,
	"audit_log_chains":       auditmigrations.Statements,
	"notifications_inbox":    notificationsmigrations.Statements,
	"uploads_objects":        uploadsregistrymigrations.Statements,
}

// exemption is a table that deliberately carries none of the triple, and the
// reason it does not. The reason is a field rather than a comment because it is
// the half of the entry that has to survive: a table listed here without one is
// a table somebody exempted to make a test pass.
type exemption struct {
	render renderer
	why    string
}

var exempt = map[string]exemption{
	// Swept, not archived. A soft delete on a table a sweeper keeps small either
	// does nothing or keeps it growing forever.
	"sessions": {sessionsmigrations.Statements,
		"swept on expiry; last_seen_at is a liveness signal, not a last mutation"},
	"work_queue_items": {workqueuemigrations.Statements,
		"swept once complete; enqueued_at and available_at are the schedule, not the row's history"},
	"outbox_messages": {outboxmigrations.Statements,
		"swept once published; next_attempt, claimed_until and published_at already say what a write meant"},
	"webauthn_sessions": {webauthnmigrations.Statements,
		"ceremony state, written once and consumed once, then swept"},
	"metering_events": {meteringmigrations.Statements,
		"the ingest ledger: written once, never updated, reaped by recorded_at on a retention window"},
	"notifications_devices": {notificationsmigrations.Statements,
		"a device token is revoked by its owner or invalidated by the provider and deleted; last_seen_at is when the handset announced itself, not a last mutation"},

	// audit_log_entries is exempt for three reasons, the first fatal. recorded_at
	// is folded into every entry's hash before the INSERT, so a database-assigned
	// creation stamp would store a value the hash does not cover and every entry
	// would read as tampered. It is caller-assignable by design rather than a
	// creation time. And the table is append-only by trigger, so last_updated_at
	// and archived_at would be columns no statement can write.
	"audit_log_entries": {auditmigrations.Statements,
		"recorded_at is hashed and caller-assigned; the table is append-only by trigger"},

	// Mapping rows. Nothing lists, filters or soft-deletes one independently of
	// its parents, and archiving a parent already hides them.
	"authz_role_permissions":    {authzmigrations.Statements, "mapping rows, rewritten wholesale with their role"},
	"authz_role_hierarchy":      {authzmigrations.Statements, "mapping rows, rewritten wholesale with their role"},
	"identity_user_roles":       {identitymigrations.Statements, "mapping rows, rewritten wholesale with their user"},
	"identity_membership_roles": {identitymigrations.Statements, "mapping rows, rewritten wholesale with their membership"},
	"identity_invitation_roles": {identitymigrations.Statements, "mapping rows, rewritten wholesale with their invitation"},

	// The OAuth credential tables, which are the sweeper shape again: a client
	// registration or a hashed credential lapses at its own expires_at and is
	// deleted, so archived_at would keep rows nothing can ever redeem. They are
	// keyed on a hash rather than on an id, and nothing lists or filters one.
	"oauth2_clients": {oauth2migrations.Statements,
		"a registration lapses at expires_at and is swept, not archived"},
	"oauth2_authorization_codes": {oauth2migrations.Statements,
		"hashed credential, redeemed once and swept at expiry"},
	"oauth2_access_tokens": {oauth2migrations.Statements,
		"hashed credential, revoked or swept at expiry"},
	"oauth2_refresh_tokens": {oauth2migrations.Statements,
		"hashed credential, revoked or swept at expiry"},
}

var allDialects = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// createdAtDefault is what a conventional created_at has to declare. The default
// is the load-bearing half: querygen's create runs its columns through ForInsert,
// which drops created_at as database-owned, so a column without one generates,
// compiles, and dies on a not-null violation the first time it runs.
var createdAtDefault = regexp.MustCompile(`(?i)\bcreated_at\s+\S+(\(\d+\))?\s+NOT NULL DEFAULT\s+\S`)

func TestConventionTriple(T *testing.T) {
	T.Parallel()

	for table, render := range conventional {
		T.Run(table, func(t *testing.T) {
			t.Parallel()

			for _, d := range dialectsOf(t, render) {
				create := createStatement(t, render, d, table)

				test.RegexMatch(t, createdAtDefault, create,
					test.Sprintf("%s in %q wants created_at NOT NULL with a dialect-appropriate DEFAULT", table, d))
				test.StrContains(t, create, querygen.LastUpdatedAtColumn,
					test.Sprintf("%s in %q wants %s", table, d, querygen.LastUpdatedAtColumn))
				test.StrContains(t, create, querygen.ArchivedAtColumn,
					test.Sprintf("%s in %q wants %s", table, d, querygen.ArchivedAtColumn))
			}
		})
	}
}

// TestNoSecondSpelling is the half of the convention a per-package test cannot
// see. last_updated_at and updated_at are both plausible names for one concept,
// and the module held twelve of the first against fourteen of the second — close
// enough to even that neither read as the exception.
func TestNoSecondSpelling(T *testing.T) {
	T.Parallel()

	for table, render := range conventional {
		T.Run(table, func(t *testing.T) {
			t.Parallel()

			for _, d := range dialectsOf(t, render) {
				create := createStatement(t, render, d, table)

				test.StrNotContains(t, strings.ReplaceAll(create, querygen.LastUpdatedAtColumn, ""), "updated_at",
					test.Sprintf("%s in %q spells its last-mutation column twice", table, d))
			}
		})
	}
}

// TestExemptTablesStayExempt is what keeps an exemption from being a place to
// put a table somebody has not thought about. Each exempt table must still carry
// none of the two columns the convention adds — a sweeper's table that grew an
// archived_at has stopped being swept, and nobody would notice from its own
// package's tests.
func TestExemptTablesStayExempt(T *testing.T) {
	T.Parallel()

	for table, e := range exempt {
		T.Run(table, func(t *testing.T) {
			t.Parallel()

			test.NotEqOp(t, "", e.why, test.Sprintf("%s is exempt without a reason", table))

			if e.render == nil {
				return
			}

			for _, d := range dialectsOf(t, e.render) {
				create := createStatement(t, e.render, d, table)

				test.StrNotContains(t, create, querygen.LastUpdatedAtColumn,
					test.Sprintf("%s in %q is exempt because %s", table, d, e.why))
				test.StrNotContains(t, create, querygen.ArchivedAtColumn,
					test.Sprintf("%s in %q is exempt because %s", table, d, e.why))
			}
		})
	}
}

// TestEveryTableIsClassified is the entry this file exists to make impossible to
// forget. A table added to any schema in the module lands in neither map until
// somebody puts it in one, and the decision it needs is which.
func TestEveryTableIsClassified(T *testing.T) {
	T.Parallel()

	renderers := map[string]renderer{
		"audit":         auditmigrations.Statements,
		"authz":         authzmigrations.Statements,
		"dataprivacy":   dataprivacymigrations.Statements,
		"identity":      identitymigrations.Statements,
		"comments":      commentsmigrations.Statements,
		"issuereports":  issuereportsmigrations.Statements,
		"metering":      meteringmigrations.Statements,
		"notifications": notificationsmigrations.Statements,
		"operations":    operationsmigrations.Statements,
		"outbox":        outboxmigrations.Statements,
		"saga":          sagamigrations.Statements,
		"sessions":      sessionsmigrations.Statements,
		"shredding":     shreddingmigrations.Statements,
		"timers":        timersmigrations.Statements,
		"webauthn":      webauthnmigrations.Statements,
		"webhooks":      webhooksmigrations.Statements,
		"workqueue":     workqueuemigrations.Statements,
	}

	for pkg, render := range renderers {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			for _, d := range dialectsOf(t, render) {
				stmts, err := render(d, "")
				must.NoError(t, err)

				for _, table := range createdTables(stmts) {
					_, isConventional := conventional[table]
					_, isExempt := exempt[table]

					test.True(t, isConventional != isExempt,
						test.Sprintf("%s in %q belongs in exactly one of conventional and exempt", table, d))
				}
			}
		})
	}
}

// dialectsOf is the set of dialects a package claims, discovered by asking.
func dialectsOf(t *testing.T, render renderer) []dialect.Dialect {
	t.Helper()

	claimed := make([]dialect.Dialect, 0, len(allDialects))

	for _, d := range allDialects {
		switch _, err := render(d, ""); {
		case err == nil:
			claimed = append(claimed, d)
		case errors.Is(err, dialect.ErrUnsupported):
		default:
			must.NoError(t, err, must.Sprintf("dialect %q", d))
		}
	}

	must.SliceNotEmpty(t, claimed)

	return claimed
}

// createStatement returns the CREATE TABLE for one table, at the empty prefix.
func createStatement(t *testing.T, render renderer, d dialect.Dialect, table string) string {
	t.Helper()

	stmts, err := render(d, "")
	must.NoError(t, err)

	for _, stmt := range stmts {
		if createdTable(stmt) == table {
			return stmt
		}
	}

	t.Fatalf("dialect %q renders no CREATE TABLE for %q", d, table)

	return ""
}

// createTablePattern captures the name a CREATE TABLE statement creates. The
// prefix is empty here, so the name is the table's own.
var createTablePattern = regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z0-9_]+)`)

func createdTable(stmt string) string {
	if m := createTablePattern.FindStringSubmatch(stmt); m != nil {
		return m[1]
	}

	return ""
}

func createdTables(stmts []string) []string {
	tables := make([]string, 0, len(stmts))

	for _, stmt := range stmts {
		if table := createdTable(stmt); table != "" {
			tables = append(tables, table)
		}
	}

	return tables
}
