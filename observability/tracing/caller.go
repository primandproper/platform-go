package tracing

import (
	"runtime"
	"strings"
	"sync"
)

const (
	// callerSkip skips runtime.Callers, GetCallerName, and StartSpan to land on the
	// instrumented method that opened the span.
	callerSkip = 3
)

var (
	PackagePrefix = "github.com/primandproper/platform-go/v5/"

	// callerNameCache memoizes resolved names by program counter. A call site's PC
	// is stable, the set of instrumented sites is small and fixed, and
	// runtime.Func.Name allocates a fresh string on every call, so caching makes
	// GetCallerName allocation-free after the first hit per site.
	callerNameCache sync.Map // map[uintptr]string
)

// GetCallerName returns the name of the function that opened the current span,
// with the platform package prefix trimmed. The single-element stack array keeps
// runtime.Callers allocation-free, and runtime.FuncForPC avoids the allocating
// runtime.CallersFrames iterator. Results are memoized by program counter so the
// allocating runtime.Func.Name call happens once per call site.
func GetCallerName() string {
	var programCounters [1]uintptr
	if runtime.Callers(callerSkip, programCounters[:]) < 1 {
		return "unknown"
	}

	pc := programCounters[0]
	if cached, ok := callerNameCache.Load(pc); ok {
		if name, isString := cached.(string); isString {
			return name
		}
	}

	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "unknown"
	}

	name := strings.TrimPrefix(fn.Name(), PackagePrefix)
	callerNameCache.Store(pc, name)

	return name
}
