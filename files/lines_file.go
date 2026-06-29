package files

import (
	"context"
	"iter"
	"os"

	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability/keys"
)

// LinesFile opens name and yields each of its lines. The open error is returned up front; any read
// error is yielded by the iterator. The file is closed when iteration is exhausted or the caller
// breaks out of the range.
func (r *standardReader) LinesFile(name string) (iter.Seq2[string, error], error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, errors.Wrap(err, "opening file")
	}

	return func(yield func(string, error) bool) {
		defer r.closeQuietly(f)

		for line, lineErr := range Lines(f) {
			if !yield(line, lineErr) {
				return
			}
		}
	}, nil
}

// ChunksFile opens name and yields successive slices of up to n lines, closing the file when
// iteration ends or the caller breaks.
func (r *standardReader) ChunksFile(name string, n int) (iter.Seq2[[]string, error], error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, errors.Wrap(err, "opening file")
	}

	return func(yield func([]string, error) bool) {
		defer r.closeQuietly(f)

		for chunk, chunkErr := range Chunks(f, n) {
			if !yield(chunk, chunkErr) {
				return
			}
		}
	}, nil
}

// SliceLinesFile opens name and returns up to count lines after skipping offset lines.
func (r *standardReader) SliceLinesFile(ctx context.Context, name string, offset, count int) ([]string, error) {
	_, op := r.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.FilenameKey, name)

	f, err := os.Open(name)
	if err != nil {
		return nil, op.Error(err, "opening file")
	}
	defer r.closeQuietly(f)

	out, err := SliceLines(f, offset, count)
	if err != nil {
		return nil, op.Error(err, "slicing lines")
	}

	return out, nil
}
