package cloudtrace

import (
	"context"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	o11yutils "github.com/primandproper/platform-go/v13/observability/utils"

	texporter "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ErrNilConfig indicates SetupCloudTrace was called with no config. It is a
// named error rather than the nil pointer read that used to happen at
// cfg.ProjectID, which is what a deployment naming "cloudtrace" and supplying
// no cloudtrace block got.
var ErrNilConfig = errors.New("nil config")

// SetupCloudTrace creates a new trace provider instance and registers it as global trace provider.
func SetupCloudTrace(ctx context.Context, serviceName string, spanCollectionProbability float64, cfg *Config) (tracing.Provider, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	exporter, err := texporter.New(texporter.WithProjectID(cfg.ProjectID))
	if err != nil {
		return nil, errors.Wrap(err, "setting up trace exporter")
	}

	res, err := o11yutils.OtelResource(ctx, serviceName)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(spanCollectionProbability)),
	)

	otel.SetTracerProvider(tp)

	return tp, nil
}
