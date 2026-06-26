package oteltrace

import (
	"context"

	"github.com/primandproper/platform-go/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/tracing"
	o11yutils "github.com/primandproper/platform-go/observability/utils"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type errorHandler struct {
	logger logging.Logger
}

func (h errorHandler) Handle(err error) {
	h.logger.Error("tracer reported issue", err)
}

func init() {
	otel.SetErrorHandler(errorHandler{logger: loggingnoop.NewLogger().WithName("otel_errors")})
}

// SetupOtelGRPC creates a new trace provider instance and registers it as global trace provider.
func SetupOtelGRPC(ctx context.Context, serviceName string, collectionProbability float64, c *Config) (tracing.TracerProvider, error) {
	res := o11yutils.MustOtelResource(ctx, serviceName)

	options := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(c.CollectorEndpoint),
	}

	if c.Insecure {
		options = append(options, otlptracegrpc.WithInsecure())
	}

	traceExp, err := otlptrace.New(ctx, otlptracegrpc.NewClient(options...))
	if err != nil {
		return nil, err
	}

	bsp := sdktrace.NewBatchSpanProcessor(traceExp)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(collectionProbability)),
	)

	// set global propagator to tracecontext (the default is no-op).
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	otel.SetTracerProvider(tracerProvider)

	return tracerProvider, nil
}
