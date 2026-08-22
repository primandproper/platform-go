package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// renderConfig carries no `env:` tags on purpose. The round-trip property the
// render functions promise is about the file, and a field with an envDefault
// comes back as that default no matter what was written — the caarlos0/env
// behavior LoadFromJSONFile documents. TestRenderJSONFiles_EnvDefaultOverlay
// covers that interaction separately.
type renderConfig struct {
	Limits  map[string]int `json:"limits"  toml:"limits"  yaml:"limits"`
	Name    string         `json:"name"    toml:"name"    yaml:"name"`
	Tags    []string       `json:"tags"    toml:"tags"    yaml:"tags"`
	Port    int            `json:"port"    toml:"port"    yaml:"port"`
	Verbose bool           `json:"verbose" toml:"verbose" yaml:"verbose"`
}

func sampleRenderConfigs() (dev, prod *renderConfig) {
	return &renderConfig{
			Limits:  map[string]int{"burst": 10, "sustained": 2},
			Name:    "localdev",
			Tags:    []string{"local", "insecure"},
			Port:    8080,
			Verbose: true,
		}, &renderConfig{
			Limits:  map[string]int{"burst": 500, "sustained": 100},
			Name:    "production",
			Tags:    []string{"hardened"},
			Port:    443,
			Verbose: false,
		}
}

func TestRenderJSONFiles(t *testing.T) {
	t.Parallel()

	t.Run("round-trips through LoadFromJSONFile", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		dev, prod := sampleRenderConfigs()

		// The prod path names a directory that does not exist yet, exercising the
		// parent-directory creation.
		devPath := filepath.Join(dir, "localdev.json")
		prodPath := filepath.Join(dir, "environments", "prod", "config.json")

		must.NoError(t, RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Config: dev, Name: "localdev", Path: devPath},
			{Config: prod, Name: "production", Path: prodPath},
		}))

		for path, want := range map[string]*renderConfig{devPath: dev, prodPath: prod} {
			test.FileExists(t, path)

			got, err := LoadFromJSONFile[renderConfig](t.Context(), path)
			must.NoError(t, err)
			test.Eq(t, want, got)
		}
	})

	t.Run("writes indented output ending in exactly one newline", func(t *testing.T) {
		t.Parallel()

		dev, _ := sampleRenderConfigs()
		path := filepath.Join(t.TempDir(), "config.json")

		must.NoError(t, RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Config: dev, Name: "localdev", Path: path},
		}))

		contents, err := os.ReadFile(path)
		must.NoError(t, err)

		test.StrContains(t, string(contents), "\n\t\"name\": \"localdev\",")
		test.True(t, strings.HasSuffix(string(contents), "}\n"))
		test.False(t, strings.HasSuffix(string(contents), "\n\n"))
	})

	t.Run("is byte-stable across runs", func(t *testing.T) {
		t.Parallel()

		dev, _ := sampleRenderConfigs()
		path := filepath.Join(t.TempDir(), "config.json")
		envs := []Environment[renderConfig]{{Config: dev, Name: "localdev", Path: path}}

		must.NoError(t, RenderJSONFiles(t.Context(), envs))
		first, err := os.ReadFile(path)
		must.NoError(t, err)

		must.NoError(t, RenderJSONFiles(t.Context(), envs))
		second, err := os.ReadFile(path)
		must.NoError(t, err)

		test.Eq(t, first, second)
	})

	t.Run("renders a config that does not implement validation", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.json")

		must.NoError(t, RenderJSONFiles(t.Context(), []Environment[sampleConfig]{
			{Config: &sampleConfig{Name: "unvalidatable"}, Name: "localdev", Path: path},
		}))

		test.FileExists(t, path)
	})
}

func TestRenderTOMLFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dev, prod := sampleRenderConfigs()
	devPath := filepath.Join(dir, "localdev.toml")
	prodPath := filepath.Join(dir, "nested", "prod.toml")

	must.NoError(t, RenderTOMLFiles(t.Context(), []Environment[renderConfig]{
		{Config: dev, Name: "localdev", Path: devPath},
		{Config: prod, Name: "production", Path: prodPath},
	}))

	for path, want := range map[string]*renderConfig{devPath: dev, prodPath: prod} {
		contents, err := os.ReadFile(path)
		must.NoError(t, err)
		test.True(t, strings.HasSuffix(string(contents), "\n"))
		test.False(t, strings.HasSuffix(string(contents), "\n\n"))

		got, err := LoadFromTOMLFile[renderConfig](t.Context(), path)
		must.NoError(t, err)
		test.Eq(t, want, got)
	}
}

func TestRenderYAMLFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dev, prod := sampleRenderConfigs()
	devPath := filepath.Join(dir, "localdev.yaml")
	prodPath := filepath.Join(dir, "nested", "prod.yaml")

	must.NoError(t, RenderYAMLFiles(t.Context(), []Environment[renderConfig]{
		{Config: dev, Name: "localdev", Path: devPath},
		{Config: prod, Name: "production", Path: prodPath},
	}))

	for path, want := range map[string]*renderConfig{devPath: dev, prodPath: prod} {
		contents, err := os.ReadFile(path)
		must.NoError(t, err)
		test.True(t, strings.HasSuffix(string(contents), "\n"))
		test.False(t, strings.HasSuffix(string(contents), "\n\n"))

		got, err := LoadFromYAMLFile[renderConfig](t.Context(), path)
		must.NoError(t, err)
		test.Eq(t, want, got)
	}
}

// TestRenderJSONFiles_EnvDefaultOverlay pins the one documented gap in the
// round trip: the rendered file holds the value it was given, but the loader's
// env overlay resets a field carrying an envDefault whose var is unset.
func TestRenderJSONFiles_EnvDefaultOverlay(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.json")

	must.NoError(t, RenderJSONFiles(t.Context(), []Environment[sampleConfig]{
		{Config: &sampleConfig{Name: "rendered", Port: 9999}, Name: "localdev", Path: path},
	}))

	test.FileContains(t, path, `"Port": 9999`)

	cfg, err := LoadFromJSONFile[sampleConfig](t.Context(), path)
	must.NoError(t, err)
	test.EqOp(t, "rendered", cfg.Name)
	test.EqOp(t, 8080, cfg.Port) // reset to the envDefault, not the rendered 9999
}

func TestRenderJSONFiles_ValidatesEverythingFirst(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.json")
	invalidPath := filepath.Join(dir, "invalid.json")

	err := RenderJSONFiles(t.Context(), []Environment[validatableConfig]{
		{Config: &validatableConfig{Name: "ok"}, Name: "localdev", Path: validPath},
		{Config: &validatableConfig{}, Name: "production", Path: invalidPath},
	})
	must.Error(t, err)
	test.StrContains(t, err.Error(), "production")

	// The valid environment came first and still must not have been written: a
	// partial render is the failure this exists to prevent.
	test.FileNotExists(t, validPath)
	test.FileNotExists(t, invalidPath)
}

// unmarshalableConfig holds a field encoding/json refuses, so marshaling fails
// after validation has passed for every environment.
type unmarshalableConfig struct {
	Signal chan struct{} `json:"signal"`
}

func TestRenderJSONFiles_MarshalErrorWritesNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.json")
	secondPath := filepath.Join(dir, "second.json")

	err := RenderJSONFiles(t.Context(), []Environment[unmarshalableConfig]{
		{Config: &unmarshalableConfig{}, Name: "localdev", Path: firstPath},
		{Config: &unmarshalableConfig{}, Name: "production", Path: secondPath},
	})
	must.Error(t, err)
	test.StrContains(t, err.Error(), "marshaling localdev config")

	test.FileNotExists(t, firstPath)
	test.FileNotExists(t, secondPath)
}

func TestRenderJSONFiles_RejectsMalformedEnvironments(t *testing.T) {
	t.Parallel()

	dev, _ := sampleRenderConfigs()
	path := filepath.Join(t.TempDir(), "config.json")

	t.Run("no environments", func(t *testing.T) {
		t.Parallel()

		err := RenderJSONFiles(t.Context(), []Environment[renderConfig]{})
		must.Error(t, err)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputProvided)
	})

	t.Run("missing name", func(t *testing.T) {
		t.Parallel()

		err := RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Config: dev, Path: path},
		})
		must.Error(t, err)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	t.Run("missing path", func(t *testing.T) {
		t.Parallel()

		err := RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Config: dev, Name: "localdev"},
		})
		must.Error(t, err)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	t.Run("nil config", func(t *testing.T) {
		t.Parallel()

		err := RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Name: "localdev", Path: path},
		})
		must.Error(t, err)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	t.Run("two environments rendering to the same path", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		err := RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Config: dev, Name: "localdev", Path: filepath.Join(dir, "config.json")},
			{Config: dev, Name: "production", Path: filepath.Join(dir, ".", "config.json")},
		})
		must.Error(t, err)
		test.StrContains(t, err.Error(), "both render to")
	})
}

func TestRenderJSONFiles_Modes(t *testing.T) {
	t.Parallel()

	t.Run("defaults to owner-only", func(t *testing.T) {
		t.Parallel()

		dev, _ := sampleRenderConfigs()
		dir := t.TempDir()
		path := filepath.Join(dir, "nested", "config.json")

		must.NoError(t, RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Config: dev, Name: "localdev", Path: path},
		}))

		test.FileMode(t, path, 0o600)
		test.DirMode(t, filepath.Dir(path), os.ModeDir|0o750)
	})

	t.Run("honors WithFileMode and WithDirMode", func(t *testing.T) {
		t.Parallel()

		dev, _ := sampleRenderConfigs()
		dir := t.TempDir()
		path := filepath.Join(dir, "nested", "config.json")

		must.NoError(t, RenderJSONFiles(t.Context(),
			[]Environment[renderConfig]{{Config: dev, Name: "localdev", Path: path}},
			WithFileMode(0o644),
			WithDirMode(0o755),
			nil, // a nil option is ignored rather than panicking
		))

		test.FileMode(t, path, 0o644)
		test.DirMode(t, filepath.Dir(path), os.ModeDir|0o755)
	})
}

func TestRenderJSONFiles_WriteErrors(t *testing.T) {
	t.Parallel()

	t.Run("parent directory cannot be created", func(t *testing.T) {
		t.Parallel()

		dev, _ := sampleRenderConfigs()
		dir := t.TempDir()

		// A regular file where a directory needs to be makes MkdirAll fail.
		blocker := filepath.Join(dir, "blocker")
		must.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

		err := RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Config: dev, Name: "localdev", Path: filepath.Join(blocker, "config.json")},
		})
		must.Error(t, err)
		test.StrContains(t, err.Error(), "creating directory for localdev config")
	})

	t.Run("file cannot be written", func(t *testing.T) {
		t.Parallel()

		dev, _ := sampleRenderConfigs()
		dir := t.TempDir()

		// The path names an existing directory, so the write itself fails after
		// MkdirAll has happily confirmed the parent exists.
		target := filepath.Join(dir, "config.json")
		must.NoError(t, os.Mkdir(target, 0o750))

		err := RenderJSONFiles(t.Context(), []Environment[renderConfig]{
			{Config: dev, Name: "localdev", Path: target},
		})
		must.Error(t, err)
		test.StrContains(t, err.Error(), "writing localdev config")
	})
}

func TestRenderYAMLFiles_ValidationError(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")

	err := RenderYAMLFiles(t.Context(), []Environment[validatableConfig]{
		{Config: &validatableConfig{}, Name: "localdev", Path: path},
	})
	must.Error(t, err)
	test.FileNotExists(t, path)
}
