package tracing

import (
	"runtime"
	"strings"
)

const (
	// callerSkip skips runtime.Callers, GetCallerName, and StartSpan to land on the
	// instrumented method that opened the span.
	callerSkip = 3
)

var (
	PackagePrefix = "github.com/primandproper/platform/"
)

// GetCallerName returns the name of the function that opened the current span,
// with the platform package prefix trimmed. The single-element stack array keeps
// runtime.Callers allocation-free, and runtime.FuncForPC avoids the allocating
// runtime.CallersFrames iterator.
func GetCallerName() string {
	var programCounters [1]uintptr
	if runtime.Callers(callerSkip, programCounters[:]) < 1 {
		return "unknown"
	}

	fn := runtime.FuncForPC(programCounters[0])
	if fn == nil {
		return "unknown"
	}

	return strings.TrimPrefix(fn.Name(), PackagePrefix)
}
