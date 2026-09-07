package sqltier_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// tier is the answer this file requires of every package that holds SQL. The
// package doc says what each one means; the tests below say what each one costs
// to claim.
type tier int

const (
	unison tier = iota
	porting
	exempt
	none
)

func (t tier) String() string {
	switch t {
	case unison:
		return "unison"
	case porting:
		return "porting"
	case exempt:
		return "exempt"
	case none:
		return "none"
	default:
		return fmt.Sprintf("tier(%d)", int(t))
	}
}

type ruling struct {
	why  string
	tier tier
}

// rulings names every package in this module that holds SQL, and the one that
// was ruled on for holding none. The key is the package's directory, relative
// to the module root.
//
// A reason is required of an exemption and of a package ruled SQL-free, and of
// nothing else: "still composes its SQL in Go, and the port is tracked" is the
// same sentence in every case, and a per-package copy of it would be a place for
// a real reason to hide.
var rulings = map[string]ruling{
	// The tier itself. Each of these is what sqlc-gen-unison emitted from its
	// package's corpus, which is what every port below is a port onto.
	"authentication/oauth2clients/internal/oauth2clientsdb":        {tier: unison},
	"authentication/oauth2server/database/internal/oauth2serverdb": {tier: unison},
	"dataprivacy/internal/dataprivacydb":                           {tier: unison},
	"identity/internal/identitydb":                                 {tier: unison},
	"issuereports/internal/issuereportsdb":                         {tier: unison},
	"links/database/internal/linksdb":                              {tier: unison},
	"comments/internal/commentsdb":                                 {tier: unison},
	"saga/internal/sagadb":                                         {tier: unison},
	"sessions/database/internal/sessionsdb":                        {tier: unison},
	"webhooks/internal/webhooksdb":                                 {tier: unison},
	"cryptography/shredding/internal/shreddingdb":                  {tier: unison},
	"authentication/webauthn/database/internal/webauthndb":         {tier: unison},
	"audit/internal/auditdb":                                       {tier: unison},
	"authorization/database/internal/authorizationdb":              {tier: unison},
	"settings/internal/settingsdb":                                 {tier: unison},
	"uploads/registry/internal/registrydb":                         {tier: unison},
	"notifications/internal/notificationsdb":                       {tier: unison},
	"operations/internal/operationsdb":                             {tier: unison},
	"timers/internal/timersdb":                                     {tier: unison},
	"workqueue/internal/workqueuedb":                               {tier: unison},
	"outbox/internal/outboxdb":                                     {tier: unison},
	"metering/internal/meteringdb":                                 {tier: unison},
	"authentication/passwordreset/internal/passwordresetdb":        {tier: unison},
	"waitlists/internal/waitlistsdb":                               {tier: unison},
	"billing/internal/billingdb":                                   {tier: unison},

	// Still composing SQL in Go: nothing, since authentication/passwordreset —
	// the last one, and the one no roster had listed — landed on the tier. The
	// answer stays in the enumeration rather than going away with its last
	// entry: a store that arrives with hand-composed statements is ruled
	// `porting` here while its port is tracked, and this section being empty is
	// what "every package that owns SQL is on the tier" looks like when it is
	// true.

	// Not table SQL. The corpus is a set of statements checked against a schema
	// this module ships, and none of these is one.
	"audit/migrations":              {tier: exempt, why: "the DDL a schema ships, including the trigger that makes the log append-only; sqlc reads schemas rather than generating them"},
	"database/dialect":              {tier: exempt, why: "one NOTIFY statement, addressed to a channel rather than to a table"},
	"database/migrate":              {tier: exempt, why: "asks a connection which schema it resolves to; goose owns the bookkeeping table and ships its DDL"},
	"database/mysql/tableaccess":    {tier: exempt, why: "DCL and catalog introspection: sqlc has no spelling for CREATE USER or GRANT, and information_schema is not in any schema this module ships"},
	"database/postgres/tableaccess": {tier: exempt, why: "DCL and catalog introspection: sqlc has no spelling for CREATE USER or GRANT, and pg_roles is not in any schema this module ships"},
	"audit/internal/queries":        {tier: exempt, why: "a corpus source rather than a store: the statements here are rendered into the committed .sql that sqlc checks and unison emits from, and they cover dataprivacy/auditerasure's three as well, since that package owns no table of its own"},
	"webhooks/internal/queries":     {tier: exempt, why: "a corpus source rather than a store: the statements here are rendered into the committed .sql that sqlc checks and unison emits from, and the eleven this package writes out in full are the shapes database/querygen's doc rules out of it"},
	"database/querygen":             {tier: exempt, why: "the generator: its SQL literals are the statements a corpus is rendered from, not statements it executes"},
	"saga/internal/queries":         {tier: exempt, why: "a corpus source on database/querygen's own terms: the statements it holds are rendered into saga's canonical .sql and executed from the generated package, never from here"},
	"distributedlock/postgres":      {tier: exempt, why: "advisory-lock function calls; the lock is a number the server holds for a session, with no table, schema or projection"},
	"operations/internal/queries":   {tier: exempt, why: "a corpus source, like the generator it renders through: its literals are the statements the committed .sql is rendered from, checked by sqlc and executed through operations/internal/operationsdb"},
	"metering/internal/queries":     {tier: exempt, why: "a corpus source: the two statements it writes out in full are the flush claim's read and the fold's arithmetic, which querygen's closed comparand set refuses to render, and both reach a database only through metering/internal/meteringdb"},
	"outbox/internal/queries":       {tier: exempt, why: "a corpus source rather than a store: the statements here are rendered into the committed .sql that sqlc checks and unison emits from, and the six this package writes out in full are the shapes database/querygen's doc rules out of it"},
	"retention":                     {tier: exempt, why: "the table a pass deletes from, the column its age is measured from and the key its batches are bounded by all arrive from a Policy an application writes at run time, so this module ships no DDL for them and nothing committed is left for sqlc to check a statement against; what it takes from database/querygen's prune is the rules rather than the rendering, and a three-dialect container suite in place of the corpus"},
	"timers/internal/queries":       {tier: exempt, why: "a corpus source whose statements are written out in full rather than rendered from database/querygen, for the reason its own doc gives: every one of them assigns an expression, and each is still checked by sqlc and executed through timers/internal/timersdb"},
	"workqueue/internal/queries":    {tier: exempt, why: "a corpus source whose seven statements are written out in full rather than rendered from database/querygen, for the reason its own doc gives: this table carries no convention triple and every write assigns an expression, and each statement is still checked by sqlc and executed through workqueue/internal/workqueuedb"},
	"search/vector/pgvector":        {tier: exempt, why: "the index table's name, dimension and metadata column are configuration, so its DDL is issued at run time and nothing committed is left for sqlc to check a statement against"},
	"testutils/containers/pgtest":   {tier: exempt, why: "the schemas and databases a container test isolates itself with, created and dropped by the harness rather than by a store"},

	// Ruled on for holding no SQL. Recorded rather than left absent, so a
	// statement appearing here later is a failing test rather than a silence.
	"authorization/database":       {tier: none, why: "the resolver whose thirteen fmt.Sprintf builders a survey counted as zero: its statements are rendered by authorization/database/internal/queries and executed through the querier above, so the package that used to compose them holds none"},
	"filtering":                    {tier: none, why: "supplies the argument names a rendered statement binds and the conversions that bind them; the keyword a survey counted is a word in a comment"},
	"audit":                        {tier: none, why: "the hash-chained log, whose sixteen builders are rendered by audit/internal/queries and executed through the querier above; the recorder, the reader, the prune target and the erasure seam compose none"},
	"dataprivacy/auditerasure":     {tier: none, why: "it owns no table, so it owns no corpus: its two deletes and its count address audit's schema and are rendered into audit's .sql, reached through audit.Erasure"},
	"identity":                     {tier: none, why: "the store the tier was built for, and the first to finish: its statements are rendered by identity/internal/queries and executed through the querier above, so the package that used to compose them holds none"},
	"authentication/passwordreset": {tier: none, why: "ported: the five fmt.Sprintf builders that composed its issuance, its lookup, its guarded redemption, its revocation and its sweep are rendered by authentication/passwordreset/internal/queries and executed through the querier above, so the package that used to compose them holds none"},
	"metering":                     {tier: none, why: "the twelve builders that composed its SQL as Go strings are gone: its statements are rendered by metering/internal/queries and executed through the querier above, so the package that used to compose them holds none"},
	"outbox":                       {tier: none, why: "ported: its statements are rendered by outbox/internal/queries and executed through the querier above, and the one line of SQL it still names is database/dialect's NOTIFY, which is addressed to a channel rather than to a table"},
	"timers":                       {tier: none, why: "ported: its statements are rendered by timers/internal/queries and executed through the querier above, and the one line of SQL it still names is database/dialect's NOTIFY, which is addressed to a channel rather than to a table"},
	"workqueue":                    {tier: none, why: "ported: the eight builders that composed its claim, its lock-ordering CTE and its bounded reaper are rendered by workqueue/internal/queries and executed through the querier above, and the one line of SQL it still names is database/dialect's NOTIFY, which is addressed to a channel rather than to a table"},
}

// TestEverySQLPackageIsClassified is the entry this file exists to make
// impossible to forget. A package that grows a statement lands in no ruling
// until somebody puts it in one, and the decision it needs is which.
func TestEverySQLPackageIsClassified(T *testing.T) {
	T.Parallel()

	for pkg, count := range sqlPackages(T) {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			r, ok := rulings[pkg]
			must.True(t, ok, must.Sprintf("%s holds %d SQL statements and no ruling; it is on the tier, ported onto it, or exempt with a reason", pkg, count))
			test.NotEqOp(t, none, r.tier,
				test.Sprintf("%s is recorded as holding no SQL and holds %d statements", pkg, count))
		})
	}
}

// TestNoRulingOutlivesItsSQL is the other direction, and the one a port breaks.
// A package whose statements have moved into a corpus stops matching here, and
// the entry saying it is still porting has to move with them — an exemption or a
// port that outlives its subject is the same undocumented state read backwards.
func TestNoRulingOutlivesItsSQL(T *testing.T) {
	T.Parallel()

	found := sqlPackages(T)

	for pkg, r := range rulings {
		if r.tier == none {
			continue
		}

		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			_, ok := found[pkg]
			test.True(t, ok, test.Sprintf("%s is recorded as %s and holds no SQL; the ruling outlived its statements", pkg, r.tier))
		})
	}
}

// TestPackagesRuledSQLFreeHoldNoSQL is what makes a ruling of "holds none"
// worth writing down. The alternative is an absence, and an absence goes on
// reading true whatever the package does next.
func TestPackagesRuledSQLFreeHoldNoSQL(T *testing.T) {
	T.Parallel()

	found := sqlPackages(T)

	for pkg, r := range rulings {
		if r.tier != none {
			continue
		}

		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			count, ok := found[pkg]
			test.False(t, ok, test.Sprintf("%s holds %d SQL statements and is recorded as holding none, because %s", pkg, count, r.why))
		})
	}
}

// TestRulingsAreReasonedAndReal keeps an exemption from being where a package
// nobody has thought about goes to be quiet, and keeps a renamed or deleted
// package from leaving a ruling behind that reads as a live one.
func TestRulingsAreReasonedAndReal(T *testing.T) {
	T.Parallel()

	root := moduleRoot(T)

	for pkg, r := range rulings {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(pkg)))
			must.NoError(t, err, must.Sprintf("%s is ruled %s and is not a package in this module", pkg, r.tier))
			test.True(t, info.IsDir(), test.Sprintf("%s is not a directory", pkg))

			if r.tier == exempt || r.tier == none {
				test.NotEqOp(t, "", r.why, test.Sprintf("%s is %s without a reason", pkg, r.tier))
			}
		})
	}
}

// sqlStatementOpening matches a string literal that opens a SQL statement.
//
// Upper case is the discriminator, and it is doing real work: this module writes
// its SQL keywords in caps and its prose in a comment, so a case-insensitive
// match reports "create anthropic provider" as a CREATE and an endpoint named
// "revoke" as a REVOKE. Leading line comments are skipped because a rendered
// corpus opens with sqlc's own -- name: annotation.
var sqlStatementOpening = regexp.MustCompile(`(?s)\A\s*(?:--[^\n]*\n\s*)*(?:SELECT|INSERT\s+INTO|UPDATE|DELETE\s+FROM|WITH|CREATE|DROP|GRANT|REVOKE|TRUNCATE|ALTER)\b`)

// scanned is every package in the module holding at least one such literal,
// mapped to how many it holds. Walked once: four tests read it, and the answer
// is a property of the tree rather than of whichever test asked first.
var scanned = sync.OnceValues(func() (map[string]int, error) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return nil, err
	}

	return scan(root)
})

func sqlPackages(t *testing.T) map[string]int {
	t.Helper()

	found, err := scanned()
	must.NoError(t, err)

	return found
}

// moduleRoot is two directories up, which is where this package sits and where
// go.mod has to be for the answer to be this module rather than whatever tree a
// test binary was copied into.
func moduleRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(t, err)
	must.FileExists(t, filepath.Join(root, "go.mod"))

	return root
}

func scan(root string) (map[string]int, error) {
	found := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			// Dot directories hold no packages of this module's, and one of them
			// holds other checkouts of it: an agent worktree under .claude would
			// otherwise report every package in the module twice.
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "artifacts" || name == "testdata") {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		count, err := statementLiterals(path)
		if err != nil {
			return err
		}

		if count == 0 {
			return nil
		}

		dir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}

		found[filepath.ToSlash(dir)] += count

		return nil
	})
	if err != nil {
		return nil, err
	}

	return found, nil
}

// statementLiterals counts the string literals in one file that open a
// statement. It reads the AST rather than the bytes so that a comment saying the
// word SELECT is not a statement, which is the false positive that put a package
// holding no SQL at all on a survey of the ones that do.
func statementLiterals(path string) (int, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return 0, err
	}

	var count int

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}

		value, unquoteErr := strconv.Unquote(lit.Value)
		if unquoteErr != nil {
			return true
		}

		if sqlStatementOpening.MatchString(value) {
			count++
		}

		return true
	})

	return count, nil
}
