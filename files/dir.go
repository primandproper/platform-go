package files

import (
	"cmp"
	"context"
	"iter"
	"os"
	"path/filepath"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/tracing"
)

// Dir is a handle rooted at a base directory. Its methods take file names relative to that base, so
// the leading path is supplied once: OpenDir("a/b") then d.StreamChunks(ctx, "stuff.txt", 100) reads
// a/b/stuff.txt. Chdir navigates freely, including to sibling and parent directories. A Dir is not
// safe for concurrent Chdir; its read methods are safe to share once the base is stable.
type Dir struct {
	reader Reader
	base   string
}

// OpenDir opens a directory handle at path using the default (noop-observability) Reader.
func OpenDir(path string) (*Dir, error) {
	return newDir(path, defaultReader)
}

// NewDir opens a directory handle at path with the given observability dependencies.
func NewDir(path string, logger logging.Logger, tracerProvider tracing.TracerProvider) (*Dir, error) {
	return newDir(path, NewReader(logger, tracerProvider))
}

func newDir(path string, reader Reader) (*Dir, error) {
	base, err := resolveDir(path)
	if err != nil {
		return nil, err
	}

	return &Dir{base: base, reader: reader}, nil
}

// Path returns the current base directory, absolute.
func (d *Dir) Path() string {
	return d.base
}

// Resolve joins name onto the base directory. It is the escape hatch for the generic Decode helpers,
// which cannot be methods: files.DecodeFile[T](ctx, d.Resolve("config.yaml"), encoding.ContentTypeYAML).
func (d *Dir) Resolve(name string) string {
	return filepath.Join(d.base, name)
}

// Chdir navigates to rel (resolved against the current base; an absolute rel replaces it),
// validating it is a directory before adopting it. It mutates the handle.
func (d *Dir) Chdir(rel string) error {
	base, err := resolveDir(d.resolveArg(rel))
	if err != nil {
		return err
	}

	d.base = base

	return nil
}

// Sub is the non-mutating form of Chdir: it returns a new *Dir rooted at rel, sharing this Dir's
// Reader.
func (d *Dir) Sub(rel string) (*Dir, error) {
	base, err := resolveDir(d.resolveArg(rel))
	if err != nil {
		return nil, err
	}

	return &Dir{base: base, reader: d.reader}, nil
}

// Lines opens name (relative to the base) and yields each of its lines.
func (d *Dir) Lines(name string) (iter.Seq2[string, error], error) {
	return d.reader.LinesFile(d.Resolve(name))
}

// Chunks opens name (relative to the base) and yields chunks of up to n lines.
func (d *Dir) Chunks(name string, n int) (iter.Seq2[[]string, error], error) {
	return d.reader.ChunksFile(d.Resolve(name), n)
}

// SliceLines opens name (relative to the base) and returns up to count lines after skipping offset.
func (d *Dir) SliceLines(ctx context.Context, name string, offset, count int) ([]string, error) {
	return d.reader.SliceLinesFile(ctx, d.Resolve(name), offset, count)
}

// StreamChunks opens name (relative to the base) and streams its chunks of up to n lines.
func (d *Dir) StreamChunks(ctx context.Context, name string, n int) (<-chan ChunkResult, error) {
	return d.reader.StreamChunksFile(ctx, d.Resolve(name), n)
}

// resolveArg resolves a Chdir/Sub argument against the current base, leaving an absolute argument as
// given.
func (d *Dir) resolveArg(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}

	return filepath.Join(d.base, rel)
}

// resolveDir cleans path to an absolute form and verifies it is a directory. filepath.Abs only fails
// when the working directory cannot be determined, in which case its empty result also fails the
// Stat below — so both surface through one error path rather than a separate, unreachable branch.
func resolveDir(path string) (string, error) {
	abs, absErr := filepath.Abs(path)

	info, statErr := os.Stat(abs)
	if err := cmp.Or(absErr, statErr); err != nil {
		return "", errors.Wrap(err, "resolving directory")
	}

	if !info.IsDir() {
		return "", errors.Newf("%q is not a directory", abs)
	}

	return abs, nil
}
