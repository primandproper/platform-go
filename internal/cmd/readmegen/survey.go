package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// generatedQueries matches the per-dialect file names sqlc-gen-unison emits.
// postgresql is sqlc's spelling of the engine; this module's dialect is called
// postgres, and the two are reconciled here rather than in the README, which
// names the dialect a consumer configures.
var generatedQueries = regexp.MustCompile(`^queries_(postgresql|mysql|sqlite)_generated\.go$`)

// The three dialect identifiers, spelled once because they are a key, a column
// heading and a file name away from each other.
const (
	postgres = "postgres"
	mysql    = "mysql"
	sqlite   = "sqlite"
)

// dialects are the three the module speaks, in the order the matrix's columns
// carry them. The order is the column order: a roster is sorted into it before
// it is rendered, so a table's cells are about membership rather than about the
// order a directory was read in.
var dialects = []string{postgres, mysql, sqlite}

// dialectColumns are the headings the three are written under, which are the
// names a consumer reads rather than the identifiers a config carries.
var dialectColumns = map[string]string{
	postgres: "Postgres",
	mysql:    "MySQL",
	sqlite:   "SQLite",
}

// transportDirs are the directory names that make a package a transport. A
// package is one because it ships one of these, not because it says so
// anywhere, which is what makes a package that has quietly grown handlers the
// one this walk catches.
var transportDirs = []string{"http", "grpc"}

// kinds are the closed set a transport directive may name, in the order the
// table groups them. Closed on purpose, for the reason the command's doc gives.
var kinds = []string{"server", "mapping", "wire conversion", "middleware", "binding", "resource surface"}

// The two directives, and the only two things this command reads out of a
// package rather than off the filesystem.
const (
	transportDirective = "//platform:transport"
	narrowingDirective = "//platform:narrowing"
)

// transport is one row of the Stores and Transports table: the package, and the
// two judgements its own doc.go carries about it.
type transport struct {
	pkg   string
	kind  string
	shape string
}

// store is one row of the SQL Dialect Support matrix, plus the reason it is a
// short row when it is one.
type store struct {
	pkg       string
	narrowing string
	dialects  []string
}

// survey is one walk of a module tree, read into the two rosters the README
// states.
type survey struct {
	transports []transport
	stores     []store
}

// surveyTree walks root and reads both rosters out of it, pairing each with the
// directive the package it names carries.
//
// Every mismatch between the two is an error rather than a row: a transport
// with no directive, a directive on a package that ships no transport, a
// narrowed store with no reason, and a reason on a store that narrows nothing.
// Each of those is a change somebody made without saying what it means, and the
// generate that would have written it down is the moment to ask.
func surveyTree(root string) (*survey, error) {
	var transportPkgs []string

	shipped := map[string][]string{}
	emitted := map[string][]string{}
	directives := map[string]map[string][]string{}

	err := walkModule(root, func(path string, d fs.DirEntry) error {
		pkg, relErr := packagePath(root, path)
		if relErr != nil {
			return relErr
		}

		switch {
		case d.IsDir() && slices.Contains(transportDirs, d.Name()):
			transportPkgs = append(transportPkgs, pkg)

		case d.IsDir() && d.Name() == "migrations":
			ddl, ddlErr := shippedDialects(path)
			if ddlErr != nil {
				return ddlErr
			}

			if len(ddl) > 0 {
				shipped[filepath.ToSlash(filepath.Dir(pkg))] = ddl
			}

		case !d.IsDir() && generatedQueries.MatchString(d.Name()):
			emitted[filepath.ToSlash(filepath.Dir(pkg))] = append(
				emitted[filepath.ToSlash(filepath.Dir(pkg))],
				dialectOf(generatedQueries.FindStringSubmatch(d.Name())[1]))

		case !d.IsDir() && d.Name() == "doc.go":
			found, readErr := readDirectives(path)
			if readErr != nil {
				return readErr
			}

			if len(found) > 0 {
				directives[filepath.ToSlash(filepath.Dir(pkg))] = found
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	transports, err := readTransports(transportPkgs, directives)
	if err != nil {
		return nil, err
	}

	stores, err := readStores(shipped, directives)
	if err != nil {
		return nil, err
	}

	if err = checkQueriers(stores, emitted); err != nil {
		return nil, err
	}

	return &survey{transports: transports, stores: stores}, nil
}

// readTransports pairs every http or grpc directory with the directive its
// doc.go carries, and refuses a pairing that is missing on either side.
func readTransports(pkgs []string, directives map[string]map[string][]string) ([]transport, error) {
	found := make([]transport, 0, len(pkgs))

	for _, pkg := range pkgs {
		lines := directives[pkg][transportDirective]

		switch len(lines) {
		case 1:
		case 0:
			return nil, fmt.Errorf(
				"%s ships a transport and its doc.go carries no %s directive; add one naming its kind and whose shape it is",
				pkg, transportDirective)
		default:
			return nil, fmt.Errorf("%s carries %d %s directives and a package is one row", pkg, len(lines), transportDirective)
		}

		kind, shape, ok := strings.Cut(lines[0], ":")
		if !ok {
			return nil, fmt.Errorf("%s: %s %q has no kind; the form is %q", pkg, transportDirective, lines[0], "<kind>: <whose shape it is>")
		}

		kind, shape = strings.TrimSpace(kind), strings.TrimSpace(shape)

		if !slices.Contains(kinds, kind) {
			return nil, fmt.Errorf("%s is a %q, which is not one of %v", pkg, kind, kinds)
		}

		if err := cellSafe(pkg, transportDirective, shape); err != nil {
			return nil, err
		}

		found = append(found, transport{pkg: pkg, kind: kind, shape: shape})
	}

	for pkg, byName := range directives {
		if _, ok := byName[transportDirective]; ok && !slices.Contains(pkgs, pkg) {
			return nil, fmt.Errorf("%s carries a %s directive and ships no http or grpc subpackage", pkg, transportDirective)
		}
	}

	slices.SortFunc(found, byTableOrder)

	return found, nil
}

// byTableOrder groups the transports by kind and then keeps each family
// together with its http row ahead of its grpc one. The adjacency is not
// cosmetic: several rows read "the same, as interceptors", which is a sentence
// about the row above it.
func byTableOrder(a, b transport) int {
	if n := slices.Index(kinds, a.kind) - slices.Index(kinds, b.kind); n != 0 {
		return n
	}

	if n := strings.Compare(parentOf(a.pkg), parentOf(b.pkg)); n != 0 {
		return n
	}

	return slices.Index(transportDirs, filepath.Base(a.pkg)) - slices.Index(transportDirs, filepath.Base(b.pkg))
}

func parentOf(pkg string) string {
	return filepath.ToSlash(filepath.Dir(pkg))
}

// readStores pairs every DDL-shipping package with the narrowing directive its
// doc.go carries, which is required of exactly the packages that ship fewer
// than three dialects.
func readStores(shipped map[string][]string, directives map[string]map[string][]string) ([]store, error) {
	found := make([]store, 0, len(shipped))

	for pkg, ddl := range shipped {
		lines := directives[pkg][narrowingDirective]

		if len(lines) > 1 {
			return nil, fmt.Errorf("%s carries %d %s directives and a package narrows for one reason", pkg, len(lines), narrowingDirective)
		}

		narrowed := len(ddl) < len(dialects)

		switch {
		case narrowed && len(lines) == 0:
			return nil, fmt.Errorf(
				"%s ships DDL for %s alone and its doc.go carries no %s directive; add one saying why",
				pkg, strings.Join(ddl, ", "), narrowingDirective)

		case !narrowed && len(lines) == 1:
			return nil, fmt.Errorf(
				"%s carries a %s directive and ships DDL for every dialect; the narrowing it describes is gone",
				pkg, narrowingDirective)
		}

		var reason string
		if narrowed {
			reason = strings.TrimSuffix(lines[0], ".")
		}

		found = append(found, store{pkg: pkg, dialects: ddl, narrowing: reason})
	}

	for pkg, byName := range directives {
		if _, ok := byName[narrowingDirective]; ok {
			if _, ships := shipped[pkg]; !ships {
				return nil, fmt.Errorf("%s carries a %s directive and ships no DDL to narrow", pkg, narrowingDirective)
			}
		}
	}

	slices.SortFunc(found, func(a, b store) int { return strings.Compare(a.pkg, b.pkg) })

	return found, nil
}

// cellSafe refuses the one character a markdown cell cannot hold, so a
// directive that would have silently split a row into two columns is reported
// against the package that wrote it.
func cellSafe(pkg, directive, text string) error {
	if text == "" {
		return fmt.Errorf("%s: %s carries no prose", pkg, directive)
	}

	if strings.Contains(text, "|") {
		return fmt.Errorf("%s: %s contains a pipe, which a table cell cannot hold: %q", pkg, directive, text)
	}

	return nil
}

// shippedDialects is the dialects a migrations directory embeds a schema for.
//
// Names outside the three are ignored rather than reported: webhooks ships
// upgrade_<dialect>.sql beside its schema, which is a migration for an existing
// table rather than a dialect the package supports.
func shippedDialects(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var found []string

	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".sql"); ok && slices.Contains(dialects, name) {
			found = append(found, name)
		}
	}

	slices.SortFunc(found, func(a, b string) int {
		return slices.Index(dialects, a) - slices.Index(dialects, b)
	})

	return found, nil
}

// readDirectives reads the //platform: lines out of one doc.go, keyed by
// directive name.
//
// The file is parsed rather than scanned so that a directive is a comment the
// compiler agrees is one, and not a line inside a string literal or a paragraph
// of a block comment that happens to start with the right bytes.
func readDirectives(path string) (map[string][]string, error) {
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	found := map[string][]string{}

	for _, group := range file.Comments {
		for _, comment := range group.List {
			name, rest, ok := strings.Cut(comment.Text, " ")
			if !ok || !strings.HasPrefix(name, "//platform:") {
				continue
			}

			found[name] = append(found[name], strings.TrimSpace(rest))
		}
	}

	return found, nil
}

// walkModule visits every entry in the module, skipping the directories that
// hold no packages of its own.
func walkModule(root string, visit func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		// Dot directories hold no packages of this module's, and one of them
		// holds other checkouts of it: an agent worktree under .claude would
		// otherwise report every package in the module twice. testdata holds
		// fixture trees that belong to no package.
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "artifacts" || name == "testdata") {
				return filepath.SkipDir
			}
		}

		return visit(path, d)
	})
}

// packagePath is path relative to the module root, in the slash-separated form
// the README's rows are written in.
func packagePath(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}

	return filepath.ToSlash(rel), nil
}

// dialectOf reconciles sqlc's engine name with the dialect this module's config
// names, which differ for exactly one of the three.
func dialectOf(engine string) string {
	if engine == "postgresql" {
		return postgres
	}

	return engine
}

// checkQueriers holds the emitted matrix against the second ground truth it has,
// and it is not the first one restated. DDL and queries are generated from
// separate inputs, so a package can ship a schema for a dialect it executes
// nothing against — a migration that creates tables no querier will ever read.
// A row is only worth acting on if both agree, and disagreement is a failed
// generate rather than a ✓ a consumer would believe.
//
// A store with no generated querier is passed over rather than failed: the tier
// is something packages are ported onto, and one still composing its SQL in Go
// has a roster its DDL is the only record of. internal/sqltier is where that
// state is enumerated and where a port is tracked.
//
// Attribution is by longest matching store prefix rather than by the generated
// package's own directory, since the querier lives in an internal subpackage
// several levels beneath the store — identity/internal/identitydb belongs to
// identity, and authentication/webauthncredentials/internal/webauthndb to
// authentication/webauthncredentials rather than to authentication.
func checkQueriers(stores []store, emitted map[string][]string) error {
	byStore := map[string][]string{}

	for dir, found := range emitted {
		owner, ok := owningStore(dir, stores)
		if !ok {
			continue
		}

		for _, d := range found {
			if !slices.Contains(byStore[owner], d) {
				byStore[owner] = append(byStore[owner], d)
			}
		}
	}

	for i := range stores {
		st := &stores[i]

		found, ok := byStore[st.pkg]
		if !ok {
			continue
		}

		slices.SortFunc(found, func(a, b string) int {
			return slices.Index(dialects, a) - slices.Index(dialects, b)
		})

		if !slices.Equal(found, st.dialects) {
			return fmt.Errorf(
				"%s ships DDL for %v and a querier emitted for %v; one of the two is wrong and neither is a row to publish",
				st.pkg, st.dialects, found)
		}
	}

	return nil
}

// owningStore is the longest DDL-shipping package that dir sits inside, which is
// the store the querier beneath it belongs to.
func owningStore(dir string, stores []store) (string, bool) {
	var owner string

	for i := range stores {
		pkg := stores[i].pkg

		if dir != pkg && !strings.HasPrefix(dir, pkg+"/") {
			continue
		}

		if len(pkg) > len(owner) {
			owner = pkg
		}
	}

	return owner, owner != ""
}
