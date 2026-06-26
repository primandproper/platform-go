package encoding

import (
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterServerEncoderDecoder registers a ContentType and ServerEncoderDecoder with the injector.
func RegisterServerEncoderDecoder(i do.Injector) {
	do.Provide[ContentType](i, func(i do.Injector) (ContentType, error) {
		return ProvideContentType(do.MustInvoke[Config](i)), nil
	})
	do.Provide[ServerEncoderDecoder](i, func(i do.Injector) (ServerEncoderDecoder, error) {
		return ProvideServerEncoderDecoder(
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[ContentType](i),
		), nil
	})
}
