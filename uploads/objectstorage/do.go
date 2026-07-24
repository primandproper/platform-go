package objectstorage

import (
	"context"

	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"
	"github.com/primandproper/platform-go/v6/uploads"

	"github.com/samber/do/v2"
)

// RegisterUploadManager registers both *Uploader and uploads.UploadManager with the injector.
// Prerequisite: *Config must be registered (e.g. via uploadscfg.RegisterStorageConfig).
func RegisterUploadManager(i do.Injector) {
	do.Provide[*Uploader](i, func(i do.Injector) (*Uploader, error) {
		return NewUploadManager(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
			do.MustInvoke[*Config](i),
		)
	})
	do.Provide[uploads.UploadManager](i, func(i do.Injector) (uploads.UploadManager, error) {
		return do.MustInvoke[*Uploader](i), nil
	})
}
