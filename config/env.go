// Package config provides helpers for populating configuration structs from
// environment variables, .env files, and JSON files. It builds on the
// caarlos0/env struct-tag conventions (`env:`, `envPrefix:`, `envDefault:`,
// ...) already used throughout platform-go's per-package config subpackages,
// giving callers a single, application-agnostic place to mount those tags onto
// their config values.
package config

import (
	"github.com/primandproper/platform-go/v4/errors"

	"github.com/caarlos0/env/v11"
)

// OnSetFunc is invoked for each field the parser populates from the
// environment. It mirrors caarlos0/env's OnSet hook and is handy for debug
// logging which variables were applied. Wire it to any logger via WithOnSet.
type OnSetFunc func(tag string, value any, isDefault bool)

type options struct {
	onSet  OnSetFunc
	prefix string
}

// Option configures how environment variables are applied.
type Option func(*options)

// WithPrefix sets a prefix prepended to every env var key the parser reads
// (e.g. "MYAPP_"). Nested envPrefix struct tags are appended after it.
func WithPrefix(prefix string) Option {
	return func(o *options) { o.prefix = prefix }
}

// WithOnSet registers a hook invoked for each field populated from the
// environment. Passing nil is a no-op.
func WithOnSet(fn OnSetFunc) Option {
	return func(o *options) { o.onSet = fn }
}

func newOptions(opts ...Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// ApplyEnvironmentVariables populates cfg (a non-nil pointer to a struct) from
// environment variables using the caarlos0/env struct tags, following that
// library's standard semantics:
//
//   - A field whose env var is set is assigned that value, overriding any value
//     the field already held (e.g. one decoded from a file).
//   - A field with an envDefault whose env var is unset is assigned the default,
//     which likewise overrides any pre-existing value. Because of this, when
//     layering env vars on top of a decoded config, a field carrying an
//     envDefault always ends up at either its env value or its default — never a
//     value that came only from the file.
//   - A field with no env var and no envDefault is left untouched.
func ApplyEnvironmentVariables(cfg any, opts ...Option) error {
	o := newOptions(opts...)

	envOpts := env.Options{Prefix: o.prefix}
	if o.onSet != nil {
		envOpts.OnSet = env.OnSetFn(o.onSet)
	}

	if err := env.ParseWithOptions(cfg, envOpts); err != nil {
		return errors.Wrap(err, "applying environment variables")
	}

	return nil
}
