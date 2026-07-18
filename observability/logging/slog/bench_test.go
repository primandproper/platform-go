package slog

import (
	"errors"
	"os"
	"testing"

	"github.com/primandproper/platform-go/v5/observability/logging"

	"github.com/shoenig/test/must"
)

// silenceOutput redirects stdout and stderr to /dev/null for the duration of the
// benchmark so we measure encoding/wrapper overhead rather than terminal I/O. The
// loggers capture os.Stdout/os.Stderr at construction time, so this must run before
// the logger is built.
func silenceOutput(b *testing.B) {
	b.Helper()

	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	must.NoError(b, err)

	origStdout, origStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = devnull, devnull

	b.Cleanup(func() {
		os.Stdout, os.Stderr = origStdout, origStderr
		_ = devnull.Close()
	})
}

func BenchmarkSlogLogger(b *testing.B) {
	silenceOutput(b)

	logger := NewSlogLogger(logging.InfoLevel)
	fields := map[string]any{"user_id": "123", "tenant": "acme", "count": 42}
	benchErr := errors.New("benchmark error")

	b.Run("Info", func(b *testing.B) {
		for b.Loop() {
			logger.Info("hello world")
		}
	})

	b.Run("WithValue", func(b *testing.B) {
		for b.Loop() {
			logger.WithValue("user_id", "123").Info("hello world")
		}
	})

	b.Run("WithValues", func(b *testing.B) {
		for b.Loop() {
			logger.WithValues(fields).Info("hello world")
		}
	})

	b.Run("Error", func(b *testing.B) {
		for b.Loop() {
			logger.Error("something failed", benchErr)
		}
	})

	b.Run("Chained", func(b *testing.B) {
		for b.Loop() {
			logger.WithName("service").WithValue("user_id", "123").WithValue("tenant", "acme").Info("hello world")
		}
	})
}
