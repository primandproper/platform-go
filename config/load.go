package config

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/joho/godotenv"
)

// LoadFromEnvironment builds a *T populated entirely from environment variables.
func LoadFromEnvironment[T any](opts ...Option) (*T, error) {
	cfg := new(T)
	if err := ApplyEnvironmentVariables(cfg, opts...); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadFromJSONFile decodes the JSON file at path into a *T, then overlays
// environment variables on top of it via ApplyEnvironmentVariables. A set env
// var takes precedence over the file value. Note the caarlos0/env caveat
// documented on ApplyEnvironmentVariables: a field carrying an envDefault whose
// env var is unset is reset to that default even if the file supplied a value,
// so give such fields their env var (or no envDefault) when the file should win.
func LoadFromJSONFile[T any](ctx context.Context, path string, opts ...Option) (*T, error) {
	return loadFile[T](ctx, path, encoding.ContentTypeJSON, opts...)
}

// LoadFromTOMLFile behaves like LoadFromJSONFile but decodes a TOML file. TOML
// keys map to struct fields by their `toml:` tag, falling back to a
// case-insensitive field-name match.
func LoadFromTOMLFile[T any](ctx context.Context, path string, opts ...Option) (*T, error) {
	return loadFile[T](ctx, path, encoding.ContentTypeTOML, opts...)
}

// LoadFromYAMLFile behaves like LoadFromJSONFile but decodes a YAML file. YAML
// keys map to struct fields by their `yaml:` tag, falling back to the
// lower-cased field name.
func LoadFromYAMLFile[T any](ctx context.Context, path string, opts ...Option) (*T, error) {
	return loadFile[T](ctx, path, encoding.ContentTypeYAML, opts...)
}

// loadFile reads the file at path, decodes it as ct into a fresh *T, then
// overlays environment variables.
//
// The decode goes through encoding rather than through the three unmarshalers
// directly, so that a config file and everything else this module reads off a
// wire are parsed by one dispatch. The alternative was three format-specific
// imports here and three more anywhere else the same question came up, drifting
// a decoder at a time — this package's job is where a config comes from, not
// what JSON means.
func loadFile[T any](ctx context.Context, path string, ct encoding.ContentType, opts ...Option) (*T, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "reading config file %q", path)
	}

	cfg := new(T)
	if err = encoding.NewClientEncoder(ct).Unmarshal(ctx, contents, cfg); err != nil {
		return nil, errors.Wrapf(err, "decoding %s config file %q", ct, path)
	}

	if err = ApplyEnvironmentVariables(cfg, opts...); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadFromDotEnvFile loads the .env file at path into the process environment
// and then builds a *T from the environment. godotenv does not override
// variables already present in the process, so real environment values still
// take precedence over values in the file.
func LoadFromDotEnvFile[T any](path string, opts ...Option) (*T, error) {
	if err := godotenv.Load(path); err != nil {
		return nil, errors.Wrapf(err, "loading .env file %q", path)
	}

	return LoadFromEnvironment[T](opts...)
}

// ResolveDotEnvPath joins baseDir and filename and returns the result if that
// file exists. A missing file yields "" (and a nil error) so callers can treat
// "no .env present" as "skip loading" rather than a failure; any other stat
// error is returned.
func ResolveDotEnvPath(baseDir, filename string) (string, error) {
	path := filepath.Join(baseDir, filename)

	switch _, err := os.Stat(path); {
	case err == nil:
		return path, nil
	case stderrors.Is(err, os.ErrNotExist):
		return "", nil
	default:
		return "", errors.Wrapf(err, "checking for .env file %q", path)
	}
}

// Validate runs cfg's context-aware validation if cfg implements
// ozzo-validation's ValidatableWithContext; otherwise it is a no-op. It is a
// convenience for validating a freshly loaded config, particularly one built
// solely from the environment where there is no file baseline to fall back on.
func Validate(ctx context.Context, cfg any) error {
	if v, ok := cfg.(validation.ValidatableWithContext); ok {
		if err := v.ValidateWithContext(ctx); err != nil {
			return errors.Wrap(err, "validating config")
		}
	}

	return nil
}
