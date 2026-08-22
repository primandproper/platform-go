package objectstorage

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/uploads"

	"github.com/samber/do/v2"
)

// RegisterUploadManager registers both *Uploader and uploads.UploadManager with the injector.
// Prerequisite: *Config must be registered (e.g. via uploadscfg.RegisterStorageConfig).
func RegisterUploadManager(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*Uploader, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewUploadManager(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithLogger(pillars.Logger),
			WithTracerProvider(pillars.TracerProvider),
			WithMetricsProvider(pillars.MetricsProvider),
		)
	})
	do.Provide(i, func(i do.Injector) (uploads.UploadManager, error) {
		return do.MustInvoke[*Uploader](i), nil
	})
}
