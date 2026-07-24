package files

import (
	"os"
	"path/filepath"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestCloseQuietly(T *testing.T) {
	T.Parallel()

	T.Run("logs and does not panic when Close fails", func(t *testing.T) {
		t.Parallel()

		r := newStandardReader(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())

		f, err := os.Create(filepath.Join(t.TempDir(), "f.txt"))
		must.NoError(t, err)
		must.NoError(t, f.Close()) // first close succeeds; the second will fail

		test.NotPanic(t, func() {
			r.closeQuietly(f) // double close returns an error, which closeQuietly logs
		})
	})
}
