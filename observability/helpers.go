package observability

import (
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

func ObserveValues(values map[string]any, span tracing.Span, logger logging.Logger) logging.Logger {
	for k, v := range values {
		if span != nil {
			tracing.AttachToSpan(span, k, v)
		}

		if logger != nil {
			logger = logger.WithValue(k, v)
			if span != nil {
				logger = logger.WithSpan(span)
			}
		}
	}

	return logger
}
