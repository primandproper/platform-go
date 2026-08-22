package ast

import (
	"bufio"
	"context"
	goast "go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
)

// GetModulePath reads the module path from the go.mod file in the given directory.
func GetModulePath(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", errors.Wrap(err, "opening go.mod")
	}
	defer func() {
		_ = f.Close() //nolint:errcheck // read-only file; close error is not actionable here
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}

	if err = scanner.Err(); err != nil {
		return "", errors.Wrap(err, "scanning go.mod")
	}

	return "", errors.New("no module directive found in go.mod")
}

// WalkModule parses every Go source file belonging to the module rooted at dir
// and calls fn for each, with the directory it was found in relative to dir, in
// slash form ("." for the root package).
//
// What "belonging to" excludes is the part worth having one copy of. A vendor
// directory holds someone else's source. A testdata directory holds source that
// is deliberately not the module's — often deliberately broken. A directory
// beginning with "." or "_" is ignored by the go tool and so is ignored here. A
// _test.go file declares types the module does not export to anyone.
//
// The subtle one is a nested module: a subdirectory with its own go.mod is a
// different module, and its types belong to whatever import path that go.mod
// names rather than to a directory under this one. Walking into it silently
// files those types under the wrong path, which is the kind of thing that
// surfaces much later as a type that cannot be found.
//
// Files are parsed with parser.SkipObjectResolution, since nothing here needs
// the deprecated object graph.
func WalkModule(dir string, fn func(file *goast.File, relDir string) error) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return errors.Wrapf(err, "resolving module directory %q", dir)
	}

	fset := token.NewFileSet()

	if err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if path != root && skipDir(path, entry.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return errors.Wrapf(parseErr, "parsing %q", path)
		}

		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return errors.Wrapf(relErr, "locating %q within %q", path, root)
		}

		return fn(file, filepath.ToSlash(rel))
	}); err != nil {
		return errors.Wrapf(err, "walking module directory %q", dir)
	}

	return nil
}

// skipDir reports whether a directory encountered during a module walk holds
// source that is not that module's. See WalkModule for what each rule is for.
func skipDir(path, name string) bool {
	if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}

	_, err := os.Stat(filepath.Join(path, "go.mod"))

	return err == nil
}

// ModuleDirs locates the source of every module the module rooted at dir
// depends on, mapping each module's path to the directory holding it.
//
// A module in the graph whose source has not been downloaded has no directory
// and is omitted rather than reported: whether a particular absence matters is
// the caller's question, not this one's.
//
// Unlike the rest of this package it runs a subprocess — `go list -m all` — for
// the non-vendored case. That is a build-time cost in a build-time package, but
// it is a real one, and it is why a vendor directory is preferred when there is
// one rather than merely consulted.
func ModuleDirs(ctx context.Context, dir string) (map[string]string, error) {
	vendorDir := filepath.Join(dir, "vendor")
	if _, err := os.Stat(filepath.Join(vendorDir, "modules.txt")); err == nil {
		return vendoredModules(vendorDir)
	}

	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Path}}\t{{.Dir}}", "all")
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		return nil, errors.Wrapf(err, "listing the modules %q depends on", dir)
	}

	dirs := map[string]string{}

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		modulePath, moduleDir, found := strings.Cut(line, "\t")
		if !found || moduleDir == "" {
			continue
		}

		dirs[modulePath] = moduleDir
	}

	return dirs, nil
}

// vendoredModules reads the module list out of a vendor directory's
// modules.txt, mapping each module's path to the directory its packages were
// vendored into.
//
// It is read directly rather than through `go list -m all`, which refuses to
// answer at all while a vendor directory is present — and a vendored module's
// source is in the vendor tree rather than in the module cache, so there would
// be nothing to point at even if it did.
func vendoredModules(vendorDir string) (map[string]string, error) {
	f, err := os.Open(filepath.Join(vendorDir, "modules.txt"))
	if err != nil {
		return nil, errors.Wrap(err, "opening vendor/modules.txt")
	}

	defer func() {
		_ = f.Close() //nolint:errcheck // read-only file; close error is not actionable here
	}()

	dirs := map[string]string{}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		// Module lines read "# <path> <version>", optionally with a "=> ..."
		// replacement following. Package lines carry no marker, and "## " lines
		// are annotations such as "## explicit".
		line := scanner.Text()
		if !strings.HasPrefix(line, "# ") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		dir := filepath.Join(vendorDir, filepath.FromSlash(fields[1]))
		//nolint:gosec // G703: the path is built from this module's own vendor/modules.txt, and is only stat'ed
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			continue
		}

		dirs[fields[1]] = dir
	}

	if err = scanner.Err(); err != nil {
		return nil, errors.Wrap(err, "scanning vendor/modules.txt")
	}

	return dirs, nil
}
