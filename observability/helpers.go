package observability

import (
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/tracing"
)

func ObserveValues(values map[string]any, span tracing.Span, logger logging.Logger) logging.Logger {
	for k, v := range values {
		if span != nil {
			tracing.AttachToSpan(span, k, v)
		}

		if logger != nil {
			logger = logger.WithValue(k, v)
		}
	}

	// Link the span to the logger exactly once — not once per key, which duplicated
	// the span.id/trace.id fields — and even when values is empty.
	if logger != nil && span != nil {
		logger = logger.WithSpan(span)
	}

	return logger
}
