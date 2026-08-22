package files

import (
	"os"
	"path/filepath"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

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

	// WithLogger is optional and documents that an absent logger logs nowhere.
	// closeQuietly logs through the retained field rather than through the
	// Observer, so nothing but normalizing at construction keeps that promise —
	// and the path only runs when a close has already failed, which is where a
	// panic would be least welcome and least likely to be noticed in testing.
	T.Run("does not panic when no logger was supplied", func(t *testing.T) {
		t.Parallel()

		r := NewReader()
		must.NotNil(t, r.logger)

		f, err := os.Create(filepath.Join(t.TempDir(), "f.txt"))
		must.NoError(t, err)
		must.NoError(t, f.Close())

		test.NotPanic(t, func() {
			r.closeQuietly(f)
		})
	})
}

func TestOsFSOpen(T *testing.T) {
	T.Parallel()

	T.Run("opens an existing file", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "f.txt")
		must.NoError(t, os.WriteFile(path, []byte("hello"), 0o600))

		f, err := osFS{}.Open(path)
		must.NoError(t, err)
		must.NoError(t, f.Close())
	})

	// os.Open hands back a nil *os.File on failure, and *os.File satisfies
	// fs.File, so returning its result straight through made the returned
	// fs.File non-nil — a value a caller's nil check accepts and the first Read
	// panics on.
	T.Run("a failed open is a nil interface, not a typed nil", func(t *testing.T) {
		t.Parallel()

		f, err := osFS{}.Open(filepath.Join(t.TempDir(), "absent.txt"))
		test.Error(t, err)

		// Compared against nil directly rather than with test.Nil, which is
		// satisfied by a nil pointer inside a non-nil interface.
		test.True(t, f == nil)
	})
}
