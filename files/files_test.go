package files_test

import (
	"iter"
	"os"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v5/files"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	must.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	return path
}

func drainSeq(t *testing.T, seq iter.Seq2[string, error]) []string {
	t.Helper()

	out := []string{}
	for line, err := range seq {
		must.NoError(t, err)
		out = append(out, line)
	}

	return out
}

func TestReader_LinesFile(T *testing.T) {
	T.Parallel()

	T.Run("yields a file's lines", func(t *testing.T) {
		t.Parallel()

		seq, err := files.LinesFile(writeTemp(t, "f.txt", "a\nb\nc\n"))
		must.NoError(t, err)
		test.Eq(t, []string{"a", "b", "c"}, drainSeq(t, seq))
	})

	T.Run("missing file errors up front", func(t *testing.T) {
		t.Parallel()

		_, err := files.LinesFile(filepath.Join(t.TempDir(), "nope.txt"))
		test.Error(t, err)
	})
}

func TestReader_ChunksFile(T *testing.T) {
	T.Parallel()

	T.Run("yields a file's chunks", func(t *testing.T) {
		t.Parallel()

		seq, err := files.ChunksFile(writeTemp(t, "f.txt", "a\nb\nc\n"), 2)
		must.NoError(t, err)

		got := [][]string{}
		for chunk, chunkErr := range seq {
			must.NoError(t, chunkErr)
			got = append(got, chunk)
		}

		test.Eq(t, [][]string{{"a", "b"}, {"c"}}, got)
	})

	T.Run("missing file errors up front", func(t *testing.T) {
		t.Parallel()

		_, err := files.ChunksFile(filepath.Join(t.TempDir(), "nope.txt"), 2)
		test.Error(t, err)
	})
}

func TestReader_SliceLinesFile(T *testing.T) {
	T.Parallel()

	T.Run("returns a window from a file", func(t *testing.T) {
		t.Parallel()

		got, err := files.SliceLinesFile(t.Context(), writeTemp(t, "f.txt", "a\nb\nc\nd\n"), 1, 2)
		must.NoError(t, err)
		test.Eq(t, []string{"b", "c"}, got)
	})

	T.Run("missing file errors", func(t *testing.T) {
		t.Parallel()

		_, err := files.SliceLinesFile(t.Context(), filepath.Join(t.TempDir(), "nope.txt"), 0, 1)
		test.Error(t, err)
	})
}

func TestNewReader(T *testing.T) {
	T.Parallel()

	T.Run("builds a usable Reader", func(t *testing.T) {
		t.Parallel()

		r := files.NewReader(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		got, err := r.SliceLinesFile(t.Context(), writeTemp(t, "f.txt", "a\nb\n"), 0, 1)
		must.NoError(t, err)
		test.Eq(t, []string{"a"}, got)
	})
}

func TestMustFileHelpers(T *testing.T) {
	T.Parallel()

	T.Run("MustLinesFile returns a seq", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"a", "b"}, drainSeq(t, files.MustLinesFile(writeTemp(t, "f.txt", "a\nb\n"))))
	})

	T.Run("MustLinesFile panics on a missing file", func(t *testing.T) {
		t.Parallel()

		missing := filepath.Join(t.TempDir(), "nope.txt")
		test.Panic(t, func() {
			_ = files.MustLinesFile(missing)
		})
	})

	T.Run("MustChunksFile panics on a missing file", func(t *testing.T) {
		t.Parallel()

		missing := filepath.Join(t.TempDir(), "nope.txt")
		test.Panic(t, func() {
			_ = files.MustChunksFile(missing, 2)
		})
	})
}
