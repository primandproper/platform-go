package files

import (
	"bufio"
	stderrors "errors"
	"io"
	"iter"
	"strings"

	"github.com/primandproper/platform-go/v5/errors"
)

// Lines yields each line of r without its trailing newline (handling both \n and \r\n). An
// unterminated final line is still yielded. A terminal read error is yielded once, as the second
// value after the last good line; io.EOF is normal completion and is never surfaced.
//
// Lines reads with a bufio.Reader rather than a bufio.Scanner, so there is no 64KB line-length cap.
func Lines(r io.Reader) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				if stderrors.Is(err, io.EOF) {
					// A final unterminated line is legitimate; yield it before completing.
					if line != "" {
						yield(trimLineEnding(line), nil)
					}
					return
				}

				// A real read failure: the partial data ReadString returned is a
				// truncated read, not a complete line, so surface the error rather than
				// yielding the fragment as a good line.
				yield("", errors.Wrap(err, "reading line"))

				return
			}

			if !yield(trimLineEnding(line), nil) {
				return
			}
		}
	}
}

// Chunks yields successive slices of up to n lines; the final chunk may hold fewer than n. n must
// be greater than zero, otherwise the iterator yields ErrNonPositiveChunkSize once and stops. A
// read error is surfaced the same way Lines surfaces it, discarding any in-progress partial chunk.
func Chunks(r io.Reader, n int) iter.Seq2[[]string, error] {
	return func(yield func([]string, error) bool) {
		if n <= 0 {
			yield(nil, ErrNonPositiveChunkSize)

			return
		}

		chunk := make([]string, 0, n)
		for line, err := range Lines(r) {
			if err != nil {
				yield(nil, err)

				return
			}

			chunk = append(chunk, line)
			if len(chunk) == n {
				if !yield(chunk, nil) {
					return
				}

				chunk = make([]string, 0, n)
			}
		}

		if len(chunk) > 0 {
			yield(chunk, nil)
		}
	}
}

// SliceLines returns up to count lines after skipping offset lines: "the 10 lines after the first 8"
// is SliceLines(r, 8, 10). It reads no further than it needs to. If offset lands at or past the end
// of the input, it returns ErrOffsetBeyondEOF; if fewer than count lines remain, it returns the
// shorter slice with a nil error.
func SliceLines(r io.Reader, offset, count int) ([]string, error) {
	switch {
	case offset < 0:
		return nil, ErrNegativeOffset
	case count < 0:
		return nil, ErrNegativeCount
	case count == 0:
		return []string{}, nil
	}

	out := make([]string, 0, count)
	skipped, reached := 0, false

	for line, err := range Lines(r) {
		if err != nil {
			return nil, err
		}

		if skipped < offset {
			skipped++

			continue
		}

		reached = true
		out = append(out, line)
		if len(out) == count {
			break
		}
	}

	if !reached {
		return nil, ErrOffsetBeyondEOF
	}

	return out, nil
}

// AllLines materializes every line of r. It is a convenience for inputs small enough to hold in
// memory; prefer Lines for large files. An empty input yields a non-nil, empty slice.
func AllLines(r io.Reader) ([]string, error) {
	out := []string{}
	for line, err := range Lines(r) {
		if err != nil {
			return nil, err
		}

		out = append(out, line)
	}

	return out, nil
}

// AllChunks materializes every chunk of up to n lines. Like AllLines, it is for inputs small enough
// to hold in memory.
func AllChunks(r io.Reader, n int) ([][]string, error) {
	if n <= 0 {
		return nil, ErrNonPositiveChunkSize
	}

	out := [][]string{}
	for chunk, err := range Chunks(r, n) {
		if err != nil {
			return nil, err
		}

		out = append(out, chunk)
	}

	return out, nil
}

// MustAllLines is like AllLines but panics on error.
func MustAllLines(r io.Reader) []string {
	out, err := AllLines(r)
	if err != nil {
		panic(err)
	}

	return out
}

// MustSliceLines is like SliceLines but panics on error.
func MustSliceLines(r io.Reader, offset, count int) []string {
	out, err := SliceLines(r, offset, count)
	if err != nil {
		panic(err)
	}

	return out
}

// trimLineEnding strips a single trailing newline and an immediately preceding carriage return.
func trimLineEnding(s string) string {
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")

	return s
}
