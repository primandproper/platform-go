package chi

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/routing/backends/internal/httpmw"

	"github.com/riandyrn/otelchi"
	otelchimetric "github.com/riandyrn/otelchi/metric"
)

// otelMiddleware is the one part of the stack that is genuinely chi's own.
//
// The other backends wrap their mux in otelhttp, which sees a bare
// http.Handler and can only name a span after the request method. otelchi
// reads chi's RouteContext, so its spans carry the matched route pattern
// instead of collapsing every path onto one span name — which is the whole
// reason this backend instruments differently rather than sharing
// httpmw.Standard's otel step.
//
// The slice is ordered outermost-first, matching httpmw.Standard.
func otelMiddleware(serviceName string, metricProvider metrics.Provider) []func(http.Handler) http.Handler {
	baseCfg := otelchimetric.NewBaseConfig(
		serviceName,
		otelchimetric.WithMeterProvider(metricProvider.MeterProvider()),
	)

	return []func(http.Handler) http.Handler{
		otelchimetric.NewRequestDurationMillis(baseCfg),
		otelchimetric.NewRequestInFlight(baseCfg),
		otelchimetric.NewResponseSizeBytes(baseCfg),
		otelchi.Middleware(
			serviceName,
			otelchi.WithRequestMethodInSpanName(true),
			otelchi.WithTraceResponseHeaders(otelchi.TraceHeaderConfig{
				TraceIDHeader:      "X-Trace-ID",
				TraceSampledHeader: "X-Trace-Sampled",
			}),
			otelchi.WithFilter(func(r *http.Request) bool {
				// Skip tracing for probes and the other operational endpoints, to
				// avoid noise from load balancers, K8s probes and scrapers.
				return !httpmw.IsUntraced(r.URL.Path)
			}),
		),
	}
}
