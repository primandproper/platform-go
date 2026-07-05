package objectstorage

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v4/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"
	"github.com/primandproper/platform-go/v4/uploads"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterUploadManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, metricsnoop.NewMetricsProvider())
		do.ProvideValue(i, &Config{
			BucketName: t.Name(),
			Provider:   MemoryProvider,
		})

		RegisterUploadManager(i)

		uploader, err := do.Invoke[*Uploader](i)
		must.NoError(t, err)
		test.NotNil(t, uploader)

		uploadManager, err := do.Invoke[uploads.UploadManager](i)
		must.NoError(t, err)
		test.NotNil(t, uploadManager)
	})
}
