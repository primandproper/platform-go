package files

import (
	"context"
	"io"
	"os"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/keys"
)

// ChunkResult is one item streamed by StreamChunks: either a chunk of up to n lines, or a terminal
// Err. A non-nil Err is always the last value sent before the channel closes.
type ChunkResult struct {
	Err   error
	Lines []string
}

// StreamChunks reads src in a background goroutine and sends each chunk of up to n lines on the
// returned channel, which StreamChunks owns and closes. A synchronous error is returned for setup
// failures (n <= 0), in which case no goroutine is started and the channel is nil. Once streaming,
// a mid-stream read error or context cancellation is delivered as the final ChunkResult.Err and the
// channel is then closed; io.EOF is clean completion (the channel simply closes).
func StreamChunks(ctx context.Context, src io.Reader, n int) (<-chan ChunkResult, error) {
	return defaultReader.streamChunks(ctx, "", src, n, nil)
}

// StreamChunksFile opens name and streams its chunks like StreamChunks. The file is closed when
// streaming finishes. Setup failures (the file failing to open, or n <= 0) are returned
// synchronously with a nil channel and no goroutine; streamChunks is the single chunk-size
// validator and closes the file via cleanup if it rejects n.
func (r *standardReader) StreamChunksFile(ctx context.Context, name string, n int) (<-chan ChunkResult, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, errors.Wrap(err, "opening file")
	}

	return r.streamChunks(ctx, name, f, n, func() { r.closeQuietly(f) })
}

// streamChunks is the shared engine behind StreamChunks and StreamChunksFile. cleanup, if non-nil,
// runs once the goroutine exits (used to close a file the caller no longer owns).
func (r *standardReader) streamChunks(ctx context.Context, name string, src io.Reader, n int, cleanup func()) (<-chan ChunkResult, error) {
	if n <= 0 {
		if cleanup != nil {
			cleanup()
		}

		return nil, ErrNonPositiveChunkSize
	}

	_, op := r.o11y.Begin(ctx)
	op.Set(keys.LengthKey, n)
	if name != "" {
		op.Set(keys.FilenameKey, name)
	}

	out := make(chan ChunkResult)

	go func() {
		defer op.End()
		if cleanup != nil {
			defer cleanup()
		}
		defer close(out)

		for chunk, err := range Chunks(src, n) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				trySend(out, ChunkResult{Err: op.Error(ctxErr, "streaming canceled")})

				return
			}

			res := ChunkResult{Lines: chunk}
			if err != nil {
				res = ChunkResult{Err: op.Error(err, "reading chunk")}
			}

			select {
			case out <- res:
			case <-ctx.Done():
				trySend(out, ChunkResult{Err: op.Error(ctx.Err(), "streaming canceled")})

				return
			}

			if err != nil {
				return
			}
		}
	}()

	return out, nil
}

// trySend delivers res to a consumer that is still receiving, without blocking if it has walked away
// (the common case once the context is canceled).
func trySend(out chan<- ChunkResult, res ChunkResult) {
	select {
	case out <- res:
	default:
	}
}
