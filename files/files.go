package files

import (
	"context"
	"io"
	"io/fs"
	"iter"

	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/tracing"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"
)

const o11yName = "files_reader"

var (
	_ Reader = (*standardReader)(nil)

	defaultReader = newStandardReader(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
)

type (
	// Reader reads files by name, with observability around each operation. The line-iterator
	// methods take no context: they are pull-driven, so the consumer's range/break owns
	// cancellation. The methods that read eagerly or stream take a context and open a span.
	Reader interface {
		LinesFile(name string) (iter.Seq2[string, error], error)
		ChunksFile(name string, n int) (iter.Seq2[[]string, error], error)
		SliceLinesFile(ctx context.Context, name string, offset, count int) ([]string, error)
		StreamChunksFile(ctx context.Context, name string, n int) (<-chan ChunkResult, error)
	}

	standardReader struct {
		fsys           fs.FS
		o11y           observability.Observer
		logger         logging.Logger
		tracerProvider tracing.TracerProvider
	}
)

// newStandardReader builds a Reader over the OS filesystem. It keeps the raw logger and tracer
// provider around so the decode helpers can build an encoding.ClientEncoder that traces under the
// same tracer as the file read.
func newStandardReader(logger logging.Logger, tracerProvider tracing.TracerProvider) *standardReader {
	return newStandardReaderFS(osFS{}, logger, tracerProvider)
}

// newStandardReaderFS is newStandardReader over an arbitrary fs.FS.
func newStandardReaderFS(fsys fs.FS, logger logging.Logger, tracerProvider tracing.TracerProvider) *standardReader {
	return &standardReader{
		fsys:           fsys,
		o11y:           observability.NewObserver(o11yName, logger, tracerProvider),
		logger:         logger,
		tracerProvider: tracerProvider,
	}
}

// NewReader builds a Reader that opens files on the OS filesystem, accepting any path os.Open would
// (including absolute paths and ".."). Use NewReaderFS to read from an embed.FS or other fs.FS.
func NewReader(logger logging.Logger, tracerProvider tracing.TracerProvider) Reader {
	return newStandardReader(logger, tracerProvider)
}

// NewReaderFS builds a Reader that opens files through fsys — an embed.FS, fstest.MapFS, os.DirFS,
// archive/zip.Reader, or any other fs.FS. Names are interpreted by fsys and so must satisfy
// fs.ValidPath (slash-separated, unrooted, no "."/".." elements). Every read, slice, stream, and
// decode behavior is identical to NewReader; only the open is redirected.
func NewReaderFS(fsys fs.FS, logger logging.Logger, tracerProvider tracing.TracerProvider) Reader {
	return newStandardReaderFS(fsys, logger, tracerProvider)
}

// closeQuietly closes c, logging any error. Closing a file opened only for reading practically never
// fails and there is nothing actionable to do if it does, so the error is logged rather than returned.
func (r *standardReader) closeQuietly(c io.Closer) {
	if err := c.Close(); err != nil {
		r.logger.Error("closing file", err)
	}
}

// LinesFile opens name and yields each of its lines. The open error is returned up front; read
// errors are yielded by the iterator. The file is closed when iteration ends or the caller breaks.
func LinesFile(name string) (iter.Seq2[string, error], error) {
	return defaultReader.LinesFile(name)
}

// ChunksFile opens name and yields successive slices of up to n lines.
func ChunksFile(name string, n int) (iter.Seq2[[]string, error], error) {
	return defaultReader.ChunksFile(name, n)
}

// SliceLinesFile opens name and returns up to count lines after skipping offset lines.
func SliceLinesFile(ctx context.Context, name string, offset, count int) ([]string, error) {
	return defaultReader.SliceLinesFile(ctx, name, offset, count)
}

// StreamChunksFile opens name and streams chunks of up to n lines on the returned channel.
func StreamChunksFile(ctx context.Context, name string, n int) (<-chan ChunkResult, error) {
	return defaultReader.StreamChunksFile(ctx, name, n)
}

// MustLinesFile is like LinesFile but panics on an open error.
func MustLinesFile(name string) iter.Seq2[string, error] {
	seq, err := LinesFile(name)
	if err != nil {
		panic(err)
	}

	return seq
}

// MustChunksFile is like ChunksFile but panics on an open error.
func MustChunksFile(name string, n int) iter.Seq2[[]string, error] {
	seq, err := ChunksFile(name, n)
	if err != nil {
		panic(err)
	}

	return seq
}
