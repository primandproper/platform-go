// Package noop is the logging.Logger that writes nowhere. Info, Debug, Warn,
// and Error are all discarded — Error included — so a process wired with it
// reports its failures only through the values it returns.
//
// The With* methods hand back the same logger rather than a derived one, which
// means the name, values, request, response, error, and span a caller attaches
// are dropped at the call site. There is no accumulated context sitting
// somewhere that a later swap to a real logger would flush. NewLogger returns a
// shared instance for the same reason: the logger holds nothing worth a second
// allocation.
//
// It is what every constructor in this module resolves to through
// logging.EnsureLogger when handed no logger, so "no logger was named" and
// "this logger was named" are the same runtime state.
package noop

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/observability/logging"

	"go.opentelemetry.io/otel/trace"
)

var _ logging.Logger = (*Logger)(nil)

var logger = &Logger{}

// Logger is a no-op Logger.
type Logger struct{}

// NewLogger returns a no-op Logger.
func NewLogger() *Logger {
	return logger
}

// Info is a no-op.
func (*Logger) Info(string) {}

// Debug is a no-op.
func (*Logger) Debug(string) {}

// Warn is a no-op.
func (*Logger) Warn(string) {}

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
