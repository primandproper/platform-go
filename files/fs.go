package files

import (
	"context"
	"io/fs"
	"iter"
	"os"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
)

// osFS opens files on the OS filesystem via os.Open. Unlike a general fs.FS it deliberately does not
// enforce fs.ValidPath, so absolute paths and ".." keep working exactly as they did before the fs.FS
// seam existed. It is the backing store for every Reader built by NewReader.
type osFS struct{}

// Open satisfies fs.FS. *os.File already satisfies fs.File, so os.Open's result is returned directly.
func (osFS) Open(name string) (fs.File, error) {
	return os.Open(name)
}

// ReadFile satisfies fs.ReadFileFS so fs.ReadFile keeps using os.ReadFile for the default Reader,
// preserving the exact whole-file read (and absolute-path support) it had before the fs.FS seam.
func (osFS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

// FS is a handle rooted at an fs.FS: the read-only analog of Dir for virtual filesystems such as
// embed.FS. Its methods take names relative to the root and, because the root is an fs.FS, those
// names must satisfy fs.ValidPath (slash-separated, unrooted, no "."/".." elements). Sub descends
// into a subtree via fs.Sub. An FS is safe to share once constructed; Sub returns a new handle
// rather than mutating this one.
type FS struct {
	reader *standardReader
}

// OpenFS returns a handle over fsys using noop observability, mirroring OpenDir.
func OpenFS(fsys fs.FS) *FS {
	return &FS{reader: newStandardReaderFS(fsys, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())}
}

// NewFS returns a handle over fsys with the given observability dependencies, mirroring NewDir.
func NewFS(fsys fs.FS, logger logging.Logger, tracerProvider tracing.TracerProvider) *FS {
	return &FS{reader: newStandardReaderFS(fsys, logger, tracerProvider)}
}

// FS returns the underlying fs.FS. It is the escape hatch for the generic decode helpers, which
// cannot be methods: files.DecodeFileFS[T](ctx, d.FS(), "config.yaml", encoding.ContentTypeYAML).
func (d *FS) FS() fs.FS {
	return d.reader.fsys
}

// Sub returns a new handle rooted at the dir subtree via fs.Sub, sharing this handle's observability.
func (d *FS) Sub(dir string) (*FS, error) {
	sub, err := fs.Sub(d.reader.fsys, dir)
	if err != nil {
		return nil, errors.Wrap(err, "descending into subtree")
	}

	return &FS{reader: newStandardReaderFS(sub, d.reader.logger, d.reader.tracerProvider)}, nil
}

// Lines opens name (relative to the root) and yields each of its lines.
func (d *FS) Lines(name string) (iter.Seq2[string, error], error) {
	return d.reader.LinesFile(name)
}

// Chunks opens name (relative to the root) and yields chunks of up to n lines.
func (d *FS) Chunks(name string, n int) (iter.Seq2[[]string, error], error) {
	return d.reader.ChunksFile(name, n)
}

// SliceLines opens name (relative to the root) and returns up to count lines after skipping offset.
func (d *FS) SliceLines(ctx context.Context, name string, offset, count int) ([]string, error) {
	return d.reader.SliceLinesFile(ctx, name, offset, count)
}

// StreamChunks opens name (relative to the root) and streams its chunks of up to n lines.
func (d *FS) StreamChunks(ctx context.Context, name string, n int) (<-chan ChunkResult, error) {
	return d.reader.StreamChunksFile(ctx, name, n)
}
