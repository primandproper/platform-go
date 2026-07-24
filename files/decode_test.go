package files_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/primandproper/platform-go/v6/encoding"
	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/files"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type sample struct {
	Name  string `json:"name"  yaml:"name"`
	Count int    `json:"count" yaml:"count"`
}

func TestDecode(T *testing.T) {
	T.Parallel()

	T.Run("decodes JSON from a reader", func(t *testing.T) {
		t.Parallel()

		got, err := files.Decode[sample](t.Context(), strings.NewReader(`{"name":"platform","count":3}`), encoding.ContentTypeJSON)
		must.NoError(t, err)
		test.EqOp(t, sample{Name: "platform", Count: 3}, got)
	})

	T.Run("decodes YAML from a reader", func(t *testing.T) {
		t.Parallel()

		got, err := files.Decode[sample](t.Context(), strings.NewReader("name: platform\ncount: 3\n"), encoding.ContentTypeYAML)
		must.NoError(t, err)
		test.EqOp(t, sample{Name: "platform", Count: 3}, got)
	})

	T.Run("empty input is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := files.Decode[sample](t.Context(), strings.NewReader(""), encoding.ContentTypeJSON)
		test.ErrorIs(t, err, errors.ErrEmptyInputParameter)
	})

	T.Run("malformed input errors", func(t *testing.T) {
		t.Parallel()

		_, err := files.Decode[sample](t.Context(), strings.NewReader("{not json"), encoding.ContentTypeJSON)
		test.Error(t, err)
	})
}

func TestDecodeFile(T *testing.T) {
	T.Parallel()

	T.Run("decodes a file by content type", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "config.yaml")
		must.NoError(t, os.WriteFile(path, []byte("name: platform\ncount: 7\n"), 0o600))

		got, err := files.DecodeFile[sample](t.Context(), path, encoding.ContentTypeYAML)
		must.NoError(t, err)
		test.EqOp(t, sample{Name: "platform", Count: 7}, got)
	})

	T.Run("missing file errors", func(t *testing.T) {
		t.Parallel()

		_, err := files.DecodeFile[sample](t.Context(), filepath.Join(t.TempDir(), "nope.json"), encoding.ContentTypeJSON)
		test.Error(t, err)
	})
}

func TestMustDecodeHelpers(T *testing.T) {
	T.Parallel()

	T.Run("MustDecode returns the value", func(t *testing.T) {
		t.Parallel()

		got := files.MustDecode[sample](t.Context(), strings.NewReader(`{"name":"platform","count":1}`), encoding.ContentTypeJSON)
		test.EqOp(t, sample{Name: "platform", Count: 1}, got)
	})

	T.Run("MustDecodeFile panics on a missing file", func(t *testing.T) {
		t.Parallel()

		missing := filepath.Join(t.TempDir(), "nope.json")
		test.Panic(t, func() {
			_ = files.MustDecodeFile[sample](t.Context(), missing, encoding.ContentTypeJSON)
		})
	})

	T.Run("MustDecodeFileFS returns the value", func(t *testing.T) {
		t.Parallel()

		got := files.MustDecodeFileFS[sample](t.Context(), fstest.MapFS{
			"config.json": {Data: []byte(`{"name":"platform","count":9}`)},
		}, "config.json", encoding.ContentTypeJSON)
		test.EqOp(t, sample{Name: "platform", Count: 9}, got)
	})

	T.Run("MustDecodeFileFS panics on a missing file", func(t *testing.T) {
		t.Parallel()

		test.Panic(t, func() {
			_ = files.MustDecodeFileFS[sample](t.Context(), fstest.MapFS{}, "nope.json", encoding.ContentTypeJSON)
		})
	})
}
