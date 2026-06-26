package cloudtrace

import (
	"context"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/tracing"
	o11yutils "github.com/primandproper/platform-go/observability/utils"

	texporter "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	"go.opentelemetry.io/otel"
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

// SetupCloudTrace creates a new trace provider instance and registers it as global trace provider.
func SetupCloudTrace(ctx context.Context, serviceName string, spanCollectionProbability float64, cfg *Config) (tracing.TracerProvider, error) {
	exporter, err := texporter.New(texporter.WithProjectID(cfg.ProjectID))
	if err != nil {
		return nil, errors.Wrap(err, "setting up trace exporter")
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(o11yutils.MustOtelResource(ctx, serviceName)),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(spanCollectionProbability)),
	)

	otel.SetTracerProvider(tp)

	return tp, nil
}
