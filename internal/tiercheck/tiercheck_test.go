package tiercheck_test

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

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
// inherits its parent's answer — which is how the README's own table is
// written, and what makes a new subpackage classified by construction.
//
// A reason is required of a nested entry and of nothing else. A top-level
// package's tier is the README's to explain; what needs saying here is why a
// directory sits under a path that belongs to primitives-go.
var roster = map[string]entry{
	// Domains: a noun with a table, and what it owes.
	"audit":         {tier: domain},
	"billing":       {tier: domain},
	"comments":      {tier: domain},
	"dataprivacy":   {tier: domain},
	"entitlements":  {tier: domain},
	"identity":      {tier: domain},
	"issuereports":  {tier: domain},
	"links":         {tier: domain},
	"mediaregistry": {tier: domain},
	"metering":      {tier: domain},
	"notifications": {tier: domain},
	"operations":    {tier: domain},
	"outbox":        {tier: domain},
	"rbac":          {tier: domain},
	"retention":     {tier: domain},
	"saga":          {tier: domain},
	"searchsync":    {tier: domain},
	"sessions":      {tier: domain},
	"settings":      {tier: domain},
	"shredding":     {tier: domain},
	"timers":        {tier: domain},
	"waitlists":     {tier: domain},
	"webhooks":      {tier: domain},
	"workqueue":     {tier: domain},

	// The straddles: a domain package under a path whose parent is a
	// primitives-go package. There is one such parent left, and it is the one
	// that groups rather than indirects — authentication/ holds five related
	// domain packages, so the name says something a reader wants, which is why
	// it survived the flattening the other six parents did not. The README's
	// "Primitives and Domains" section says why each one splits where it does.
	"authentication/oauth2clients":       {tier: domain, why: "the administered client registry's table, under a protocol implementation that is a primitive"},
	"authentication/oauth2serverstore":   {tier: domain, why: "the client and token tables, under a protocol implementation that is a primitive"},
	"authentication/passwordreset":       {tier: domain, why: "a table of reset tokens, under engines that hash and issue"},
	"authentication/signin":              {tier: domain, why: "the order the engines and the directory are used in, owning no table of its own"},
	"authentication/webauthncredentials": {tier: domain, why: "the ceremony table, under a protocol engine that is a primitive"},

	// The composition root.
	"errormappers": {tier: root},
	"service":      {tier: root},

	// internal/ is not in the README's table, because a consumer cannot import
	// any of it. It is classified here anyway, so that the completeness check
	// covers the whole tree rather than the part of it a consumer can see.
	"internal/cmd":              {tier: root, why: "generators run by make, over the whole tree"},
	"internal/configroster":     {tier: root, why: "the roster of every config subpackage a service wires, both modules'"},
	"internal/countwidth":       {tier: root, why: "a convention test over every result count the module exports"},
	"internal/protoconvention":  {tier: root, why: "a convention test over every .proto the module ships"},
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

// TestNoPackageIsAPrimitive is what the crossing check became when the split
// landed. It used to walk every import in the module and fail on a
// primitives-tier package that named a domain one; primitives-go now enforces
// that from its own side, over its whole tree and with no roster to keep, so
// the walk moved there and this is what is left of the rule on this side.
//
// A package ruled a primitive belongs in primitives-go and therefore not in
// this repository. Nothing can catch a primitive somebody rostered as a domain
// — that judgment is the README's rule applied by a person — but the answer is
// kept spellable so that reaching for it is a failing test with somewhere to
// send you, rather than a value the roster no longer has a word for.
func TestNoPackageIsAPrimitive(t *testing.T) {
	t.Parallel()

	for path, e := range roster {
		if e.tier == primitive {
			t.Errorf("%s is rostered as a primitive, which is a package this repository does not hold\n\t"+
				"a primitive goes to primitives-go; see the README's \"Primitives and Domains\" section for the "+
				"rule, and internal/tiercheck's doc for why the answer is still spellable here", path)
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

// TestNestedEntriesAreReasoned requires a why where one is load-bearing: an
// entry naming a directory below the module root. Every one of those is either
// a straddle — a domain package under a path that belongs to primitives-go — or
// an internal package the README's table cannot name, and in both cases the
// enumeration is the only place the reason can live. A top-level package's tier
// is the README's to explain, so an entry that agrees with its path carries no
// reason and is failed for carrying one.
func TestNestedEntriesAreReasoned(t *testing.T) {
	t.Parallel()

	for path, e := range roster {
		nested := strings.Contains(path, "/")

		switch {
		case nested && e.why == "":
			t.Errorf("%s needs a reason: it is %s under a path this module does not own the root of", path, e.tier)
		case !nested && e.why != "":
			t.Errorf("%s carries a reason but is a top-level package; the README's table is where a "+
				"top-level package's tier is explained", path)
		}
	}
}

// TestNoParentDirectoryOnlyIndirects fails a directory that holds no Go files
// and exactly one Go-bearing child. That directory is not grouping anything —
// it is one name a reader has to walk through, and it is worse than free,
// because a parent that belongs to primitives-go shows a relationship to a
// package this repository does not hold. `cryptography/` was the sharp case:
// there has never been a `cryptography` here, so `cryptography/shredding` named
// a parent that exists in neither tree a reader could open.
//
// Six directories were in that shape when this check was written and all six
// were flattened inside the /v14 major — `uploads/`, `authorization/`,
// `cryptography/` and `search/` at the root, and `authentication/oauth2server/`
// and `authentication/webauthn/` under the parent that stayed. A package path is
// the most breaking thing in Go, so the next one to appear is cheapest to catch
// before it is tagged, which is what this test is for.
//
// `authentication/` passes because it groups: five related domain packages under
// a name that says something. So does any directory holding Go files of its own,
// whatever its children — `searchsync` has one subpackage and is a package
// itself. proto/ trees are outside this entirely, because they hold no Go at any
// depth: their shape is the protobuf import path's and not this module's to
// choose.
func TestNoParentDirectoryOnlyIndirects(t *testing.T) {
	t.Parallel()

	moduleDir := moduleRoot(t)

	packages := packageDirs(t, moduleDir)

	holdsGo := make(map[string]struct{}, len(packages))
	for _, dir := range packages {
		holdsGo[dir] = struct{}{}
	}

	// children is, for each directory that some Go package sits beneath, the
	// set of its immediate children that lead to one. A directory holding no Go
	// of its own is worth its name only if that set has more than one member.
	children := map[string]map[string]struct{}{}

	for _, dir := range packages {
		segments := strings.Split(dir, "/")
		for i := 1; i < len(segments); i++ {
			ancestor := strings.Join(segments[:i], "/")

			if children[ancestor] == nil {
				children[ancestor] = map[string]struct{}{}
			}

			children[ancestor][segments[i]] = struct{}{}
		}
	}

	for _, ancestor := range slices.Sorted(maps.Keys(children)) {
		if _, ok := holdsGo[ancestor]; ok {
			continue
		}

		if len(children[ancestor]) != 1 {
			continue
		}

		only := slices.Sorted(maps.Keys(children[ancestor]))[0]

		t.Errorf("%s holds no Go files and exactly one child, %s: it indirects rather than groups\n\t"+
			"give the child a name that carries the relationship and move it up, as the six "+
			"flattened for /v14 did; a parent that groups several packages is what this check allows",
			ancestor, only)
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

// readmeRow matches one row of the "Primitives and Domains" table, and
// backticked pulls the package paths out of its last cell. The header and the
// separator match too and contribute nothing, because neither holds a
// backticked path.
var (
	readmeRow  = regexp.MustCompile(`(?m)^\|(.+)\|\s*$`)
	backticked = regexp.MustCompile("`([^`]+)`")
)

// nextHeading ends the section at the next heading of any level, searched from
// the line after the section's own. "## " alone would run past "### Transports"
// and read that table's rows as package rulings, which is one heading away from
// being right and reports as forty packages the roster has never heard of.
var nextHeading = regexp.MustCompile(`(?m)^#{1,6} `)

// readmeTiers reads the README's table into the same shape as the roster. It
// parses the rendered table rather than a machine-readable sidecar because the
// table is the artifact a reader is pointed at, so it is the one that has to be
// right.
//
// Every package the table names is a domain, because every package this module
// holds is one — the composition-root row is the exception, and it is named by
// its two members rather than by its wording, which is prose and free to change.
func readmeTiers(t *testing.T, moduleDir string) map[string]tier {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(moduleDir, "README.md"))
	must.NoError(t, err)

	section := string(body)
	start := strings.Index(section, "## Primitives and Domains")
	must.NotEq(t, -1, start, must.Sprint("the README has no \"Primitives and Domains\" section"))
	section = section[start:]

	if body := strings.IndexByte(section, '\n'); body != -1 {
		if end := nextHeading.FindStringIndex(section[body:]); end != nil {
			section = section[:body+end[0]]
		}
	}

	tiers := map[string]tier{}

	for _, row := range readmeRow.FindAllStringSubmatch(section, -1) {
		for _, cell := range backticked.FindAllStringSubmatch(row[1], -1) {
			path := cell[1]

			want := domain
			if slices.Contains([]string{"errormappers", "service"}, path) {
				want = root
			}

			tiers[path] = want
		}
	}

	return tiers
}
