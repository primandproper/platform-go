package ast

import (
	goast "go/ast"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// writeModule writes a one-package module into a temporary directory and
// returns its root.
func writeModule(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	must.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/minimal\n\ngo 1.26\n"), 0o600))
	must.NoError(t, os.MkdirAll(filepath.Join(dir, "internal", "config"), 0o750))
	must.NoError(t, os.WriteFile(filepath.Join(dir, "internal", "config", "config.go"), []byte("package config\n\ntype Config struct{}\n"), 0o600))

	return dir
}

// walkDirs collects the relative directory reported for each file the walk
// visits, in sorted order.
func walkDirs(t *testing.T, dir string) []string {
	t.Helper()

	var seen []string

	must.NoError(t, WalkModule(dir, func(_ *goast.File, relDir string) error {
		seen = append(seen, relDir)

		return nil
	}))

	slices.Sort(seen)

	return seen
}

func TestGetModulePath(T *testing.T) {
	T.Parallel()

	T.Run("reads module path from go.mod", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/example/test\n\ngo 1.21\n"), 0o600)
		must.NoError(t, err)

		path, err := GetModulePath(dir)

		must.NoError(t, err)
		test.EqOp(t, "github.com/example/test", path)
	})

	T.Run("returns error when go.mod does not exist", func(t *testing.T) {
		t.Parallel()

		path, err := GetModulePath(t.TempDir())

		test.EqOp(t, "", path)
		test.Error(t, err)
	})

	T.Run("returns error when no module directive found", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("go 1.21\n"), 0o600)
		must.NoError(t, err)

		path, err := GetModulePath(dir)

		test.EqOp(t, "", path)
		test.Error(t, err)
	})
}

func TestWalkModule(T *testing.T) {
	T.Parallel()

	T.Run("reports each file's directory relative to the module root, in slash form", func(t *testing.T) {
		t.Parallel()

		dir := writeModule(t)
		must.NoError(t, os.WriteFile(filepath.Join(dir, "root.go"), []byte("package minimal\n"), 0o600))

		test.Eq(t, []string{".", "internal/config"}, walkDirs(t, dir))
	})

	T.Run("skips a nested module, whose types are not this module's", func(t *testing.T) {
		t.Parallel()

		dir := writeModule(t)
		nested := filepath.Join(dir, "tools")
		must.NoError(t, os.MkdirAll(nested, 0o750))
		must.NoError(t, os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module example.com/tools\n\ngo 1.26\n"), 0o600))
		must.NoError(t, os.WriteFile(filepath.Join(nested, "tools.go"), []byte("package tools\n"), 0o600))

		test.Eq(t, []string{"internal/config"}, walkDirs(t, dir))
	})

	T.Run("skips vendored, hidden, and fixture directories", func(t *testing.T) {
		t.Parallel()

		dir := writeModule(t)

		for _, name := range []string{"vendor", "testdata", ".hidden", "_ignored"} {
			sub := filepath.Join(dir, name)
			must.NoError(t, os.MkdirAll(sub, 0o750))
			must.NoError(t, os.WriteFile(filepath.Join(sub, "x.go"), []byte("package x\n"), 0o600))
		}

		test.Eq(t, []string{"internal/config"}, walkDirs(t, dir))
	})

	T.Run("skips test files, which declare what the module does not", func(t *testing.T) {
		t.Parallel()

		dir := writeModule(t)
		must.NoError(t, os.WriteFile(filepath.Join(dir, "internal", "config", "helper_test.go"), []byte("package config\n"), 0o600))

		test.Eq(t, []string{"internal/config"}, walkDirs(t, dir))
	})

	T.Run("reports a file it cannot parse rather than dropping what it declares", func(t *testing.T) {
		t.Parallel()

		dir := writeModule(t)
		must.NoError(t, os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package !!!\n"), 0o600))

		test.Error(t, WalkModule(dir, func(*goast.File, string) error { return nil }))
	})

	T.Run("reports a directory that does not exist", func(t *testing.T) {
		t.Parallel()

		test.Error(t, WalkModule(filepath.Join(t.TempDir(), "absent"), func(*goast.File, string) error { return nil }))
	})

	T.Run("stops and reports what the callback returns", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("sentinel")

		test.ErrorIs(t, WalkModule(writeModule(t), func(*goast.File, string) error { return sentinel }), sentinel)
	})
}

func TestModuleDirs(T *testing.T) {
	T.Parallel()

	T.Run("reads a vendor directory in preference to running go list", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		vendored := filepath.Join(dir, "vendor", "example.com", "dep")
		must.NoError(t, os.MkdirAll(vendored, 0o750))
		must.NoError(t, os.WriteFile(
			filepath.Join(dir, "vendor", "modules.txt"),
			[]byte("# example.com/dep v1.2.3\n## explicit; go 1.26\nexample.com/dep/database\n"),
			0o600,
		))

		dirs, err := ModuleDirs(t.Context(), dir)

		must.NoError(t, err)
		test.Eq(t, map[string]string{"example.com/dep": vendored}, dirs)
	})

	T.Run("omits a module listed in modules.txt whose source was not vendored", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		must.NoError(t, os.MkdirAll(filepath.Join(dir, "vendor"), 0o750))
		must.NoError(t, os.WriteFile(
			filepath.Join(dir, "vendor", "modules.txt"),
			[]byte("# example.com/absent v1.0.0\n## explicit\n"),
			0o600,
		))

		dirs, err := ModuleDirs(t.Context(), dir)

		must.NoError(t, err)
		test.MapEmpty(t, dirs)
	})
}
