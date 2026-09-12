package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The tests below are all the same shape, and it is the shape the acceptance of
// this command rests on: build a tree, generate, mutate the tree, generate
// again, and read what moved. Nothing in this file parses the module's own
// README or asserts what it currently says — the claim being made is that the
// file follows the tree, and a fixture that has to be edited alongside the tree
// would be the hand-maintained roster this command replaced.
//
// prose_test.go does read it, for the three things about the "Transports"
// section that are mechanical rather than argued, and says there why that is
// not the same bargain.

// tree is a throwaway module tree that generate can be pointed at.
type tree struct {
	t    *testing.T
	root string
}

func newTree(t *testing.T) *tree {
	t.Helper()

	tr := &tree{t: t, root: t.TempDir()}
	tr.write("README.md", readmeTemplate)

	return tr
}

// readmeTemplate is the least a README can be and still have somewhere to put
// the three regions.
const readmeTemplate = `# fixture

## Stores and Transports

<!-- readmegen:transports -->
<!-- /readmegen:transports -->

## SQL Dialect Support

<!-- readmegen:dialects -->
<!-- /readmegen:dialects -->

### Why the three narrow

<!-- readmegen:narrowings -->
<!-- /readmegen:narrowings -->
`

func (tr *tree) write(rel, body string) {
	tr.t.Helper()

	path := filepath.Join(tr.root, filepath.FromSlash(rel))
	must.NoError(tr.t, os.MkdirAll(filepath.Dir(path), 0o750))
	must.NoError(tr.t, os.WriteFile(path, []byte(body), 0o600))
}

// pkg writes a doc.go for a package, carrying whichever directives it was given.
func (tr *tree) pkg(path string, directives ...string) {
	tr.t.Helper()

	body := "// Package " + filepath.Base(path) + " is a fixture.\npackage " + filepath.Base(path) + "\n"
	for _, d := range directives {
		body += "\n" + d + "\n"
	}

	tr.write(path+"/doc.go", body)
}

// migrations gives a package the DDL that makes it a row of the matrix.
func (tr *tree) migrations(pkg string, ddl ...string) {
	tr.t.Helper()

	for _, d := range ddl {
		tr.write(pkg+"/migrations/"+d+".sql", "-- fixture\n")
	}
}

// querier gives a package the generated files the matrix is cross-checked
// against, under the internal subpackage a real one lives in.
func (tr *tree) querier(pkg string, engines ...string) {
	tr.t.Helper()

	for _, e := range engines {
		tr.write(pkg+"/internal/db/queries_"+e+"_generated.go", "package db\n")
	}
}

// squash collapses the padding a rendered table carries, so an assertion below
// is about a row's cells rather than about which fixture package happened to be
// the widest one in that tree.
func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (tr *tree) remove(rel string) {
	tr.t.Helper()

	must.NoError(tr.t, os.RemoveAll(filepath.Join(tr.root, filepath.FromSlash(rel))))
}

// generate runs the command against the tree and hands back the README.
func (tr *tree) generate() string {
	tr.t.Helper()

	must.NoError(tr.t, run(tr.root, "README.md"))

	body, err := os.ReadFile(filepath.Join(tr.root, "README.md"))
	must.NoError(tr.t, err)

	return string(body)
}

// fails runs the command expecting it not to finish, and hands back the reason.
func (tr *tree) fails() string {
	tr.t.Helper()

	err := run(tr.root, "README.md")
	must.Error(tr.t, err)

	return err.Error()
}

// aTransport is the smallest tree with one row in each table, which every
// mutation test starts from so that the mutation is the only difference.
func aTransport(t *testing.T) *tree {
	t.Helper()

	tr := newTree(t)
	tr.pkg("server/http", "//platform:transport server: bind, serve, drain")
	tr.pkg("audit")
	tr.migrations("audit", "postgres", "mysql", "sqlite")

	return tr
}

// TestAGainedTransportGainsARow is the first half of the claim this command
// makes: a package that grows handlers changes the README on the next generate,
// which is what reds the drift gate until the change is committed.
func TestAGainedTransportGainsARow(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	before := tr.generate()

	test.StrNotContains(T, before, "`settings/grpc`")

	tr.pkg("settings", "//platform:narrowing fixture")
	tr.migrations("settings", "postgres")
	tr.pkg("settings/grpc", "//platform:transport middleware: a header, checked")

	after := tr.generate()

	test.NotEqOp(T, before, after)
	test.StrContains(T, squash(after), squash("| `settings/grpc` | middleware | a header, checked |"))
}

// TestALostTransportLosesItsRow is the other direction, and the one a rename or
// a deletion causes. A row outliving its subject reads exactly like a live one.
func TestALostTransportLosesItsRow(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("sessions/http", "//platform:transport binding: a signed cookie")

	before := tr.generate()
	must.StrContains(T, before, "`sessions/http`")

	tr.remove("sessions")

	after := tr.generate()

	test.NotEqOp(T, before, after)
	test.StrNotContains(T, after, "`sessions/http`")
}

// TestAGainedDialectTicksItsColumn is the same claim for the matrix. The tick
// follows the .sql file, so a package that grows a dialect cannot be a row that
// goes on reading the old roster.
func TestAGainedDialectTicksItsColumn(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("workqueue", "//platform:narrowing the claim is the package")
	tr.migrations("workqueue", "postgres")

	before := tr.generate()
	must.StrContains(T, squash(before), squash("| `workqueue` | ✓ | — | — |"))

	tr.migrations("workqueue", "mysql", "sqlite")
	tr.remove("workqueue/doc.go")
	tr.pkg("workqueue")

	after := tr.generate()

	test.NotEqOp(T, before, after)
	test.StrContains(T, squash(after), squash("| `workqueue` | ✓ | ✓ | ✓ |"))
}

// TestALostDialectUnticksItsColumn is the mutation that matters most, because
// the row that survives it is the one a consumer chooses a database on.
func TestALostDialectUnticksItsColumn(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)

	before := tr.generate()
	must.StrContains(T, squash(before), squash("| `audit` | ✓ | ✓ | ✓ |"))

	tr.remove("audit/migrations/mysql.sql")
	tr.remove("audit/migrations/sqlite.sql")
	tr.pkg("audit", "//platform:narrowing MySQL cannot end the claim in one statement")

	after := tr.generate()

	test.NotEqOp(T, before, after)
	test.StrContains(T, squash(after), squash("| `audit` | ✓ | — | — |"))
	test.StrContains(T, after, "- `audit` — MySQL cannot end the claim in one statement.\n")
}

// TestANarrowedPackageMustSayWhy is the forcing function that replaced the test
// which used to check that a narrowed package's doc pointed at the README. The
// reason is now the thing the row is emitted from, so a narrowing with no reason
// is a failed generate rather than a row nobody can act on.
func TestANarrowedPackageMustSayWhy(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("timers")
	tr.migrations("timers", "postgres")

	test.StrContains(T, tr.fails(), "timers ships DDL for postgres alone")
}

// TestAWidenedPackageDropsItsReason is the same rule read backwards: a package
// that has grown the dialects it lacked still carries the sentence explaining
// why it lacked them, and that sentence is now wrong.
func TestAWidenedPackageDropsItsReason(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("timers", "//platform:narrowing MySQL has no RETURNING")
	tr.migrations("timers", "postgres", "mysql", "sqlite")

	test.StrContains(T, tr.fails(), "the narrowing it describes is gone")
}

// TestATransportMustNameItsKind is the judgement half of the transports table.
// The walk finds the directory; what the directory is for is a decision, and a
// generate that cannot read one refuses rather than emitting a blank cell.
func TestATransportMustNameItsKind(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("billing/http")

	test.StrContains(T, tr.fails(), "billing/http ships a transport and its doc.go carries no //platform:transport directive")
}

// TestKindsComeFromTheClosedSet keeps the second column from becoming free text.
// One row calling itself an adapter is a row answering a different question than
// the rest of the table.
func TestKindsComeFromTheClosedSet(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("billing/http", "//platform:transport adapter: a thing")

	test.StrContains(T, tr.fails(), `billing/http is a "adapter", which is not one of`)
}

// TestADirectiveOutlivingItsTransportFails catches the deletion that leaves the
// judgement behind, which would otherwise be a directive nobody reads sitting in
// a package that no longer ships what it describes.
func TestADirectiveOutlivingItsTransportFails(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("billing", "//platform:transport middleware: a header, checked")

	test.StrContains(T, tr.fails(), "billing carries a //platform:transport directive and ships no http or grpc subpackage")
}

// TestAReasonOutlivingItsStoreFails is the same for the matrix.
func TestAReasonOutlivingItsStoreFails(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("billing", "//platform:narrowing MySQL has no RETURNING")

	test.StrContains(T, tr.fails(), "billing carries a //platform:narrowing directive and ships no DDL to narrow")
}

// TestAPipeInADirectiveFails guards the one character a cell cannot hold, which
// would otherwise split a row into columns the header does not have.
func TestAPipeInADirectiveFails(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("billing/http", "//platform:transport middleware: a header | or two")

	test.StrContains(T, tr.fails(), "contains a pipe")
}

// TestAQuerierDisagreeingWithTheDDLFails is the second ground truth, and it is
// not the first restated: DDL and queries are generated from separate inputs, so
// a package can ship a schema for a dialect it executes nothing against.
func TestAQuerierDisagreeingWithTheDDLFails(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.querier("audit", "postgresql", "mysql")

	test.StrContains(T, tr.fails(), "audit ships DDL for [postgres mysql sqlite] and a querier emitted for [postgres mysql]")
}

// TestAQuerierAgreeingWithTheDDLPasses is the other side of that check, and it
// is what makes postgresql-versus-postgres this command's problem rather than
// the README's.
func TestAQuerierAgreeingWithTheDDLPasses(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.querier("audit", "postgresql", "mysql", "sqlite")

	test.StrContains(T, squash(tr.generate()), squash("| `audit` | ✓ | ✓ | ✓ |"))
}

// TestAStoreWithNoQuerierIsPassedOver keeps the tier a thing packages are ported
// onto rather than a thing the matrix requires of them.
func TestAStoreWithNoQuerierIsPassedOver(T *testing.T) {
	T.Parallel()

	test.StrContains(T, squash(aTransport(T).generate()), squash("| `audit` | ✓ | ✓ | ✓ |"))
}

// TestAMissingRegionFails is the failure the whole change turns on. A section
// whose markers have been deleted has stopped being generated, and emitting the
// other two while quietly skipping it is the rot this replaced.
func TestAMissingRegionFails(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.write("README.md", strings.ReplaceAll(readmeTemplate, "readmegen:dialects", "readmegen:whatever"))

	test.StrContains(T, tr.fails(), `no "<!-- readmegen:dialects -->" marker`)
}

// TestAnUnclosedRegionFails catches the half-deleted marker pair, which would
// otherwise swallow the rest of the file into one table.
func TestAnUnclosedRegionFails(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.write("README.md", strings.ReplaceAll(readmeTemplate, "<!-- /readmegen:narrowings -->", ""))

	test.StrContains(T, tr.fails(), "is not closed by")
}

// TestGeneratingTwiceChangesNothing is what makes the drift gate readable: a
// second run over an unchanged tree has to produce the same bytes, or every
// generate would red the workflow whether or not the tree had moved.
func TestGeneratingTwiceChangesNothing(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("workqueue", "//platform:narrowing the claim is the package")
	tr.migrations("workqueue", "postgres")

	test.EqOp(T, tr.generate(), tr.generate())
}

// TestOnlyNarrowedPackagesAreListed keeps the narrowings list exactly as long as
// the narrowing is real, so it is a list of decisions rather than of packages.
func TestOnlyNarrowedPackagesAreListed(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.pkg("workqueue", "//platform:narrowing the claim is the package")
	tr.migrations("workqueue", "postgres")

	body := tr.generate()

	test.StrContains(T, body, "- `workqueue` — the claim is the package.\n")
	test.StrNotContains(T, body, "- `audit`")
}

// TestRowsGroupByKindAndKeepFamiliesTogether is not cosmetic. Several rows read
// "the same, as interceptors", which is a sentence about the row above it, so
// the sort has to keep an http row ahead of its grpc sibling.
func TestRowsGroupByKindAndKeepFamiliesTogether(T *testing.T) {
	T.Parallel()

	tr := newTree(T)
	tr.pkg("ratelimiting/grpc", "//platform:transport middleware: the same, as interceptors")
	tr.pkg("ratelimiting/http", "//platform:transport middleware: a token per request")
	tr.pkg("server/grpc", "//platform:transport server: the same, for gRPC")
	tr.pkg("server/http", "//platform:transport server: bind, serve, drain")
	tr.pkg("authorization/http", "//platform:transport middleware: a declared requirement")

	body := tr.generate()

	order := []string{"server/http", "server/grpc", "authorization/http", "ratelimiting/http", "ratelimiting/grpc"}

	var at int

	for _, pkg := range order {
		i := strings.Index(body[at:], "`"+pkg+"`")
		must.GreaterEq(T, 0, i, must.Sprintf("%s is out of order or missing", pkg))

		at += i
	}
}

// TestDirectivesAreReadOnlyFromRealComments is why the file is parsed rather
// than scanned: a package whose prose quotes a directive is not a package that
// carries one.
func TestDirectivesAreReadOnlyFromRealComments(T *testing.T) {
	T.Parallel()

	tr := aTransport(T)
	tr.write("billing/doc.go", "package billing\n\nconst quoted = `//platform:transport middleware: not a directive`\n")

	test.StrContains(T, tr.generate(), "`server/http`")
}

// TestModuleRootIsFoundFromWithin is the thing that lets the go:generate line be
// `go run .` from this command's own directory and still describe the module.
func TestModuleRootIsFoundFromWithin(T *testing.T) {
	T.Parallel()

	root, err := moduleRoot()
	must.NoError(T, err)

	must.FileExists(T, filepath.Join(root, "go.mod"))
	must.FileExists(T, filepath.Join(root, "README.md"))
}
