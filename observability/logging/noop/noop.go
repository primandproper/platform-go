package noop

import (
	"net/http"

	"github.com/primandproper/platform-go/observability/logging"

	"go.opentelemetry.io/otel/trace"
)

var _ logging.Logger = (*Logger)(nil)

var logger = &Logger{}

// Logger is a no-op Logger.
type Logger struct{}

// NewLogger returns a no-op Logger.
func NewLogger() logging.Logger {
	return logger
}

// Info is a no-op.
func (*Logger) Info(string) {}

// Debug is a no-op.
func (*Logger) Debug(string) {}

// Error is a no-op.
func (*Logger) Error(string, error) {}

// SetRequestIDFunc is a no-op.
func (*Logger) SetRequestIDFunc(logging.RequestIDFunc) {}

// Clone returns the same no-op Logger.
func (l *Logger) Clone() logging.Logger { return l }

// WithName returns the same no-op Logger.
func (l *Logger) WithName(string) logging.Logger { return l }

// WithValues returns the same no-op Logger.
func (l *Logger) WithValues(map[string]any) logging.Logger { return l }

// WithValue returns the same no-op Logger.
func (l *Logger) WithValue(string, any) logging.Logger { return l }

// WithRequest returns the same no-op Logger.
func (l *Logger) WithRequest(*http.Request) logging.Logger { return l }

// WithResponse returns the same no-op Logger.
func (l *Logger) WithResponse(*http.Response) logging.Logger { return l }

// WithError returns the same no-op Logger.
func (l *Logger) WithError(error) logging.Logger { return l }

// WithSpan returns the same no-op Logger.
func (l *Logger) WithSpan(trace.Span) logging.Logger { return l }
