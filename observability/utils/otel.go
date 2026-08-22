package o11yutils

import (
	"context"

	"github.com/primandproper/platform-go/v13/errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// resourceOptions returns the detectors every pillar's resource is built from.
func resourceOptions(serviceName string) []resource.Option {
	options := []resource.Option{
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithOSType(),
	}

	if serviceName != "" {
		options = append(options,
			resource.WithAttributes(
				attribute.KeyValue{
					Key:   semconv.ServiceNameKey,
					Value: attribute.StringValue(serviceName),
				},
			),
		)
	}

	return options
}

// OtelResource assembles the OpenTelemetry resource describing this process.
//
// Every caller is a constructor that already returns an error, which is why this
// returns one too: resource detection reads the environment and the host, and a
// detector that fails is a misconfiguration the caller can report like any
// other, not grounds for taking the process down from inside a constructor.
func OtelResource(ctx context.Context, serviceName string) (*resource.Resource, error) {
	res, err := resource.New(ctx, resourceOptions(serviceName)...)
	if err != nil {
		return nil, errors.Wrap(err, "assembling otel resource")
	}

	return res, nil
}

// MustOtelResource is OtelResource for callers that have nowhere to put an
// error. Prefer OtelResource: everything in this repo that builds a resource is
// a constructor with an error in its signature.
func MustOtelResource(ctx context.Context, serviceName string) *resource.Resource {
	res, err := OtelResource(ctx, serviceName)
	if err != nil {
		panic(err)
	}

	return res
}
