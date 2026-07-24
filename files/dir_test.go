package files_test

import (
	"iter"
	"os"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v6/encoding"
	"github.com/primandproper/platform-go/v6/files"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// buildTree lays out root/b/stuff.txt and root/c/whatever.txt, mirroring the a/b + a/c example, and
// returns the root.
func buildTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	must.NoError(t, os.MkdirAll(filepath.Join(root, "b"), 0o700))
	must.NoError(t, os.MkdirAll(filepath.Join(root, "c"), 0o700))
	must.NoError(t, os.WriteFile(filepath.Join(root, "b", "stuff.txt"), []byte("1\n2\n3\n"), 0o600))
	must.NoError(t, os.WriteFile(filepath.Join(root, "c", "whatever.txt"), []byte("x\ny\n"), 0o600))

	return root
}

func TestDir(T *testing.T) {
	T.Parallel()

	T.Run("resolves names against the base directory", func(t *testing.T) {
		t.Parallel()

		d, err := files.OpenDir(filepath.Join(buildTree(t), "b"))
		must.NoError(t, err)

		test.Eq(t, []string{"1", "2", "3"}, drainSeq(t, mustLines(t, d, "stuff.txt")))
	})

	T.Run("Chdir navigates to a sibling directory", func(t *testing.T) {
		t.Parallel()

		root := buildTree(t)
		d, err := files.OpenDir(filepath.Join(root, "b"))
		must.NoError(t, err)

		must.NoError(t, d.Chdir("../c"))
		test.EqOp(t, filepath.Join(root, "c"), d.Path())
		test.Eq(t, []string{"x", "y"}, drainSeq(t, mustLines(t, d, "whatever.txt")))
	})

	T.Run("Chdir navigates to the parent directory", func(t *testing.T) {
		t.Parallel()

		root := buildTree(t)
		d, err := files.OpenDir(filepath.Join(root, "b"))
		must.NoError(t, err)

		must.NoError(t, d.Chdir(".."))
		test.EqOp(t, root, d.Path())
	})

	T.Run("Sub returns a new handle without mutating the original", func(t *testing.T) {
		t.Parallel()

		root := buildTree(t)
		d, err := files.OpenDir(filepath.Join(root, "b"))
		must.NoError(t, err)

		sub, err := d.Sub("../c")
		must.NoError(t, err)

		test.EqOp(t, filepath.Join(root, "b"), d.Path())
		test.EqOp(t, filepath.Join(root, "c"), sub.Path())
	})

	T.Run("Resolve feeds the generic decode helpers", func(t *testing.T) {
		t.Parallel()

		root := buildTree(t)
		must.NoError(t, os.WriteFile(filepath.Join(root, "b", "config.json"), []byte(`{"name":"platform","count":2}`), 0o600))

		d, err := files.OpenDir(filepath.Join(root, "b"))
		must.NoError(t, err)

		got, err := files.DecodeFile[sample](t.Context(), d.Resolve("config.json"), encoding.ContentTypeJSON)
		must.NoError(t, err)
		test.EqOp(t, sample{Name: "platform", Count: 2}, got)
	})

	T.Run("StreamChunks works relative to the base", func(t *testing.T) {
		t.Parallel()

		d, err := files.OpenDir(filepath.Join(buildTree(t), "b"))
		must.NoError(t, err)

		out, err := d.StreamChunks(t.Context(), "stuff.txt", 2)
		must.NoError(t, err)

		got := [][]string{}
		for res := range out {
			must.NoError(t, res.Err)
			got = append(got, res.Lines)
		}

		test.Eq(t, [][]string{{"1", "2"}, {"3"}}, got)
	})

	T.Run("OpenDir on a non-directory errors", func(t *testing.T) {
		t.Parallel()

		root := buildTree(t)
		_, err := files.OpenDir(filepath.Join(root, "b", "stuff.txt"))
		test.Error(t, err)
	})

	T.Run("Chdir to a non-directory errors", func(t *testing.T) {
		t.Parallel()

		root := buildTree(t)
		d, err := files.OpenDir(filepath.Join(root, "b"))
		must.NoError(t, err)

		test.Error(t, d.Chdir("stuff.txt"))
	})
}

func mustLines(t *testing.T, d *files.Dir, name string) iter.Seq2[string, error] {
	t.Helper()

	seq, err := d.Lines(name)
	must.NoError(t, err)

	return seq
}
