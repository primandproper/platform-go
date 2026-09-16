package schemaconvention_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	auditmigrations "github.com/primandproper/platform-go/v14/audit/migrations"
	oauth2clientsmigrations "github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"
	oauth2serverstoremigrations "github.com/primandproper/platform-go/v14/authentication/oauth2serverstore/migrations"
	passwordresetmigrations "github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"
	webauthnmigrations "github.com/primandproper/platform-go/v14/authentication/webauthnsessions/migrations"
	billingmigrations "github.com/primandproper/platform-go/v14/billing/migrations"
	commentsmigrations "github.com/primandproper/platform-go/v14/comments/migrations"
	dataprivacymigrations "github.com/primandproper/platform-go/v14/dataprivacy/migrations"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"
	issuereportsmigrations "github.com/primandproper/platform-go/v14/issuereports/migrations"
	linksmigrations "github.com/primandproper/platform-go/v14/links/database/migrations"
	mediaregistrymigrations "github.com/primandproper/platform-go/v14/mediaregistry/migrations"
	meteringmigrations "github.com/primandproper/platform-go/v14/metering/migrations"
	notificationsmigrations "github.com/primandproper/platform-go/v14/notifications/migrations"
	operationsmigrations "github.com/primandproper/platform-go/v14/operations/migrations"
	outboxmigrations "github.com/primandproper/platform-go/v14/outbox/migrations"
	rbacmigrations "github.com/primandproper/platform-go/v14/rbac/migrations"
	sagamigrations "github.com/primandproper/platform-go/v14/saga/migrations"
	sessionsmigrations "github.com/primandproper/platform-go/v14/sessions/database/migrations"
	settingsmigrations "github.com/primandproper/platform-go/v14/settings/migrations"
	shreddingmigrations "github.com/primandproper/platform-go/v14/shredding/migrations"
	timersmigrations "github.com/primandproper/platform-go/v14/timers/migrations"
	waitlistsmigrations "github.com/primandproper/platform-go/v14/waitlists/migrations"
	webhooksmigrations "github.com/primandproper/platform-go/v14/webhooks/migrations"
	workqueuemigrations "github.com/primandproper/platform-go/v14/workqueue/migrations"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// renderer is the one function every schema-shipping package exposes: the DDL
// for a dialect, split into statements. A package that does not claim a dialect
// answers dialect.ErrUnsupported, which is how this test discovers which
// dialects a table has to satisfy the convention in rather than being told.
type renderer func(dialect.Dialect, string) ([]string, error)

// renderers is every schema-shipping package in the module, keyed by the
// directory it sits in relative to the module root — the package a reader would
// go to, not the migrations subdirectory under it.
//
// The key is a path rather than a nickname because the roster is checked against
// a walk of the tree in both directions below, and a path is the half of the
// entry the walk can find. A Go function value is not something a walk can
// produce, so this is the shape protoconvention's roster has for the same
// reason: the enumeration is written down, and nothing in it is taken on trust.
//
// Every table the module ships is reached through this map, which is why an
// absent entry was never a failing test. The list ran eight packages short for
// long enough that action_links, password_reset_tokens and the four billing
// tables were classified by nobody at all.
var renderers = map[string]renderer{
	"audit":                            auditmigrations.Statements,
	"authentication/oauth2clients":     oauth2clientsmigrations.Statements,
	"authentication/oauth2serverstore": oauth2serverstoremigrations.Statements,
	"authentication/passwordreset":     passwordresetmigrations.Statements,
	"authentication/webauthnsessions":  webauthnmigrations.Statements,
	"billing":                          billingmigrations.Statements,
	"comments":                         commentsmigrations.Statements,
	"dataprivacy":                      dataprivacymigrations.Statements,
	"identity":                         identitymigrations.Statements,
	"issuereports":                     issuereportsmigrations.Statements,
	"links/database":                   linksmigrations.Statements,
	"mediaregistry":                    mediaregistrymigrations.Statements,
	"metering":                         meteringmigrations.Statements,
	"notifications":                    notificationsmigrations.Statements,
	"operations":                       operationsmigrations.Statements,
	"outbox":                           outboxmigrations.Statements,
	"rbac":                             rbacmigrations.Statements,
	"saga":                             sagamigrations.Statements,
	"sessions/database":                sessionsmigrations.Statements,
	"settings":                         settingsmigrations.Statements,
	"shredding":                        shreddingmigrations.Statements,
	"timers":                           timersmigrations.Statements,
	"waitlists":                        waitlistsmigrations.Statements,
	"webhooks":                         webhooksmigrations.Statements,
	"workqueue":                        workqueuemigrations.Statements,
}

// conventional is every table in the module that stores consumer rows.
//
// A table belongs here or in exempt, and being in neither is the failure this
// test exists to catch: a table nobody classified is a table whose columns
// nobody decided.
var conventional = map[string]renderer{
	"identity_users":            identitymigrations.Statements,
	"issue_reports":             issuereportsmigrations.Statements,
	"comments":                  commentsmigrations.Statements,
	"identity_accounts":         identitymigrations.Statements,
	"identity_memberships":      identitymigrations.Statements,
	"identity_invitations":      identitymigrations.Statements,
	"authz_roles":               rbacmigrations.Statements,
	"authz_permissions":         rbacmigrations.Statements,
	"operations":                operationsmigrations.Statements,
	"webhooks_endpoints":        webhooksmigrations.Statements,
	"webhooks_subscriptions":    webhooksmigrations.Statements,
	"webhooks_deliveries":       webhooksmigrations.Statements,
	"webhooks_dispatches":       webhooksmigrations.Statements,
	"webhooks_attempts":         webhooksmigrations.Statements,
	"shredding_subject_keys":    shreddingmigrations.Statements,
	"dataprivacy_requests":      dataprivacymigrations.Statements,
	"saga_instances":            sagamigrations.Statements,
	"scheduled_timers":          timersmigrations.Statements,
	"metering_totals":           meteringmigrations.Statements,
	"audit_log_chains":          auditmigrations.Statements,
	"notifications_inbox":       notificationsmigrations.Statements,
	"uploads_objects":           mediaregistrymigrations.Statements,
	"billing_products":          billingmigrations.Statements,
	"billing_subscriptions":     billingmigrations.Statements,
	"billing_purchases":         billingmigrations.Statements,
	"billing_transactions":      billingmigrations.Statements,
	"settings_definitions":      settingsmigrations.Statements,
	"settings_values":           settingsmigrations.Statements,
	"waitlists":                 waitlistsmigrations.Statements,
	"waitlist_signups":          waitlistsmigrations.Statements,
	"oauth2_registered_clients": oauth2clientsmigrations.Statements,
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

	// The two bearer-credential tables, which are the sweeper shape with a
	// second reason on top: the row is named by the digest of a secret, nothing
	// lists or filters one, and last_updated_at would be a second copy of the
	// single column that records the one mutation either row has.
	//
	// action_links carries the other half of this package's doc — created_at
	// NOT NULL with no DEFAULT — and is the module's only table that does. It is
	// safe there and nowhere else: the links store assigns the column on the
	// insert rather than leaving it to the server, because a link's creation
	// time is the minter's clock, the same clock expires_at and purge_after are
	// derived from. A row whose three timestamps came from two clocks is a link
	// that expires at the wrong moment.
	"action_links": {linksmigrations.Statements,
		"minted, resolved once and collected by the sweeper on purge_after; created_at is the minter's clock, assigned on the insert"},
	"password_reset_tokens": {passwordresetmigrations.Statements,
		"issued, redeemed once and swept on expires_at; redeemed_at is the only mutation the row has"},

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
	"authz_role_permissions":      {rbacmigrations.Statements, "mapping rows, rewritten wholesale with their role"},
	"authz_role_hierarchy":        {rbacmigrations.Statements, "mapping rows, rewritten wholesale with their role"},
	"identity_user_roles":         {identitymigrations.Statements, "mapping rows, rewritten wholesale with their user"},
	"identity_membership_roles":   {identitymigrations.Statements, "mapping rows, rewritten wholesale with their membership"},
	"identity_invitation_roles":   {identitymigrations.Statements, "mapping rows, rewritten wholesale with their invitation"},
	"settings_definition_options": {settingsmigrations.Statements, "mapping rows, rewritten wholesale with their definition"},

	// The OAuth credential tables, which are the sweeper shape again: a client
	// registration or a hashed credential lapses at its own expires_at and is
	// deleted, so archived_at would keep rows nothing can ever redeem. They are
	// keyed on a hash rather than on an id, and nothing lists or filters one.
	"oauth2_clients": {oauth2serverstoremigrations.Statements,
		"a registration lapses at expires_at and is swept, not archived"},
	"oauth2_authorization_codes": {oauth2serverstoremigrations.Statements,
		"hashed credential, redeemed once and swept at expiry"},
	"oauth2_access_tokens": {oauth2serverstoremigrations.Statements,
		"hashed credential, revoked or swept at expiry"},
	"oauth2_refresh_tokens": {oauth2serverstoremigrations.Statements,
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

// TestTheRosterIsEverySchemaShippingPackage checks the roster against the tree in
// both directions, which is what makes the classification above a claim about
// the module rather than about whichever packages somebody remembered to import.
// A twenty-sixth schema fails here until it is rostered, and an entry for a
// package that has been moved or deleted fails rather than quietly classifying
// tables nothing ships.
func TestTheRosterIsEverySchemaShippingPackage(T *testing.T) {
	T.Parallel()

	found := schemaPackages(T)

	rostered := make([]string, 0, len(renderers))
	for pkg := range renderers {
		rostered = append(rostered, pkg)
	}

	slices.Sort(rostered)

	for pkg := range found {
		test.True(T, slices.Contains(rostered, pkg), test.Sprintf(
			"%s ships a migrations directory and is in no roster entry, so nothing classifies its tables", pkg))
	}

	for _, pkg := range rostered {
		_, ok := found[pkg]
		test.True(T, ok, test.Sprintf("the roster names %s and no such migrations directory ships", pkg))
	}
}

// TestEachEntryRendersItsOwnPackagesTables is what keeps the roster's keys
// honest. The path is what the walk finds and the renderer is what the
// classification reads, and nothing above would notice an entry that paired one
// package's path with another package's DDL — the tables would all still be
// classified, by a roster that had quietly stopped covering two packages and
// started covering one twice.
func TestEachEntryRendersItsOwnPackagesTables(T *testing.T) {
	T.Parallel()

	found := schemaPackages(T)

	for pkg, render := range renderers {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			declared, ok := found[pkg]
			if !ok {
				t.Skipf("%s ships no migrations directory, which TestTheRosterIsEverySchemaShippingPackage reports", pkg)
			}

			rendered := map[string]struct{}{}

			for _, d := range dialectsOf(t, render) {
				stmts, err := render(d, "")
				must.NoError(t, err)

				for _, table := range createdTables(stmts) {
					rendered[table] = struct{}{}
				}
			}

			names := make([]string, 0, len(rendered))
			for table := range rendered {
				names = append(names, table)
			}

			slices.Sort(names)

			test.Eq(t, declared, names, test.Sprintf(
				"%s declares %v in its .sql files and renders %v", pkg, declared, names))
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
// prefix placeholder is consumed rather than captured, so that one pattern reads
// both a rendered statement — which this file renders at the empty prefix — and
// the authored .sql the walk below reads, where every table name carries it.
var createTablePattern = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:\{\{PREFIX\}\})?([a-z0-9_]+)`)

func createdTable(stmt string) string {
	if m := createTablePattern.FindStringSubmatch(stripComments(stmt)); m != nil {
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

// stripComments removes the line comments, so that prose naming a CREATE TABLE
// is not read as one — billing's and oauth2clients' schemas both hold a sentence
// that does. SQL's line comment is spelled the same in all three dialects.
func stripComments(ddl string) string {
	var b strings.Builder

	for line := range strings.SplitSeq(ddl, "\n") {
		if before, _, found := strings.Cut(line, "--"); found {
			line = before
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	return b.String()
}

// walked is every migrations directory in the module, by the package that ships
// it, mapped to the sorted names of the tables its .sql files create. Walked
// once: two tests read it, and the answer is a property of the tree rather than
// of whichever test asked first.
var walked = sync.OnceValues(func() (map[string][]string, error) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return nil, err
	}

	return sweep(root)
})

func schemaPackages(t *testing.T) map[string][]string {
	t.Helper()

	found, err := walked()
	must.NoError(t, err)
	must.MapNotEmpty(t, found)

	return found
}

func sweep(root string) (map[string][]string, error) {
	found := map[string][]string{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if !d.IsDir() {
			return nil
		}

		// Dot directories hold no schema of this module's, and one of them holds
		// other checkouts of it: an agent worktree under .claude would otherwise
		// report every package in the module under a second path.
		if name := d.Name(); path != root && strings.HasPrefix(name, ".") {
			return filepath.SkipDir
		}

		if d.Name() != "migrations" {
			return nil
		}

		tables, err := tablesIn(path)
		if err != nil {
			return err
		}

		pkg, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}

		found[filepath.ToSlash(pkg)] = tables

		// The schema/ subdirectory holds generated mirrors of the files just
		// read, and no migrations directory nests inside another.
		return filepath.SkipDir
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// tablesIn is every table the .sql files directly in one migrations directory
// create, sorted and deduplicated. Deduplication is what lets the answer be
// compared against a renderer's: the DDL is authored once per dialect, so each
// table is declared as many times as the package claims dialects.
func tablesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	tables := []string{}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}

		body, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return nil, readErr
		}

		for _, match := range createTablePattern.FindAllStringSubmatch(stripComments(string(body)), -1) {
			if !slices.Contains(tables, match[1]) {
				tables = append(tables, match[1])
			}
		}
	}

	slices.Sort(tables)

	return tables, nil
}

// indexStatement matches a statement that creates an index as a statement of its
// own, rather than as a key inside the table it belongs to. The UNIQUE is
// optional because CREATE UNIQUE INDEX is the same problem wearing a different
// word.
var indexStatement = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\b`)

// conditional matches the guard that makes a create re-runnable.
var conditional = regexp.MustCompile(`(?i)\bIF\s+NOT\s+EXISTS\b`)

// TestSchemaIsRerunnable is the second convention this package asserts, and it is
// here rather than in twenty-five packages for the reason the triple is: the
// failure cannot be seen from inside one schema.
//
// Rendering a package's DDL twice proves nothing — the statements are identical
// both times, and what is in question is what a server does with the second set.
// So the check is on the shape of the statements, which is where the answer
// actually lives, and the per-package container suites are what confirm the
// reading against a real server.
//
// Fourteen of the module's schemas failed this at once, each of them looking
// locally correct, which is what a convention nobody checks looks like from
// inside any one package.
func TestSchemaIsRerunnable(T *testing.T) {
	T.Parallel()

	for pkg, render := range renderers {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			for _, d := range dialectsOf(t, render) {
				stmts, err := render(d, "")
				must.NoError(t, err)
				must.SliceNotEmpty(t, stmts)

				for _, stmt := range stmts {
					switch {
					case d == dialect.MySQL && indexStatement.MatchString(stmt):
						// MySQL has no CREATE INDEX IF NOT EXISTS, so there is
						// no conditional to add: the key belongs inside the
						// CREATE TABLE IF NOT EXISTS, where it is skipped
						// exactly when the table is. Anything else aborts on
						// the duplicate key name and takes the statements after
						// it down too.
						t.Errorf("%s renders a standalone index on %q, which a second run cannot skip:\n\t%s\n\t"+
							"declare it inline as a KEY under the CREATE TABLE it belongs to", pkg, d, stmt)
					case createdTable(stmt) != "" || indexStatement.MatchString(stmt):
						test.RegexMatch(t, conditional, stmt, test.Sprintf(
							"%s renders an unconditional create on %q, which fails on a second run:\n\t%s",
							pkg, d, stmt))
					}
				}
			}
		})
	}
}
