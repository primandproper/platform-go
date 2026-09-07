package tiercheck_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// modulePath is this module's import path, and the prefix that tells one of its
// own imports from a dependency's.
const modulePath = "github.com/primandproper/platform-go/v14/"

// tier is the answer this file requires of every package. The package doc says
// what each one means.
type tier int

const (
	primitive tier = iota
	domain
	root
)

func (t tier) String() string {
	switch t {
	case primitive:
		return "primitive"
	case domain:
		return "domain"
	case root:
		return "root"
	default:
		return fmt.Sprintf("tier(%d)", int(t))
	}
}

// roster names every package in this module, by directory prefix relative to
// the module root. The longest matching prefix wins, so a nested package
// inherits its parent's answer unless it overrides it — which is how the
// README's own table is written, and what makes a new subpackage classified by
// construction.
//
// A reason is required of an entry that overrides its parent and of every
// internal one, and of nothing else. The tier of a top-level package is the
// README's to explain; what needs saying here is why a path departs from the
// answer its parent already gave.
var roster = map[string]entry{
	// Primitives: a provider behind an interface.
	"analytics":       {tier: primitive},
	"authentication":  {tier: primitive},
	"authorization":   {tier: primitive},
	"cache":           {tier: primitive},
	"capitalism":      {tier: primitive},
	"cryptography":    {tier: primitive},
	"distributedlock": {tier: primitive},
	"email":           {tier: primitive},
	"embeddings":      {tier: primitive},
	"eventcapture":    {tier: primitive},
	"eventstream":     {tier: primitive},
	"featureflags":    {tier: primitive},
	"llm":             {tier: primitive},
	"messagequeue":    {tier: primitive},
	"ratelimiting":    {tier: primitive},
	"search":          {tier: primitive},
	"secrets":         {tier: primitive},
	"uploads":         {tier: primitive},

	// Primitives: a transport whose shape is not the consumer's.
	"compression": {tier: primitive},
	"cookies":     {tier: primitive},
	"encoding":    {tier: primitive},
	"healthcheck": {tier: primitive},
	"httpclient":  {tier: primitive},
	"idempotency": {tier: primitive},
	"routing":     {tier: primitive},
	"server":      {tier: primitive},

	// Primitives: the database and schema tooling stores are built with.
	"database":  {tier: primitive},
	"filtering": {tier: primitive},

	// Primitives: the cross-cutting values and utilities both tiers build on.
	"batching":        {tier: primitive},
	"bitmask":         {tier: primitive},
	"charset":         {tier: primitive},
	"circuitbreaking": {tier: primitive},
	"clock":           {tier: primitive},
	"config":          {tier: primitive},
	"errors":          {tier: primitive},
	"fake":            {tier: primitive},
	"files":           {tier: primitive},
	"identifiers":     {tier: primitive},
	"jobs":            {tier: primitive},
	"numbers":         {tier: primitive},
	"observability":   {tier: primitive},
	"panicking":       {tier: primitive},
	"pointer":         {tier: primitive},
	"qrcodes":         {tier: primitive},
	"random":          {tier: primitive},
	"reflection":      {tier: primitive},
	"retry":           {tier: primitive},
	"tenancy":         {tier: primitive},
	"testutils":       {tier: primitive},
	"version":         {tier: primitive},

	// Domains: a noun with a table, and what it owes.
	"audit":         {tier: domain},
	"billing":       {tier: domain},
	"comments":      {tier: domain},
	"dataprivacy":   {tier: domain},
	"entitlements":  {tier: domain},
	"identity":      {tier: domain},
	"issuereports":  {tier: domain},
	"links":         {tier: domain},
	"metering":      {tier: domain},
	"notifications": {tier: domain},
	"operations":    {tier: domain},
	"outbox":        {tier: domain},
	"retention":     {tier: domain},
	"saga":          {tier: domain},
	"sessions":      {tier: domain},
	"settings":      {tier: domain},
	"timers":        {tier: domain},
	"waitlists":     {tier: domain},
	"webhooks":      {tier: domain},
	"workqueue":     {tier: domain},

	// The packages that straddle: a store nested inside a primitive, a primitive
	// nested inside a domain, or a domain flow nested under the engines it
	// orchestrates. Each overrides the answer its parent gave, and the README's
	// "Primitives and Domains" section says why each one splits where it does.
	"authentication/oauth2clients":         {tier: domain, why: "the administered client registry's table, under a protocol implementation that is a primitive"},
	"authentication/oauth2server/database": {tier: domain, why: "the client and token tables, under a protocol implementation that is a primitive"},
	"authentication/passwordreset":         {tier: domain, why: "a table of reset tokens, under engines that hash and issue"},
	"authentication/signin":                {tier: domain, why: "the order the engines and the directory are used in, owning no table of its own"},
	"authentication/webauthn/database":     {tier: domain, why: "the ceremony table, under a protocol engine that is a primitive"},
	"authorization/database":               {tier: domain, why: "the roles and permissions tables, under a policy interface that is a primitive"},
	"cryptography/shredding":               {tier: domain, why: "the per-subject key table, under primitives that encrypt and sign"},
	"uploads/registry":                     {tier: domain, why: "the object metadata rows, under an object store behind an interface"},
	"notifications/mobile":                 {tier: primitive, why: "a push provider behind an interface, under the package that owns the device and inbox tables"},
	"search/sync":                          {tier: domain, why: "a reindexing worker driven by the outbox, under two search indexes that are primitives"},
	"webhooks/inbound":                     {tier: primitive, why: "a receiver whose payload shape Stripe or GitHub decides, under the endpoint store"},

	// The composition root.
	"errormappers": {tier: root},
	"service":      {tier: root},

	// internal/ is not in the README's table, because a consumer cannot import
	// any of it. It is classified here anyway: five of these become exported
	// packages in primitives-go, so a domain import from one of them is the
	// same blocker as any other, and the two that are only reachable from one
	// tier should say which.
	"internal/cbormode":         {tier: primitive, why: "one CBOR encode mode, shared by cache and encoding"},
	"internal/cfgnorm":          {tier: primitive, why: "config normalizations over struct tags; exported in primitives-go"},
	"internal/injection":        {tier: primitive, why: "optional resolution from a do.Injector; exported in primitives-go"},
	"internal/pgretry":          {tier: primitive, why: "the Postgres write-retry loop; exported in primitives-go"},
	"internal/plainname":        {tier: primitive, why: "an identifier's plain form; exported in primitives-go"},
	"internal/redisclient":      {tier: primitive, why: "one redis client build, shared by the four redis providers"},
	"internal/sqlguard":         {tier: primitive, why: "the guarded write; exported in primitives-go"},
	"internal/cmd":              {tier: root, why: "generators run by make, over the whole tree"},
	"internal/configroster":     {tier: root, why: "the roster of every config subpackage in the module, both tiers"},
	"internal/schemaconvention": {tier: root, why: "a convention test over every package that ships DDL"},
	"internal/scopeddl":         {tier: root, why: "a convention test over every scoped table in the module"},
	"internal/sentinelmatrix":   {tier: root, why: "the roster of every domain sentinel and the mappers that answer for it"},
	"internal/sqltier":          {tier: root, why: "the roster of every package in the module that holds SQL"},
	"internal/tiercheck":        {tier: root, why: "this roster"},
}

type entry struct {
	why  string
	tier tier
}

// TestNoPrimitiveImportsADomain is the check the rest of this file exists to
// make possible.
//
// Test files are included deliberately. A primitives-go test file is compiled by
// primitives-go, so a test that reaches into the domain tier blocks the split
// exactly as much as production code that does.
func TestNoPrimitiveImportsADomain(t *testing.T) {
	t.Parallel()

	moduleDir := moduleRoot(t)

	for _, edge := range edges(t, moduleDir) {
		from, ok := classify(edge.from)
		if !ok {
			continue // TestEveryPackageIsClassified is where that fails.
		}

		to, ok := classify(edge.to)
		if !ok {
			continue
		}

		if from.tier == primitive && to.tier == domain {
			t.Errorf("%s (%s) imports %s (%s), from %s\n\t"+
				"primitives-go will not be able to import platform-go; see the README's "+
				"\"Primitives and Domains\" section, and internal/tiercheck's doc",
				edge.from, from.tier, edge.to, to.tier, edge.file)
		}
	}
}

// TestEveryPackageIsClassified is the half of the roster that catches a package
// nobody ruled on. Without it a directory nobody named simply is not checked,
// which reads the same as a directory that passed.
func TestEveryPackageIsClassified(t *testing.T) {
	t.Parallel()

	moduleDir := moduleRoot(t)

	for _, dir := range packageDirs(t, moduleDir) {
		if _, ok := classify(dir); !ok {
			t.Errorf("package %s is in no tier: add it to internal/tiercheck's roster, "+
				"and to the README's \"Primitives and Domains\" table if it is a new top-level package", dir)
		}
	}
}

// TestNoRosterEntryOutlivesItsDirectory is the other direction. An entry for a
// directory that no longer exists is a ruling about nothing, and it is how a
// roster starts describing a tree that has moved on.
func TestNoRosterEntryOutlivesItsDirectory(t *testing.T) {
	t.Parallel()

	moduleDir := moduleRoot(t)

	for path := range roster {
		info, err := os.Stat(filepath.Join(moduleDir, filepath.FromSlash(path)))
		if err != nil || !info.IsDir() {
			t.Errorf("roster names %s, which is not a directory in this module", path)
		}
	}
}

// TestOverridesAndInternalsAreReasoned requires a why where one is load-bearing:
// an entry that departs from the answer its parent gave, and every internal
// package. A top-level package's tier is the README's to explain.
func TestOverridesAndInternalsAreReasoned(t *testing.T) {
	t.Parallel()

	for path, e := range roster {
		internal := strings.HasPrefix(path, "internal/")

		parent, hasParent := classify(filepath.ToSlash(filepath.Dir(path)))
		override := hasParent && parent.tier != e.tier

		switch {
		case (internal || override) && e.why == "":
			t.Errorf("%s needs a reason: it is %s where its parent or its path does not say so", path, e.tier)
		case !internal && !override && e.why != "":
			t.Errorf("%s carries a reason but agrees with its parent; the README's table is where a "+
				"top-level package's tier is explained", path)
		}
	}
}

// TestRosterAgreesWithTheREADME reads the "Primitives and Domains" table and
// requires that it and the roster say the same thing about every package it
// names. The table is what a reader is pointed at; this file is what a build
// enforces, and the two saying different things would make one of them a lie
// nobody notices.
func TestRosterAgreesWithTheREADME(t *testing.T) {
	t.Parallel()

	documented := readmeTiers(t, moduleRoot(t))
	must.MapNotEmpty(t, documented)

	for path, want := range documented {
		got, ok := roster[path]
		if !ok {
			t.Errorf("the README's table names %s and the roster does not", path)

			continue
		}

		test.EqOp(t, want, got.tier, test.Sprintf("tier of %s", path))
	}

	// And the other direction, for everything a consumer can import. internal/
	// is deliberately absent from the table.
	for path, e := range roster {
		if strings.HasPrefix(path, "internal/") {
			continue
		}

		if _, ok := documented[path]; !ok {
			t.Errorf("the roster names %s (%s) and the README's table does not", path, e.tier)
		}
	}
}

// classify resolves a directory to its ruling by longest matching prefix, which
// is how a nested package inherits its parent's answer.
func classify(dir string) (entry, bool) {
	for {
		if e, ok := roster[dir]; ok {
			return e, true
		}

		parent := filepath.ToSlash(filepath.Dir(dir))
		if parent == dir || parent == "." || parent == "/" {
			return entry{}, false
		}

		dir = parent
	}
}

// moduleRoot is two directories up, which is where this package sits and where
// go.mod has to be for the answer to be this module rather than whatever tree a
// test binary was copied into.
func moduleRoot(t *testing.T) string {
	t.Helper()

	moduleDir, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(t, err)
	must.FileExists(t, filepath.Join(moduleDir, "go.mod"))

	return moduleDir
}

// skipDir names the directories that hold no packages of this module's. One of
// them holds other checkouts of it: an agent worktree under .claude would
// otherwise report every package in the module twice. testdata is skipped
// because the go tool skips it — a fixture there is compiled only by the test
// that names it, and creates no module edge.
func skipDir(moduleDir, path, name string) bool {
	return path != moduleDir && (strings.HasPrefix(name, ".") || name == "artifacts" || name == "testdata")
}

// packageDirs is every directory in the module holding at least one Go file.
func packageDirs(t *testing.T, moduleDir string) []string {
	t.Helper()

	seen := map[string]struct{}{}

	must.NoError(t, filepath.WalkDir(moduleDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			if skipDir(moduleDir, path, d.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		rel, err := filepath.Rel(moduleDir, filepath.Dir(path))
		if err != nil {
			return err
		}

		if rel != "." {
			seen[filepath.ToSlash(rel)] = struct{}{}
		}

		return nil
	}))

	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}

	sort.Strings(dirs)

	return dirs
}

// edge is one import of this module's own, from the directory that wrote it to
// the directory it names.
type edge struct {
	from string
	to   string
	file string
}

// edges reads every Go file in the module, test files included, and returns the
// imports that name this module.
func edges(t *testing.T, moduleDir string) []edge {
	t.Helper()

	var found []edge

	must.NoError(t, filepath.WalkDir(moduleDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			if skipDir(moduleDir, path, d.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(moduleDir, filepath.Dir(path))
		if err != nil {
			return err
		}

		from := filepath.ToSlash(rel)

		for _, spec := range file.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil || !strings.HasPrefix(imported, modulePath) {
				continue
			}

			found = append(found, edge{
				from: from,
				to:   strings.TrimPrefix(imported, modulePath),
				file: filepath.ToSlash(mustRel(moduleDir, path)),
			})
		}

		return nil
	}))

	return found
}

func mustRel(moduleDir, path string) string {
	rel, err := filepath.Rel(moduleDir, path)
	if err != nil {
		return path
	}

	return rel
}

// readmeRow matches one row of the "Primitives and Domains" table: a tier in the
// first cell, and a list of backticked package paths in the last.
var (
	readmeRow  = regexp.MustCompile(`(?m)^\|\s*` + "`" + `(primitives-go|platform-go)` + "`" + `\s*\|[^|]*\|(.*)\|\s*$`)
	backticked = regexp.MustCompile("`([^`]+)`")
)

// readmeTiers reads the README's table into the same shape as the roster. It
// parses the rendered table rather than a machine-readable sidecar because the
// table is the artifact a reader is pointed at, so it is the one that has to be
// right.
func readmeTiers(t *testing.T, moduleDir string) map[string]tier {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(moduleDir, "README.md"))
	must.NoError(t, err)

	section := string(body)
	start := strings.Index(section, "## Primitives and Domains")
	must.NotEq(t, -1, start, must.Sprint("the README has no \"Primitives and Domains\" section"))
	section = section[start:]

	if end := strings.Index(section[1:], "\n## "); end != -1 {
		section = section[:end+1]
	}

	tiers := map[string]tier{}

	for _, row := range readmeRow.FindAllStringSubmatch(section, -1) {
		// The composition-root row is the one place the table's tier column and
		// this file's answer deliberately differ: "platform-go" is where those
		// two packages live, and root is what they are.
		want := domain
		if row[1] == "primitives-go" {
			want = primitive
		}

		for _, cell := range backticked.FindAllStringSubmatch(row[2], -1) {
			path := cell[1]
			if slices.Contains([]string{"errormappers", "service"}, path) {
				want = root
			}

			tiers[path] = want
		}
	}

	return tiers
}
