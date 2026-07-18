package files_test

import (
	"testing"
	"testing/fstest"

	"github.com/primandproper/platform-go/v5/encoding"
	"github.com/primandproper/platform-go/v5/files"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func mapFS() fstest.MapFS {
	return fstest.MapFS{
		"top.txt":         {Data: []byte("a\nb\nc\n")},
		"sub/nested.txt":  {Data: []byte("x\ny\n")},
		"sub/config.json": {Data: []byte(`{"name":"platform"}`)},
	}
}

func TestNewReaderFS(T *testing.T) {
	T.Parallel()

	T.Run("reads lines through an fs.FS", func(t *testing.T) {
		t.Parallel()

		r := files.NewReaderFS(mapFS(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		seq, err := r.LinesFile("top.txt")
		must.NoError(t, err)
		test.Eq(t, []string{"a", "b", "c"}, drainSeq(t, seq))
	})

	T.Run("streams chunks through an fs.FS", func(t *testing.T) {
		t.Parallel()

		r := files.NewReaderFS(mapFS(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		ch, err := r.StreamChunksFile(t.Context(), "top.txt", 2)
		must.NoError(t, err)

		got := [][]string{}
		for res := range ch {
			must.NoError(t, res.Err)
			got = append(got, res.Lines)
		}

		test.Eq(t, [][]string{{"a", "b"}, {"c"}}, got)
	})

	T.Run("missing file errors up front", func(t *testing.T) {
		t.Parallel()

		r := files.NewReaderFS(mapFS(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		_, err := r.LinesFile("nope.txt")
		test.Error(t, err)
	})
}

func TestFS(T *testing.T) {
	T.Parallel()

	T.Run("OpenFS reads relative names", func(t *testing.T) {
		t.Parallel()

		d := files.OpenFS(mapFS())
		seq, err := d.Lines("top.txt")
		must.NoError(t, err)
		test.Eq(t, []string{"a", "b", "c"}, drainSeq(t, seq))
	})

	T.Run("NewFS reads relative names", func(t *testing.T) {
		t.Parallel()

		d := files.NewFS(mapFS(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		seq, err := d.Lines("top.txt")
		must.NoError(t, err)
		test.Eq(t, []string{"a", "b", "c"}, drainSeq(t, seq))
	})

	T.Run("Chunks yields chunks of lines", func(t *testing.T) {
		t.Parallel()

		seq, err := files.OpenFS(mapFS()).Chunks("top.txt", 2)
		must.NoError(t, err)

		got := [][]string{}
		for chunk, err := range seq {
			must.NoError(t, err)
			got = append(got, chunk)
		}
		test.Eq(t, [][]string{{"a", "b"}, {"c"}}, got)
	})

	T.Run("StreamChunks streams chunks of lines", func(t *testing.T) {
		t.Parallel()

		ch, err := files.OpenFS(mapFS()).StreamChunks(t.Context(), "top.txt", 2)
		must.NoError(t, err)

		got := [][]string{}
		for res := range ch {
			must.NoError(t, res.Err)
			got = append(got, res.Lines)
		}
		test.Eq(t, [][]string{{"a", "b"}, {"c"}}, got)
	})

	T.Run("Sub descends into a subtree", func(t *testing.T) {
		t.Parallel()

		sub, err := files.OpenFS(mapFS()).Sub("sub")
		must.NoError(t, err)

		got, err := sub.SliceLines(t.Context(), "nested.txt", 0, 1)
		must.NoError(t, err)
		test.Eq(t, []string{"x"}, got)
	})

	T.Run("Sub rejects an invalid path", func(t *testing.T) {
		t.Parallel()

		_, err := files.OpenFS(mapFS()).Sub("../escape")
		test.Error(t, err)
	})

	T.Run("FS exposes the root for the decode helpers", func(t *testing.T) {
		t.Parallel()

		type config struct {
			Name string `json:"name"`
		}

		sub, err := files.OpenFS(mapFS()).Sub("sub")
		must.NoError(t, err)

		cfg, err := files.DecodeFileFS[config](t.Context(), sub.FS(), "config.json", encoding.ContentTypeJSON)
		must.NoError(t, err)
		test.EqOp(t, "platform", cfg.Name)
	})
}

func TestDecodeFileFS(T *testing.T) {
	T.Parallel()

	type config struct {
		Name string `json:"name"`
	}

	T.Run("decodes a file within an fs.FS", func(t *testing.T) {
		t.Parallel()

		cfg, err := files.DecodeFileFS[config](t.Context(), mapFS(), "sub/config.json", encoding.ContentTypeJSON)
		must.NoError(t, err)
		test.EqOp(t, "platform", cfg.Name)
	})

	T.Run("missing file errors", func(t *testing.T) {
		t.Parallel()

		_, err := files.DecodeFileFS[config](t.Context(), mapFS(), "nope.json", encoding.ContentTypeJSON)
		test.Error(t, err)
	})
}
