package objectstorage

import (
	"context"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v2/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v2/circuitbreaking/config"
	platformerrors "github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	s3v2 "github.com/aws/aws-sdk-go-v2/service/s3"
	validation "github.com/go-ozzo/ozzo-validation/v4"
	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"
	"gocloud.dev/blob/gcsblob"
	"gocloud.dev/blob/memblob"
	"gocloud.dev/blob/s3blob"
	"gocloud.dev/gcp"
)

const (
	// MemoryProvider indicates we'd like to use the memory adapter for blob.
	MemoryProvider = "memory"
)

var (
	// ErrNilConfig denotes that the provided configuration is nil.
	ErrNilConfig = platformerrors.New("nil config provided")
)

type (
	// Uploader implements our UploadManager struct.
	Uploader struct {
		bucket         *blob.Bucket
		o11y           observability.Observer
		circuitBreaker circuitbreaking.CircuitBreaker
		saveCounter    metrics.Int64Counter
		readCounter    metrics.Int64Counter
		saveErrCounter metrics.Int64Counter
		readErrCounter metrics.Int64Counter
		latencyHist    metrics.Float64Histogram
	}

	// Config configures our UploadManager.
	Config struct {
		_                 struct{}                  `json:"-"`
		FilesystemConfig  *FilesystemConfig         `env:"init"                envPrefix:"FILESYSTEM_"            json:"filesystem,omitempty"`
		S3Config          *S3Config                 `env:"init"                envPrefix:"S3_"                    json:"s3,omitempty"`
		GCP               *GCPConfig                `env:"init"                envPrefix:"GCP_"                   json:"gcpConfig,omitempty"`
		R2Config          *R2Config                 `env:"init"                envPrefix:"R2_"                    json:"r2,omitempty"`
		BackblazeB2Config *BackblazeB2Config        `env:"init"                envPrefix:"BACKBLAZE_B2_"          json:"backblazeB2,omitempty"`
		BucketPrefix      string                    `env:"BUCKET_PREFIX"       json:"bucketPrefix,omitempty"`
		BucketName        string                    `env:"BUCKET_NAME"         json:"bucketName,omitempty"`
		UploadFilenameKey string                    `env:"UPLOAD_FILENAME_KEY" json:"uploadFilenameKey,omitempty"`
		Provider          string                    `env:"PROVIDER"            json:"provider,omitempty"`
		CircuitBreaker    circuitbreakingcfg.Config `env:"init"                envPrefix:"CIRCUIT_BREAKING_"      json:"circuitBreakerConfig"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the Config.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.BucketName, validation.Required),
		validation.Field(&c.Provider, validation.In(S3Provider, FilesystemProvider, MemoryProvider, GCPCloudStorageProvider, R2Provider, BackblazeB2Provider)),
		validation.Field(&c.S3Config, validation.When(c.Provider == S3Provider, validation.Required).Else(validation.Nil)),
		validation.Field(&c.GCP, validation.When(c.Provider == GCPCloudStorageProvider, validation.Required).Else(validation.Nil)),
		validation.Field(&c.FilesystemConfig, validation.When(c.Provider == FilesystemProvider, validation.Required).Else(validation.Nil)),
		validation.Field(&c.R2Config, validation.When(c.Provider == R2Provider, validation.Required).Else(validation.Nil)),
		validation.Field(&c.BackblazeB2Config, validation.When(c.Provider == BackblazeB2Provider, validation.Required).Else(validation.Nil)),
	)
}

// NewUploadManager provides a new uploads.UploadManager.
func NewUploadManager(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, cfg *Config) (*Uploader, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	cb, err := cfg.CircuitBreaker.ProvideCircuitBreaker(ctx, logger, metricsProvider)
	if err != nil {
		return nil, platformerrors.Wrap(err, "initializing upload manager circuit breaker")
	}

	serviceName := fmt.Sprintf("%s_uploader", cfg.BucketName)

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	saveCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_saves", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating save counter")
	}

	readCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_reads", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating read counter")
	}

	saveErrCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_save_errors", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating save error counter")
	}

	readErrCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_read_errors", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating read error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	u := &Uploader{
		o11y:           observability.NewObserver(serviceName, logger, tracerProvider),
		circuitBreaker: circuitbreakingcfg.EnsureCircuitBreaker(cb),
		saveCounter:    saveCounter,
		readCounter:    readCounter,
		saveErrCounter: saveErrCounter,
		readErrCounter: readErrCounter,
		latencyHist:    latencyHist,
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "upload manager provided invalid config")
	}

	if err = u.selectBucket(ctx, cfg); err != nil {
		return nil, platformerrors.Wrap(err, "initializing bucket")
	}

	return u, nil
}

func (u *Uploader) selectBucket(ctx context.Context, cfg *Config) (err error) {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case S3Provider:
		if cfg.S3Config == nil {
			return ErrNilConfig
		}

		if u.bucket, err = s3blob.OpenBucketV2(ctx, s3v2.New(s3v2.Options{}), cfg.S3Config.BucketName, &s3blob.Options{
			UseLegacyList: false,
		}); err != nil {
			return platformerrors.Wrap(err, "initializing s3 bucket")
		}
	case GCPCloudStorageProvider:
		creds, credsErr := gcp.DefaultCredentials(ctx)
		if credsErr != nil {
			return platformerrors.Wrap(credsErr, "initializing GCP objectstorage")
		}

		client, clientErr := gcp.NewHTTPClient(gcp.DefaultTransport(), creds.TokenSource)
		if clientErr != nil {
			return platformerrors.Wrap(clientErr, "initializing GCP objectstorage")
		}

		u.bucket, err = gcsblob.OpenBucket(ctx, client, cfg.GCP.BucketName, nil)
		if err != nil {
			return platformerrors.Wrap(err, "initializing GCP objectstorage")
		}

		if available, availabilityErr := u.bucket.IsAccessible(ctx); availabilityErr != nil {
			return platformerrors.Wrap(availabilityErr, "verifying bucket accessibility")
		} else if !available {
			return platformerrors.Newf("bucket %q is unavailable", cfg.BucketName)
		}

	case R2Provider:
		if cfg.R2Config == nil {
			return ErrNilConfig
		}

		endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.R2Config.AccountID)
		client := s3v2.New(s3v2.Options{
			BaseEndpoint: aws.String(endpoint),
			Credentials:  credentials.NewStaticCredentialsProvider(cfg.R2Config.AccessKeyID, cfg.R2Config.SecretAccessKey, ""),
			Region:       "auto",
		})

		if u.bucket, err = s3blob.OpenBucketV2(ctx, client, cfg.R2Config.BucketName, &s3blob.Options{
			UseLegacyList: false,
		}); err != nil {
			return platformerrors.Wrap(err, "initializing r2 bucket")
		}
	case BackblazeB2Provider:
		if cfg.BackblazeB2Config == nil {
			return ErrNilConfig
		}

		endpoint := fmt.Sprintf("https://s3.%s.backblazeb2.com", cfg.BackblazeB2Config.Region)
		client := s3v2.New(s3v2.Options{
			BaseEndpoint: aws.String(endpoint),
			Credentials:  credentials.NewStaticCredentialsProvider(cfg.BackblazeB2Config.ApplicationKeyID, cfg.BackblazeB2Config.ApplicationKey, ""),
			Region:       cfg.BackblazeB2Config.Region,
		})

		if u.bucket, err = s3blob.OpenBucketV2(ctx, client, cfg.BackblazeB2Config.BucketName, &s3blob.Options{
			UseLegacyList: false,
		}); err != nil {
			return platformerrors.Wrap(err, "initializing backblaze b2 bucket")
		}
	case MemoryProvider:
		u.bucket = memblob.OpenBucket(&memblob.Options{})
	default:
		if cfg.FilesystemConfig == nil {
			return ErrNilConfig
		}

		if u.bucket, err = fileblob.OpenBucket(cfg.FilesystemConfig.RootDirectory, &fileblob.Options{
			URLSigner: nil,
			CreateDir: true,
		}); err != nil {
			return platformerrors.Wrap(err, "initializing filesystem bucket")
		}
	}

	if cfg.BucketPrefix != "" {
		u.bucket = blob.PrefixedBucket(u.bucket, cfg.BucketPrefix)
	}

	return err
}
