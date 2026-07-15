package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestLoadFromEnvironment(t *testing.T) {
	t.Setenv("NAME", "fromenv")

	cfg, err := LoadFromEnvironment[sampleConfig]()
	must.NoError(t, err)
	must.NotNil(t, cfg)
	test.EqOp(t, "fromenv", cfg.Name)
	test.EqOp(t, 8080, cfg.Port)
}

// TestLoadFromJSONFile_OverlaysEnvironment uses t.Setenv and therefore runs
// serially, separate from the parallel subtests below.
func TestLoadFromJSONFile_OverlaysEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	must.NoError(t, os.WriteFile(path, []byte(`{"Name":"fromfile","Verbose":true}`), 0o600))

	// NAME is set in the environment and overrides the file value; Verbose has
	// no env var (and no default) so the file value survives; Port has no file
	// value and takes its envDefault.
	t.Setenv("NAME", "fromenv")

	cfg, err := LoadFromJSONFile[sampleConfig](path)
	must.NoError(t, err)
	test.EqOp(t, "fromenv", cfg.Name)
	test.EqOp(t, true, cfg.Verbose)
	test.EqOp(t, 8080, cfg.Port)
}

func TestLoadFromJSONFile(t *testing.T) {
	t.Parallel()

	t.Run("errors on missing file", func(t *testing.T) {
		t.Parallel()

		_, err := LoadFromJSONFile[sampleConfig](filepath.Join(t.TempDir(), "nope.json"))
		test.Error(t, err)
	})

	t.Run("errors on invalid JSON", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "bad.json")
		must.NoError(t, os.WriteFile(path, []byte(`{not json`), 0o600))

		_, err := LoadFromJSONFile[sampleConfig](path)
		test.Error(t, err)
	})
}

func TestLoadFromTOMLFile(t *testing.T) {
	t.Parallel()

	t.Run("decodes and applies defaults", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		must.NoError(t, os.WriteFile(path, []byte("name = \"fromfile\"\nverbose = true\n"), 0o600))

		cfg, err := LoadFromTOMLFile[sampleConfig](path)
		must.NoError(t, err)
		test.EqOp(t, "fromfile", cfg.Name)
		test.EqOp(t, true, cfg.Verbose)
		test.EqOp(t, 8080, cfg.Port) // from envDefault
	})

	t.Run("errors on invalid TOML", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "bad.toml")
		must.NoError(t, os.WriteFile(path, []byte("name = = broken"), 0o600))

		_, err := LoadFromTOMLFile[sampleConfig](path)
		test.Error(t, err)
	})
}

func TestLoadFromYAMLFile(t *testing.T) {
	t.Parallel()

	t.Run("decodes and applies defaults", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		must.NoError(t, os.WriteFile(path, []byte("name: fromfile\nverbose: true\n"), 0o600))

		cfg, err := LoadFromYAMLFile[sampleConfig](path)
		must.NoError(t, err)
		test.EqOp(t, "fromfile", cfg.Name)
		test.EqOp(t, true, cfg.Verbose)
		test.EqOp(t, 8080, cfg.Port) // from envDefault
	})

	t.Run("errors on invalid YAML", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "bad.yaml")
		must.NoError(t, os.WriteFile(path, []byte("name: : :\n\t- broken"), 0o600))

		_, err := LoadFromYAMLFile[sampleConfig](path)
		test.Error(t, err)
	})
}

// dotEnvConfig uses env keys unique to the dotenv test. godotenv.Load mutates
// the real process environment (not a test-scoped copy), so distinct keys keep
// it from leaking into the other tests when they run in a shuffled order.
type dotEnvConfig struct {
	Name string `env:"DOTENV_NAME"`
	Port int    `env:"DOTENV_PORT" envDefault:"8080"`
}

//nolint:paralleltest // godotenv.Load mutates the process environment; must run serially.
func TestLoadFromDotEnvFile(t *testing.T) {
	t.Cleanup(func() {
		os.Unsetenv("DOTENV_NAME")
		os.Unsetenv("DOTENV_PORT")
	})

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	must.NoError(t, os.WriteFile(path, []byte("DOTENV_NAME=fromdotenv\nDOTENV_PORT=1234\n"), 0o600))

	cfg, err := LoadFromDotEnvFile[dotEnvConfig](path)
	must.NoError(t, err)
	test.EqOp(t, "fromdotenv", cfg.Name)
	test.EqOp(t, 1234, cfg.Port)
}

func TestResolveDotEnvPath(t *testing.T) {
	t.Parallel()

	t.Run("returns path when file exists", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		must.NoError(t, os.WriteFile(path, []byte("NAME=x\n"), 0o600))

		resolved, err := ResolveDotEnvPath(dir, ".env")
		must.NoError(t, err)
		test.EqOp(t, path, resolved)
	})

	t.Run("returns empty string when file is absent", func(t *testing.T) {
		t.Parallel()

		resolved, err := ResolveDotEnvPath(t.TempDir(), ".env")
		must.NoError(t, err)
		test.EqOp(t, "", resolved)
	})
}

type validatableConfig struct {
	Name string `env:"NAME"`
}

func (c *validatableConfig) ValidateWithContext(_ context.Context) error {
	if c.Name == "" {
		return validation.NewError("name_required", "name is required")
	}

	return nil
}

func TestValidate(t *testing.T) {
	t.Parallel()

	t.Run("passes for a valid config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, Validate(context.Background(), &validatableConfig{Name: "ok"}))
	})

	t.Run("fails for an invalid config", func(t *testing.T) {
		t.Parallel()

		test.Error(t, Validate(context.Background(), &validatableConfig{}))
	})

	t.Run("is a no-op for a non-validatable config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, Validate(context.Background(), &sampleConfig{}))
	})
}
