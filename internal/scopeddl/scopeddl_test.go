package scopeddl_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// column is one tenancy column in one shipped table, and where it was found.
type column struct {
	// pkg is the directory holding the migrations, relative to the module root:
	// the package a reader would go to, not the migrations subdirectory.
	pkg string

	// file is the .sql it was declared in, relative to the module root. Each
	// column is declared once per dialect and again in the generated mirror, and
	// a failure names the one file to open.
	file string

	// table is the table's canonical name, with the prefix placeholder removed.
	table string

	// name is the column's, and definition is everything the DDL says about it
	// after the name.
	name       string
	definition string
}

// qualified is how a column is recorded below, and how a failure names it.
func (c *column) qualified() string { return c.table + "." + c.name }

// tenancyColumns is every tenancy column this module's schemas ship, by the
// package that ships it. The package doc says why the list is written down
// rather than derived: a claim about a set is checkable only against an
// enumeration of it, and a sweep that found nothing would otherwise pass.
//
// A schema that grows a scope column belongs here. Nothing about the entry is a
// decision — the column either exists or it does not — so unlike
// internal/sqltier's rulings, none of them carries a reason.
var tenancyColumns = map[string][]string{
	"audit":                        {"audit_log_chains.scope", "audit_log_entries.scope"},
	"authentication/oauth2clients": {"oauth2_registered_clients.scope"},
	"authentication/passwordreset": {"password_reset_tokens.scope"},
	"dataprivacy":                  {"dataprivacy_requests.subject_scope"},
	"identity": {
		"identity_accounts.scope",
		"identity_invitations.scope",
		"identity_memberships.scope",
		"identity_users.scope",
	},
	"billing": {
		"billing_products.scope",
		"billing_purchases.scope",
		"billing_subscriptions.scope",
		"billing_transactions.scope",
	},
	"comments":          {"comments.scope"},
	"issuereports":      {"issue_reports.scope"},
	"notifications":     {"notifications_devices.scope", "notifications_inbox.scope"},
	"operations":        {"operations.scope"},
	"sessions/database": {"sessions.scope"},
	"settings":          {"settings_definitions.scope", "settings_values.scope"},
	"mediaregistry":     {"uploads_objects.scope"},
	"waitlists":         {"waitlist_signups.scope", "waitlists.scope"},
	"webhooks":          {"webhooks_deliveries.scope", "webhooks_endpoints.scope"},
}

// TestNoScopeColumnCarriesADefault is the clause this package exists to keep.
// The empty string is tenancy.Global() rather than the absence of a scope, so a
// default files the write that forgot the column in the tenant that matches
// nobody instead of failing it.
func TestNoScopeColumnCarriesADefault(T *testing.T) {
	T.Parallel()

	columns := scopeColumns(T)

	for i := range columns {
		c := &columns[i]

		T.Run(c.file+"/"+c.qualified(), func(t *testing.T) {
			t.Parallel()

			test.False(t, hasDefault.MatchString(c.definition),
				test.Sprintf("%s in %s is %q; the empty string is tenancy.Global(), so a default hands the global scope to a write that omitted the column",
					c.qualified(), c.file, strings.TrimSpace(c.name+" "+c.definition)))
		})
	}
}

// TestEveryScopeColumnIsRecorded is what stops the sweep above from passing
// because it found nothing. A schema that grows a tenancy column is one nobody
// has looked at yet, and it lands here before it lands in a release.
func TestEveryScopeColumnIsRecorded(T *testing.T) {
	T.Parallel()

	for pkg, found := range foundByPackage(T) {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			recorded, ok := tenancyColumns[pkg]
			must.True(t, ok, must.Sprintf("%s ships tenancy columns %v and is not recorded in this package", pkg, found))
			test.Eq(t, recorded, found)
		})
	}
}

// TestNoRecordedColumnHasVanished is the other direction. A renamed or deleted
// column would otherwise leave an entry that goes on reading true while the
// sweep it stands for covers nothing.
func TestNoRecordedColumnHasVanished(T *testing.T) {
	T.Parallel()

	found := foundByPackage(T)

	for pkg, recorded := range tenancyColumns {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			columns, ok := found[pkg]
			must.True(t, ok, must.Sprintf("%s is recorded as shipping %v and ships no tenancy column at all", pkg, recorded))
			test.Eq(t, recorded, columns)
		})
	}
}

// hasDefault matches the clause the rule forbids. Case-insensitive because the
// rule is about what the engine reads rather than about how this module spells
// it, and word-bounded so a column named default_value is not one.
var hasDefault = regexp.MustCompile(`(?i)\bDEFAULT\b`)

// tenancyColumnName reports whether a column name is a scope. Suffix rather
// than substring: subject_scope is whose request it is, and OAuth's scopes is a
// list of permissions that has nothing to do with tenancy.
func tenancyColumnName(name string) bool {
	return name == "scope" || strings.HasSuffix(name, "_scope")
}

// swept is every tenancy column in the module's shipped DDL. Walked once: three
// tests read it, and the answer is a property of the tree rather than of
// whichever test asked first.
var swept = sync.OnceValues(func() ([]column, error) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return nil, err
	}

	return sweep(root)
})

func scopeColumns(t *testing.T) []column {
	t.Helper()

	found, err := swept()
	must.NoError(t, err)
	must.SliceNotEmpty(t, found)

	return found
}

// foundByPackage is the sweep collapsed to what the enumeration records: one
// sorted, deduplicated list of qualified names per package. Deduplication is
// what makes the two sources agree — a package's DDL is written once per dialect
// and mirrored again under schema/, so every column is found five or six times.
func foundByPackage(t *testing.T) map[string][]string {
	t.Helper()

	byPackage := map[string][]string{}
	columns := scopeColumns(t)

	for i := range columns {
		c := &columns[i]
		if qualified := c.qualified(); !slices.Contains(byPackage[c.pkg], qualified) {
			byPackage[c.pkg] = append(byPackage[c.pkg], qualified)
		}
	}

	for pkg := range byPackage {
		slices.Sort(byPackage[pkg])
	}

	return byPackage
}

func sweep(root string) ([]column, error) {
	var found []column

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			// Dot directories hold no schema of this module's, and one of them
			// holds other checkouts of it: an agent worktree under .claude would
			// otherwise report every column in the module twice.
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "artifacts" || name == "testdata") {
				return filepath.SkipDir
			}

			return nil
		}

		// Under a migrations directory is where this module's DDL lives; the
		// .sql files elsewhere are rendered statement corpora, which create no
		// table and so declare no column.
		if !strings.HasSuffix(path, ".sql") || !strings.Contains(filepath.ToSlash(path), "/migrations/") {
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		pkg, err := filepath.Rel(root, migrationsOwner(filepath.Dir(path)))
		if err != nil {
			return err
		}

		file, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		found = append(found, columnsIn(filepath.ToSlash(pkg), filepath.ToSlash(file), string(body))...)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// migrationsOwner walks up from the directory holding a .sql file to the package
// that ships it — past schema/, which holds the generated mirrors, and past
// migrations/ itself.
func migrationsOwner(dir string) string {
	for filepath.Base(dir) != "migrations" {
		dir = filepath.Dir(dir)
	}

	return filepath.Dir(dir)
}

// createTable matches a table declaration's opening. The body is taken by
// matching parentheses from there rather than by another expression, because the
// column list contains parentheses of its own — VARCHAR(255), an inline index —
// and a regex that stopped at the first ')' would read a MySQL table as four
// columns.
var createTable = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\S+)\s*\(`)

// columnsIn reads one dialect's DDL and returns its tenancy columns.
func columnsIn(pkg, file, ddl string) []column {
	ddl = stripComments(ddl)

	var found []column

	for _, match := range createTable.FindAllStringSubmatchIndex(ddl, -1) {
		table := strings.TrimPrefix(ddl[match[2]:match[3]], "{{PREFIX}}")

		body, ok := parenthesized(ddl[match[1]-1:])
		if !ok {
			continue
		}

		for _, definition := range topLevelCommas(body) {
			name, rest, split := strings.Cut(strings.TrimSpace(definition), " ")
			if !split || !tenancyColumnName(name) {
				continue
			}

			found = append(found, column{
				pkg:        pkg,
				file:       file,
				table:      table,
				name:       name,
				definition: strings.TrimSpace(rest),
			})
		}
	}

	return found
}

// stripComments removes the line comments, so that prose about a DEFAULT is not
// read as one. SQL's line comment is spelled the same in all three dialects.
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

// parenthesized returns what the leading '(' of s encloses.
func parenthesized(s string) (string, bool) {
	depth := 0

	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i], true
			}
		}
	}

	return "", false
}

// topLevelCommas splits a column list on the commas that separate its entries,
// leaving alone the ones inside a type's width or an inline index's column list.
func topLevelCommas(body string) []string {
	var (
		parts []string
		depth int
		start int
	)

	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}

	return append(parts, body[start:])
}
