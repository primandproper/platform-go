package config

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/errors"
)

const (
	// jsonIndent is the indentation the JSON renderer uses. It is fixed rather
	// than an option because the output's whole purpose is to be checked in and
	// read in a diff, and a file whose indentation depends on which call site
	// last rendered it produces a diff that is all whitespace.
	jsonIndent = "\t"

	// defaultRenderFileMode is the mode a rendered file is created with. It is
	// owner-only because that is the safe thing to create a file as; a repo
	// checking these in and expecting other users to read them wants
	// WithFileMode(0o644). Note that os.WriteFile does not chmod a file that
	// already exists, so this applies on first render only.
	defaultRenderFileMode fs.FileMode = 0o600

	// defaultRenderDirMode is the mode any parent directory is created with.
	defaultRenderDirMode fs.FileMode = 0o750
)

// Environment pairs one named configuration object with the path it renders to.
// It is the unit the render functions operate on: the Config is built in Go, so
// the file on disk is a projection of a real, compiled, validated object rather
// than hand-maintained text that drifts from the struct it is decoded into.
type Environment[T any] struct {
	// Config is this environment's configuration object. It must not be nil.
	Config *T

	// Name labels the environment in error messages (e.g. "localdev"). It must
	// not be empty — every failure the render functions report names the
	// environment it came from, and an unnamed one makes those unreadable.
	Name string

	// Path is where the rendered file is written. Parent directories are
	// created as needed. It must not be empty, and no two environments in one
	// call may resolve to the same path.
	Path string
}

// RenderOption configures how the render functions write their files.
//
// It is deliberately not Option. Option configures the environment-variable
// overlay the loaders apply on the way in, and neither WithPrefix nor WithOnSet
// means anything on the way out; sharing the type would buy symmetry at the
// price of accepting two options and silently ignoring them.
type RenderOption func(*renderOptions)

type renderOptions struct {
	dirMode  fs.FileMode
	fileMode fs.FileMode
}

// WithFileMode sets the mode rendered files are created with, overriding the
// owner-only default. It has no effect on a file that already exists.
func WithFileMode(mode fs.FileMode) RenderOption {
	return func(o *renderOptions) { o.fileMode = mode }
}

// WithDirMode sets the mode parent directories are created with, overriding the
// owner-and-group default.
func WithDirMode(mode fs.FileMode) RenderOption {
	return func(o *renderOptions) { o.dirMode = mode }
}

func newRenderOptions(opts ...RenderOption) *renderOptions {
	o := &renderOptions{
		dirMode:  defaultRenderDirMode,
		fileMode: defaultRenderFileMode,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// RenderJSONFiles writes each environment's Config to its Path as indented
// JSON, and is the inverse of LoadFromJSONFile: what it writes, that function
// reads back into an equal *T.
//
// The one thing that breaks the round trip is the caarlos0/env caveat
// LoadFromJSONFile already documents. The overlay runs after the decode, so a
// field carrying an envDefault whose env var is unset comes back as that
// default no matter what was rendered. That is a property of loading, not of
// rendering — the file on disk holds the value it was given either way — but a
// test asserting Render-then-Load equality has to account for it.
//
// Nothing is written until every environment has been validated and marshaled,
// so a config that fails either fails the whole call rather than landing a
// broken file next to updated ones. Validation goes through Validate, which is
// a no-op for a T that does not implement ozzo-validation's
// ValidatableWithContext: there is a compiler behind the file regardless, but
// only a validatable T gets the second guarantee.
//
// The output is stable across runs — struct fields render in declaration order
// and encoding/json sorts map keys — and ends in exactly one newline.
func RenderJSONFiles[T any](ctx context.Context, envs []Environment[T], opts ...RenderOption) error {
	return render(ctx, envs, encoding.ContentTypeJSON, opts...)
}

// RenderTOMLFiles behaves like RenderJSONFiles but writes TOML, inverting
// LoadFromTOMLFile. Fields render under their `toml:` tag.
func RenderTOMLFiles[T any](ctx context.Context, envs []Environment[T], opts ...RenderOption) error {
	return render(ctx, envs, encoding.ContentTypeTOML, opts...)
}

// RenderYAMLFiles behaves like RenderJSONFiles but writes YAML, inverting
// LoadFromYAMLFile. Fields render under their `yaml:` tag.
func RenderYAMLFiles[T any](ctx context.Context, envs []Environment[T], opts ...RenderOption) error {
	return render(ctx, envs, encoding.ContentTypeYAML, opts...)
}

// render is the body all three renderers share, in three passes: check the
// environments, validate and marshal every one of them, and only then touch the
// disk.
//
// The passes are what makes the guarantee worth having. A single loop that
// validated, marshaled, and wrote one environment at a time would leave the
// first two files updated and the third stale the moment the third config is
// invalid, which is the failure this exists to prevent. Writing is still not
// atomic across files — nothing short of a staging directory would make it so —
// but by the time the first byte is written the only errors left are the disk's.
//
// The marshal goes through encoding for the same reason loadFile's decode does:
// one dispatch for every format this module reads or writes, so a renderer and
// its loader cannot drift a marshaler apart.
func render[T any](ctx context.Context, envs []Environment[T], ct encoding.ContentType, opts ...RenderOption) error {
	if len(envs) == 0 {
		return errors.Wrap(errors.ErrEmptyInputProvided, "no environments to render")
	}

	o := newRenderOptions(opts...)

	namesByPath := make(map[string]string, len(envs))
	for i := range envs {
		env := &envs[i]

		switch {
		case env.Name == "":
			return errors.Wrapf(errors.ErrEmptyInputParameter, "environment at index %d has no name", i)
		case env.Path == "":
			return errors.Wrapf(errors.ErrEmptyInputParameter, "environment %q has no path", env.Name)
		case env.Config == nil:
			return errors.Wrapf(errors.ErrNilInputParameter, "environment %q has no config", env.Name)
		}

		cleaned := filepath.Clean(env.Path)
		if other, ok := namesByPath[cleaned]; ok {
			return errors.Newf("environments %q and %q both render to %q", other, env.Name, cleaned)
		}

		namesByPath[cleaned] = env.Name
	}

	encoder := encoding.NewClientEncoder(ct)

	rendered := make([][]byte, len(envs))
	for i := range envs {
		env := &envs[i]

		if err := Validate(ctx, env.Config); err != nil {
			return errors.Wrapf(err, "environment %q", env.Name)
		}

		data, err := encoder.Marshal(ctx, env.Config)
		if err != nil {
			return errors.Wrapf(err, "marshaling %s config", env.Name)
		}

		if ct == encoding.ContentTypeJSON {
			// encoding marshals to the most compact form every content type has,
			// which is the right answer for a wire payload and the wrong one for a
			// file a human reviews. Re-indenting here rather than reaching for
			// json.MarshalIndent keeps the bytes the same ones encoding produced.
			var indented bytes.Buffer
			if err = json.Indent(&indented, data, "", jsonIndent); err != nil {
				return errors.Wrapf(err, "indenting %s config", env.Name)
			}

			data = indented.Bytes()
		}

		// TOML and YAML marshalers already end in a newline and JSON does not, so
		// normalize rather than append: exactly one, whatever the format.
		rendered[i] = append(bytes.TrimRight(data, "\n"), '\n')
	}

	for i := range envs {
		env := &envs[i]

		if err := os.MkdirAll(filepath.Dir(env.Path), o.dirMode); err != nil {
			return errors.Wrapf(err, "creating directory for %s config", env.Name)
		}

		if err := os.WriteFile(env.Path, rendered[i], o.fileMode); err != nil {
			return errors.Wrapf(err, "writing %s config", env.Name)
		}
	}

	return nil
}
