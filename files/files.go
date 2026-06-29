package files

import (
	"context"
	"iter"
	"os"

	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/tracing"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"
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
		o11y           observability.Observer
		logger         logging.Logger
		tracerProvider tracing.TracerProvider
	}
)

// newStandardReader keeps the raw logger and tracer provider around so the decode helpers can
// build an encoding.ClientEncoder that traces under the same tracer as the file read.
func newStandardReader(logger logging.Logger, tracerProvider tracing.TracerProvider) *standardReader {
	return &standardReader{
		o11y:           observability.NewObserver(o11yName, logger, tracerProvider),
		logger:         logger,
		tracerProvider: tracerProvider,
	}
}

// NewReader builds a new Reader.
func NewReader(logger logging.Logger, tracerProvider tracing.TracerProvider) Reader {
	return newStandardReader(logger, tracerProvider)
}

// closeQuietly closes f, logging any error. Closing a file opened only for reading practically never
// fails and there is nothing actionable to do if it does, so the error is logged rather than returned.
func (r *standardReader) closeQuietly(f *os.File) {
	if err := f.Close(); err != nil {
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
