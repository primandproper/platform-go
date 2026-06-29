package files_test

import (
	"io"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v2/files"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func collectLines(t *testing.T, r io.Reader) []string {
	t.Helper()

	out := []string{}
	for line, err := range files.Lines(r) {
		must.NoError(t, err)
		out = append(out, line)
	}

	return out
}

func TestLines(T *testing.T) {
	T.Parallel()

	T.Run("yields each line without the trailing newline", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"a", "b", "c"}, collectLines(t, strings.NewReader("a\nb\nc\n")))
	})

	T.Run("handles CRLF line endings", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"a", "b"}, collectLines(t, strings.NewReader("a\r\nb\r\n")))
	})

	T.Run("yields an unterminated final line", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"a", "b"}, collectLines(t, strings.NewReader("a\nb")))
	})

	T.Run("empty input yields nothing", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, collectLines(t, strings.NewReader("")))
	})

	T.Run("surfaces a read error", func(t *testing.T) {
		t.Parallel()

		sentinel := io.ErrClosedPipe
		var got error
		for _, err := range files.Lines(&errReader{err: sentinel}) {
			if err != nil {
				got = err
			}
		}

		test.ErrorIs(t, got, sentinel)
	})
}

func TestChunks(T *testing.T) {
	T.Parallel()

	T.Run("yields chunks of n lines with a short final chunk", func(t *testing.T) {
		t.Parallel()

		got := [][]string{}
		for chunk, err := range files.Chunks(strings.NewReader("a\nb\nc\nd\ne\n"), 2) {
			must.NoError(t, err)
			got = append(got, chunk)
		}

		test.Eq(t, [][]string{{"a", "b"}, {"c", "d"}, {"e"}}, got)
	})

	T.Run("non-positive n yields ErrNonPositiveChunkSize", func(t *testing.T) {
		t.Parallel()

		var got error
		for _, err := range files.Chunks(strings.NewReader("a\n"), 0) {
			got = err
		}

		test.ErrorIs(t, got, files.ErrNonPositiveChunkSize)
	})
}

func TestSliceLines(T *testing.T) {
	T.Parallel()

	const ten = "l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\n"

	T.Run("returns the window after the offset", func(t *testing.T) {
		t.Parallel()

		got, err := files.SliceLines(strings.NewReader(ten), 8, 10)
		must.NoError(t, err)
		test.Eq(t, []string{"l8", "l9"}, got)
	})

	T.Run("returns a short slice when fewer than count remain", func(t *testing.T) {
		t.Parallel()

		got, err := files.SliceLines(strings.NewReader(ten), 3, 4)
		must.NoError(t, err)
		test.Eq(t, []string{"l3", "l4", "l5", "l6"}, got)
	})

	T.Run("offset at or past EOF returns ErrOffsetBeyondEOF", func(t *testing.T) {
		t.Parallel()

		_, err := files.SliceLines(strings.NewReader("a\nb\nc\n"), 8, 10)
		test.ErrorIs(t, err, files.ErrOffsetBeyondEOF)
	})

	T.Run("offset exactly equal to line count is beyond EOF", func(t *testing.T) {
		t.Parallel()

		_, err := files.SliceLines(strings.NewReader("a\nb\nc\n"), 3, 1)
		test.ErrorIs(t, err, files.ErrOffsetBeyondEOF)
	})

	T.Run("count of zero returns an empty slice", func(t *testing.T) {
		t.Parallel()

		got, err := files.SliceLines(strings.NewReader(ten), 2, 0)
		must.NoError(t, err)
		test.SliceEmpty(t, got)
	})

	T.Run("negative offset is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := files.SliceLines(strings.NewReader(ten), -1, 3)
		test.ErrorIs(t, err, files.ErrNegativeOffset)
	})

	T.Run("negative count is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := files.SliceLines(strings.NewReader(ten), 0, -1)
		test.ErrorIs(t, err, files.ErrNegativeCount)
	})
}

func TestAllLines(T *testing.T) {
	T.Parallel()

	T.Run("materializes every line", func(t *testing.T) {
		t.Parallel()

		got, err := files.AllLines(strings.NewReader("a\nb\nc"))
		must.NoError(t, err)
		test.Eq(t, []string{"a", "b", "c"}, got)
	})

	T.Run("empty input returns a non-nil empty slice", func(t *testing.T) {
		t.Parallel()

		got, err := files.AllLines(strings.NewReader(""))
		must.NoError(t, err)
		must.NotNil(t, got)
		test.SliceEmpty(t, got)
	})
}

func TestAllChunks(T *testing.T) {
	T.Parallel()

	T.Run("materializes every chunk", func(t *testing.T) {
		t.Parallel()

		got, err := files.AllChunks(strings.NewReader("a\nb\nc\n"), 2)
		must.NoError(t, err)
		test.Eq(t, [][]string{{"a", "b"}, {"c"}}, got)
	})

	T.Run("non-positive n is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := files.AllChunks(strings.NewReader("a\n"), 0)
		test.ErrorIs(t, err, files.ErrNonPositiveChunkSize)
	})
}

func TestMustLineHelpers(T *testing.T) {
	T.Parallel()

	T.Run("MustAllLines returns lines", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"a", "b"}, files.MustAllLines(strings.NewReader("a\nb\n")))
	})

	T.Run("MustSliceLines panics past EOF", func(t *testing.T) {
		t.Parallel()

		test.Panic(t, func() {
			_ = files.MustSliceLines(strings.NewReader("a\n"), 8, 1)
		})
	})
}

// errReader fails every read with a fixed error, to exercise the read-error path.
type errReader struct {
	err error
}

func (e *errReader) Read([]byte) (int, error) {
	return 0, e.err
}
