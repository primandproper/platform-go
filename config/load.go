package config

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"os"
	"path/filepath"

	"github.com/primandproper/platform-go/v4/errors"

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
func LoadFromJSONFile[T any](path string, opts ...Option) (*T, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "reading config file %q", path)
	}

	cfg := new(T)
	if err = json.Unmarshal(contents, cfg); err != nil {
		return nil, errors.Wrapf(err, "decoding config file %q", path)
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
