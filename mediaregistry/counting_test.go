package mediaregistry

import (
	"io"
	"strings"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestCountingReader(T *testing.T) {
	T.Parallel()

	T.Run("counts what passed through", func(t *testing.T) {
		t.Parallel()

		c := &countingReader{r: strings.NewReader("twelve bytes")}

		read, err := io.ReadAll(c)
		must.NoError(t, err)
		test.EqOp(t, "twelve bytes", string(read))
		test.EqOp(t, int64(12), c.n)
	})

	T.Run("counts the bytes a failing read delivered", func(t *testing.T) {
		t.Parallel()

		// A reader that hands back bytes and an error in the same call is
		// legal, and the bytes were still read. Counting them separately from
		// the error is what keeps the count honest about what a provider saw.
		c := &countingReader{r: io.MultiReader(strings.NewReader("abc"), &failingReader{})}

		_, err := io.ReadAll(c)
		must.Error(t, err)
		test.EqOp(t, int64(3), c.n)
	})
}

// failingReader fails on every read, for the partial-read case above.
type failingReader struct{}

var _ io.Reader = (*failingReader)(nil)

func (*failingReader) Read([]byte) (int, error) {
	return 0, platformerrors.New("reader broke")
}
