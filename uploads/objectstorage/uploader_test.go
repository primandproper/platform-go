package objectstorage

import (
	"os"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName:       t.Name(),
			Provider:         FilesystemProvider,
			FilesystemConfig: &FilesystemConfig{RootDirectory: t.Name()},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with missing bucket name", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: MemoryProvider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   "invalid_provider",
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with s3 provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   S3Provider,
			S3Config:   &S3Config{BucketName: t.Name()},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with s3 provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   S3Provider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with gcp provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   GCPCloudStorageProvider,
			GCP:        &GCPConfig{BucketName: t.Name()},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with gcp provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   GCPCloudStorageProvider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with r2 provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   R2Provider,
			R2Config: &R2Config{
				AccountID:       t.Name(),
				BucketName:      t.Name(),
				AccessKeyID:     t.Name(),
				SecretAccessKey: t.Name(),
			},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with r2 provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   R2Provider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with backblaze b2 provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   BackblazeB2Provider,
			BackblazeB2Config: &BackblazeB2Config{
				ApplicationKeyID: t.Name(),
				ApplicationKey:   t.Name(),
				BucketName:       t.Name(),
				Region:           t.Name(),
			},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with backblaze b2 provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   BackblazeB2Provider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with memory provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   MemoryProvider,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with filesystem provider missing config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   FilesystemProvider,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with non-s3 provider having s3 config is invalid", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   MemoryProvider,
			S3Config:   &S3Config{BucketName: t.Name()},
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestNewUploadManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l := loggingnoop.NewLogger()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   MemoryProvider,
		}

		x, err := NewUploadManager(ctx, l, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), cfg)
		test.NotNil(t, x)
		test.NoError(t, err)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l := loggingnoop.NewLogger()

		x, err := NewUploadManager(ctx, l, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), nil)
		test.Nil(t, x)
		test.Error(t, err)
	})

	T.Run("with invalid config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l := loggingnoop.NewLogger()
		cfg := &Config{}

		x, err := NewUploadManager(ctx, l, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), cfg)
		test.Nil(t, x)
		test.Error(t, err)
	})

	T.Run("with filesystem provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l := loggingnoop.NewLogger()
		tempDir := os.TempDir()

		cfg := &Config{
			BucketName:       t.Name(),
			Provider:         FilesystemProvider,
			FilesystemConfig: &FilesystemConfig{RootDirectory: tempDir},
		}

		x, err := NewUploadManager(ctx, l, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), cfg)
		test.NotNil(t, x)
		test.NoError(t, err)
	})

	T.Run("with bucket prefix", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l := loggingnoop.NewLogger()
		cfg := &Config{
			BucketName:   t.Name(),
			Provider:     MemoryProvider,
			BucketPrefix: "prefix/",
		}

		x, err := NewUploadManager(ctx, l, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), cfg)
		test.NotNil(t, x)
		test.NoError(t, err)
	})

	T.Run("with selectBucket error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l := loggingnoop.NewLogger()
		cfg := &Config{
			BucketName: t.Name(),
			Provider:   GCPCloudStorageProvider,
			GCP:        &GCPConfig{BucketName: t.Name()},
		}

		x, err := NewUploadManager(ctx, l, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), cfg)
		test.Nil(t, x)
		test.Error(t, err)
	})
}

func TestUploader_selectBucket(T *testing.T) {
	T.Parallel()

	T.Run("s3 happy path", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider: S3Provider,
			S3Config: &S3Config{
				BucketName: t.Name(),
			},
		}

		test.NoError(t, u.selectBucket(ctx, cfg))
	})

	T.Run("s3 with nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider: S3Provider,
			S3Config: nil,
		}

		test.Error(t, u.selectBucket(ctx, cfg))
	})

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider: MemoryProvider,
		}

		test.NoError(t, u.selectBucket(ctx, cfg))
	})

	T.Run("r2 happy path", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider: R2Provider,
			R2Config: &R2Config{
				AccountID:       t.Name(),
				BucketName:      t.Name(),
				AccessKeyID:     t.Name(),
				SecretAccessKey: t.Name(),
			},
		}

		test.NoError(t, u.selectBucket(ctx, cfg))
	})

	T.Run("r2 with nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider: R2Provider,
			R2Config: nil,
		}

		test.Error(t, u.selectBucket(ctx, cfg))
	})

	T.Run("backblaze b2 happy path", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider: BackblazeB2Provider,
			BackblazeB2Config: &BackblazeB2Config{
				ApplicationKeyID: t.Name(),
				ApplicationKey:   t.Name(),
				BucketName:       t.Name(),
				Region:           t.Name(),
			},
		}

		test.NoError(t, u.selectBucket(ctx, cfg))
	})

	T.Run("backblaze b2 with nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider:          BackblazeB2Provider,
			BackblazeB2Config: nil,
		}

		test.Error(t, u.selectBucket(ctx, cfg))
	})

	T.Run("filesystem happy path", func(t *testing.T) {
		t.Parallel()

		tempDir := os.TempDir()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider: FilesystemProvider,
			FilesystemConfig: &FilesystemConfig{
				RootDirectory: tempDir,
			},
		}

		test.NoError(t, u.selectBucket(ctx, cfg))
	})

	T.Run("filesystem with nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider:         FilesystemProvider,
			FilesystemConfig: nil,
		}

		test.Error(t, u.selectBucket(ctx, cfg))
	})

	T.Run("memory provider with bucket prefix", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider:     MemoryProvider,
			BucketPrefix: "my-prefix/",
		}

		test.NoError(t, u.selectBucket(ctx, cfg))
		test.NotNil(t, u.bucket)
	})

	T.Run("unknown provider falls through to filesystem default", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		tempDir := os.TempDir()
		cfg := &Config{
			Provider:         "something_unknown",
			FilesystemConfig: &FilesystemConfig{RootDirectory: tempDir},
		}

		test.NoError(t, u.selectBucket(ctx, cfg))
	})

	T.Run("gcp provider fails without credentials", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider: GCPCloudStorageProvider,
			GCP: &GCPConfig{
				BucketName: t.Name(),
			},
		}

		test.Error(t, u.selectBucket(ctx, cfg))
	})

	T.Run("filesystem with invalid root directory", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := &Uploader{}
		cfg := &Config{
			Provider:         FilesystemProvider,
			FilesystemConfig: &FilesystemConfig{RootDirectory: string([]byte{0x00})},
		}

		test.Error(t, u.selectBucket(ctx, cfg))
	})
}
