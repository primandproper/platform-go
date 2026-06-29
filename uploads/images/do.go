package images

import (
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterMediaUploadProcessor registers a MediaUploadProcessor with the injector.
func RegisterMediaUploadProcessor(i do.Injector) {
	do.Provide[MediaUploadProcessor](i, func(i do.Injector) (MediaUploadProcessor, error) {
		return NewMediaUploadProcessor(
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
		), nil
	})
}
