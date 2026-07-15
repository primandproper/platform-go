package config

import (
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type sampleConfig struct {
	Name   string `env:"NAME"`
	Nested struct {
		Token string `env:"TOKEN"`
	} `envPrefix:"NESTED_"`
	Port    int  `env:"PORT"    envDefault:"8080"`
	Verbose bool `env:"VERBOSE"`
}

func TestApplyEnvironmentVariables(t *testing.T) {
	t.Run("populates fields and applies defaults", func(t *testing.T) {
		t.Setenv("NAME", "widget")
		t.Setenv("VERBOSE", "true")

		var cfg sampleConfig
		must.NoError(t, ApplyEnvironmentVariables(&cfg))

		test.EqOp(t, "widget", cfg.Name)
		test.EqOp(t, 8080, cfg.Port) // from envDefault
		test.EqOp(t, true, cfg.Verbose)
	})

	t.Run("respects prefix", func(t *testing.T) {
		t.Setenv("MYAPP_NAME", "prefixed")
		t.Setenv("MYAPP_NESTED_TOKEN", "sekret")

		var cfg sampleConfig
		must.NoError(t, ApplyEnvironmentVariables(&cfg, WithPrefix("MYAPP_")))

		test.EqOp(t, "prefixed", cfg.Name)
		test.EqOp(t, "sekret", cfg.Nested.Token)
	})

	t.Run("leaves a field with no env var and no default untouched", func(t *testing.T) { //nolint:paralleltest // shares process env with sibling subtests.
		// Name has no envDefault, so an unset NAME leaves the existing value in place.
		cfg := sampleConfig{Name: "preexisting"}
		must.NoError(t, ApplyEnvironmentVariables(&cfg))

		test.EqOp(t, "preexisting", cfg.Name)
	})

	t.Run("envDefault overrides a preexisting value when the env var is unset", func(t *testing.T) { //nolint:paralleltest // shares process env with sibling subtests.
		// Port carries an envDefault, so per caarlos0/env semantics the default
		// wins over the initialized value when PORT is not set.
		cfg := sampleConfig{Port: 5000}
		must.NoError(t, ApplyEnvironmentVariables(&cfg))

		test.EqOp(t, 8080, cfg.Port)
	})

	t.Run("invokes OnSet hook", func(t *testing.T) {
		t.Setenv("NAME", "observed")

		var tags []string
		var cfg sampleConfig
		must.NoError(t, ApplyEnvironmentVariables(&cfg, WithOnSet(func(tag string, _ any, _ bool) {
			tags = append(tags, tag)
		})))

		test.SliceContains(t, tags, "NAME")
	})

	t.Run("returns an error for a non-pointer", func(t *testing.T) { //nolint:paralleltest // shares process env with sibling subtests.
		var cfg sampleConfig
		test.Error(t, ApplyEnvironmentVariables(cfg))
	})
}
