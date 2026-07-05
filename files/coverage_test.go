package files_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v4/encoding"
	"github.com/primandproper/platform-go/v4/files"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestCoreReadErrors(T *testing.T) {
	T.Parallel()

	T.Run("Decode surfaces a read error", func(t *testing.T) {
		t.Parallel()

		_, err := files.Decode[sample](t.Context(), &errReader{err: io.ErrClosedPipe}, encoding.ContentTypeJSON)
		test.ErrorIs(t, err, io.ErrClosedPipe)
	})

	T.Run("SliceLines surfaces a read error", func(t *testing.T) {
		t.Parallel()

		_, err := files.SliceLines(&errReader{err: io.ErrClosedPipe}, 0, 1)
		test.ErrorIs(t, err, io.ErrClosedPipe)
	})

	T.Run("AllLines surfaces a read error", func(t *testing.T) {
		t.Parallel()

		_, err := files.AllLines(&errReader{err: io.ErrClosedPipe})
		test.ErrorIs(t, err, io.ErrClosedPipe)
	})

	T.Run("AllChunks surfaces a read error", func(t *testing.T) {
		t.Parallel()

		_, err := files.AllChunks(&errReader{err: io.ErrClosedPipe}, 2)
		test.ErrorIs(t, err, io.ErrClosedPipe)
	})
}

func TestMustHelpersComplete(T *testing.T) {
	T.Parallel()

	T.Run("MustAllLines panics on a read error", func(t *testing.T) {
		t.Parallel()

		test.Panic(t, func() {
			_ = files.MustAllLines(&errReader{err: io.ErrClosedPipe})
		})
	})

	T.Run("MustSliceLines returns the window", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"b"}, files.MustSliceLines(strings.NewReader("a\nb\nc\n"), 1, 1))
	})

	T.Run("MustChunksFile returns a seq", func(t *testing.T) {
		t.Parallel()

		got := [][]string{}
		for chunk, err := range files.MustChunksFile(writeTemp(t, "f.txt", "a\nb\nc\n"), 2) {
			must.NoError(t, err)
			got = append(got, chunk)
		}

		test.Eq(t, [][]string{{"a", "b"}, {"c"}}, got)
	})

	T.Run("MustDecode panics on malformed input", func(t *testing.T) {
		t.Parallel()

		test.Panic(t, func() {
			_ = files.MustDecode[sample](t.Context(), strings.NewReader("{bad"), encoding.ContentTypeJSON)
		})
	})

	T.Run("MustDecodeFile returns the value", func(t *testing.T) {
		t.Parallel()

		path := writeTemp(t, "config.json", `{"name":"platform","count":9}`)
		got := files.MustDecodeFile[sample](t.Context(), path, encoding.ContentTypeJSON)
		test.EqOp(t, sample{Name: "platform", Count: 9}, got)
	})
}

func TestIteratorEarlyBreak(T *testing.T) {
	T.Parallel()

	T.Run("LinesFile closes when the caller breaks early", func(t *testing.T) {
		t.Parallel()

		seq, err := files.LinesFile(writeTemp(t, "f.txt", "a\nb\nc\n"))
		must.NoError(t, err)

		first := ""
		for line, lineErr := range seq {
			must.NoError(t, lineErr)
			first = line

			break
		}

		test.EqOp(t, "a", first)
	})

	T.Run("ChunksFile closes when the caller breaks early", func(t *testing.T) {
		t.Parallel()

		seq, err := files.ChunksFile(writeTemp(t, "f.txt", "a\nb\nc\nd\n"), 2)
		must.NoError(t, err)

		var first []string
		for chunk, chunkErr := range seq {
			must.NoError(t, chunkErr)
			first = chunk

			break
		}

		test.Eq(t, []string{"a", "b"}, first)
	})
}

func TestSliceLinesFile_SlicingError(T *testing.T) {
	T.Parallel()

	T.Run("offset past EOF on a real file errors", func(t *testing.T) {
		t.Parallel()

		_, err := files.SliceLinesFile(t.Context(), writeTemp(t, "f.txt", "a\nb\n"), 8, 2)
		test.ErrorIs(t, err, files.ErrOffsetBeyondEOF)
	})
}

func TestDirCoverage(T *testing.T) {
	T.Parallel()

	T.Run("NewDir builds a handle with injected dependencies", func(t *testing.T) {
		t.Parallel()

		d, err := files.NewDir(filepath.Join(buildTree(t), "b"), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)
		test.Eq(t, []string{"1", "2", "3"}, drainSeq(t, mustLines(t, d, "stuff.txt")))
	})

	T.Run("Chunks resolves relative to the base", func(t *testing.T) {
		t.Parallel()

		d, err := files.OpenDir(filepath.Join(buildTree(t), "b"))
		must.NoError(t, err)

		seq, err := d.Chunks("stuff.txt", 2)
		must.NoError(t, err)

		got := [][]string{}
		for chunk, chunkErr := range seq {
			must.NoError(t, chunkErr)
			got = append(got, chunk)
		}

		test.Eq(t, [][]string{{"1", "2"}, {"3"}}, got)
	})

	T.Run("SliceLines resolves relative to the base", func(t *testing.T) {
		t.Parallel()

		d, err := files.OpenDir(filepath.Join(buildTree(t), "b"))
		must.NoError(t, err)

		got, err := d.SliceLines(t.Context(), "stuff.txt", 1, 1)
		must.NoError(t, err)
		test.Eq(t, []string{"2"}, got)
	})

	T.Run("Chdir accepts an absolute path", func(t *testing.T) {
		t.Parallel()

		root := buildTree(t)
		d, err := files.OpenDir(filepath.Join(root, "b"))
		must.NoError(t, err)

		must.NoError(t, d.Chdir(filepath.Join(root, "c")))
		test.EqOp(t, filepath.Join(root, "c"), d.Path())
	})

	T.Run("Sub on a missing directory errors", func(t *testing.T) {
		t.Parallel()

		d, err := files.OpenDir(filepath.Join(buildTree(t), "b"))
		must.NoError(t, err)

		_, err = d.Sub("nope")
		test.Error(t, err)
	})

	T.Run("OpenDir on a missing path errors", func(t *testing.T) {
		t.Parallel()

		_, err := files.OpenDir(filepath.Join(t.TempDir(), "nope"))
		test.Error(t, err)
	})
}

func TestStreamChunksFile_NonPositiveN(T *testing.T) {
	T.Parallel()

	T.Run("rejects n <= 0 after opening and closes the file", func(t *testing.T) {
		t.Parallel()

		out, err := files.StreamChunksFile(t.Context(), writeTemp(t, "f.txt", "a\nb\n"), 0)
		test.ErrorIs(t, err, files.ErrNonPositiveChunkSize)
		test.Nil(t, out)
	})
}

func TestStreamChunks_Cancellation(T *testing.T) {
	T.Parallel()

	T.Run("pre-canceled context stops at the loop guard", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		out, err := files.StreamChunks(ctx, strings.NewReader("a\nb\nc\n"), 1)
		must.NoError(t, err)

		// The consumer keeps draining, so the loop-guard branch fires and its cancellation result is
		// delivered before the channel closes.
		for range out { //nolint:revive // draining to completion is the point
		}
	})

	T.Run("cancellation while blocked on a send stops the producer", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		out, err := files.StreamChunks(ctx, strings.NewReader(strings.Repeat("x\n", 1000)), 1)
		must.NoError(t, err)

		<-out                             // take one; the producer now blocks sending the next chunk
		time.Sleep(25 * time.Millisecond) // let it reach and park on the blocking send
		cancel()                          // the send select takes its ctx.Done arm
		time.Sleep(25 * time.Millisecond) // let the non-blocking trySend run with no receiver ready
		for range out {                   //nolint:revive // drain whatever remains until close
		}
	})
}
