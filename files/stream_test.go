package files_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v4/files"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestStreamChunks(T *testing.T) {
	T.Parallel()

	T.Run("streams every chunk then closes", func(t *testing.T) {
		t.Parallel()

		out, err := files.StreamChunks(t.Context(), strings.NewReader("a\nb\nc\nd\n"), 2)
		must.NoError(t, err)

		got := [][]string{}
		for res := range out {
			must.NoError(t, res.Err)
			got = append(got, res.Lines)
		}

		test.Eq(t, [][]string{{"a", "b"}, {"c", "d"}}, got)
	})

	T.Run("delivers a mid-stream read error", func(t *testing.T) {
		t.Parallel()

		out, err := files.StreamChunks(t.Context(), &errReader{err: io.ErrClosedPipe}, 2)
		must.NoError(t, err)

		var gotErr error
		for res := range out {
			if res.Err != nil {
				gotErr = res.Err
			}
		}

		test.ErrorIs(t, gotErr, io.ErrClosedPipe)
	})

	T.Run("non-positive n fails fast with no channel", func(t *testing.T) {
		t.Parallel()

		out, err := files.StreamChunks(t.Context(), strings.NewReader("a\n"), 0)
		test.ErrorIs(t, err, files.ErrNonPositiveChunkSize)
		test.Nil(t, out)
	})

	T.Run("cancellation closes the channel and the goroutine exits", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		out, err := files.StreamChunks(ctx, strings.NewReader(strings.Repeat("x\n", 1000)), 1)
		must.NoError(t, err)

		// Take one chunk, cancel, then drain. Reaching the end of the range proves the producer
		// goroutine returned and closed the channel rather than leaking.
		<-out
		cancel()
		for range out {
		}
	})
}

func TestStreamChunksFile(T *testing.T) {
	T.Parallel()

	T.Run("streams a file's chunks", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "data.txt")
		must.NoError(t, os.WriteFile(path, []byte("a\nb\nc\n"), 0o600))

		out, err := files.StreamChunksFile(t.Context(), path, 2)
		must.NoError(t, err)

		got := [][]string{}
		for res := range out {
			must.NoError(t, res.Err)
			got = append(got, res.Lines)
		}

		test.Eq(t, [][]string{{"a", "b"}, {"c"}}, got)
	})

	T.Run("missing file fails fast with no channel", func(t *testing.T) {
		t.Parallel()

		out, err := files.StreamChunksFile(t.Context(), filepath.Join(t.TempDir(), "nope.txt"), 2)
		test.Error(t, err)
		test.Nil(t, out)
	})
}
