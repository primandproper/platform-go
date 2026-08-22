package objectstorage

import (
	"context"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
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

// ErrNilConfig denotes that the provided configuration is nil.
//
// An unrecognized provider is reported as errors.ErrUnknownProvider rather than
// a sentinel of this package's own: startup code branches on one thing for
// "the config named a provider nothing implements", whichever package it
// reached.
var ErrNilConfig = platformerrors.New("nil config provided")

type (
	// Uploader implements the uploads.UploadManager interface.
	Uploader struct {
		bucket         *blob.Bucket
		o11y           observability.Observer
		circuitBreaker circuitbreaking.CircuitBreaker
		instruments    *instruments
	}

	// Config configures our UploadManager.
	Config struct {
		_                 struct{}                  `json:"-"            yaml:"-"`
		FilesystemConfig  *FilesystemConfig         `env:",init"         envPrefix:"FILESYSTEM_"       json:"filesystem,omitempty"          yaml:"filesystem,omitempty"`
		R2Config          *R2Config                 `env:",init"         envPrefix:"R2_"               json:"r2,omitempty"                  yaml:"r2,omitempty"`
		BackblazeB2Config *BackblazeB2Config        `env:",init"         envPrefix:"BACKBLAZE_B2_"     json:"backblazeB2,omitempty"         yaml:"backblazeB2,omitempty"`
		BucketPrefix      string                    `env:"BUCKET_PREFIX" json:"bucketPrefix,omitempty" yaml:"bucketPrefix,omitempty"`
		BucketName        string                    `env:"BUCKET_NAME"   json:"bucketName,omitempty"   yaml:"bucketName,omitempty"`
		Provider          string                    `env:"PROVIDER"      json:"provider,omitempty"     yaml:"provider,omitempty"`
		CircuitBreaker    circuitbreakingcfg.Config `env:",init"         envPrefix:"CIRCUIT_BREAKING_" json:"circuitBreakerConfig,omitzero" yaml:"circuitBreakerConfig,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the Config. It first canonicalizes Provider (trim + lowercase) so
// validation, the conditional sub-config rules, and dispatch in selectBucket all agree — otherwise
// a value like "S3" or " s3 " would fail validation yet dispatch cleanly.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	c.Provider = strings.TrimSpace(strings.ToLower(c.Provider))

	// Release the sub-configs env parsing's ",init" allocated and nothing filled
	// in, so the Nil rules below read "the operator configured this" rather than
	// "env parsing ran".
	cfgnorm.ZeroToNil(&c.FilesystemConfig)
	cfgnorm.ZeroToNil(&c.R2Config)
	cfgnorm.ZeroToNil(&c.BackblazeB2Config)

	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.BucketName, validation.Required),
		validation.Field(&c.Provider, validation.Required, validation.In(S3Provider, FilesystemProvider, MemoryProvider, GCPCloudStorageProvider, R2Provider, BackblazeB2Provider)),
		validation.Field(&c.FilesystemConfig, validation.When(c.Provider == FilesystemProvider, validation.Required).Else(validation.Nil)),
		validation.Field(&c.R2Config, validation.When(c.Provider == R2Provider, validation.Required).Else(validation.Nil)),
		validation.Field(&c.BackblazeB2Config, validation.When(c.Provider == BackblazeB2Provider, validation.Required).Else(validation.Nil)),
	)
}

// NewUploadManager provides a new uploads.UploadManager.
func NewUploadManager(ctx context.Context, cfg *Config, opts ...Option) (*Uploader, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "upload manager provided invalid config")
	}

	o := newOptions(opts)

	cb, err := cfg.CircuitBreaker.NewCircuitBreaker(ctx, circuitbreakingcfg.WithLogger(o.logger), circuitbreakingcfg.WithMetricsProvider(o.metricsProvider))
	if err != nil {
		return nil, platformerrors.Wrap(err, "initializing upload manager circuit breaker")
	}

	serviceName := fmt.Sprintf("%s_uploader", cfg.BucketName)

	instruments, err := newInstruments(o.metricsProvider, cfg.BucketName)
	if err != nil {
		return nil, err
	}

	u := &Uploader{
		o11y:           observability.NewObserver(serviceName, o.logger, o.tracerProvider),
		circuitBreaker: circuitbreakingcfg.EnsureCircuitBreaker(cb, circuitbreakingcfg.WithLogger(o.logger)),
		instruments:    instruments,
	}

	if err = u.selectBucket(ctx, cfg); err != nil {
		return nil, platformerrors.Wrap(err, "initializing bucket")
	}

	return u, nil
}

func (u *Uploader) selectBucket(ctx context.Context, cfg *Config) (err error) {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case S3Provider:
		awsCfg, awsCfgErr := awsconfig.LoadDefaultConfig(ctx)
		if awsCfgErr != nil {
			return platformerrors.Wrap(awsCfgErr, "loading aws config for s3 bucket")
		}

		if u.bucket, err = s3blob.OpenBucketV2(ctx, s3v2.NewFromConfig(awsCfg), cfg.BucketName, &s3blob.Options{
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

		u.bucket, err = gcsblob.OpenBucket(ctx, client, cfg.BucketName, nil)
		if err != nil {
			return platformerrors.Wrap(err, "initializing GCP objectstorage")
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

		if u.bucket, err = s3blob.OpenBucketV2(ctx, client, cfg.BucketName, &s3blob.Options{
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

		if u.bucket, err = s3blob.OpenBucketV2(ctx, client, cfg.BucketName, &s3blob.Options{
			UseLegacyList: false,
		}); err != nil {
			return platformerrors.Wrap(err, "initializing backblaze b2 bucket")
		}
	case MemoryProvider:
		u.bucket = memblob.OpenBucket(&memblob.Options{})
	case FilesystemProvider:
		if cfg.FilesystemConfig == nil {
			return ErrNilConfig
		}

		if u.bucket, err = fileblob.OpenBucket(cfg.FilesystemConfig.RootDirectory, &fileblob.Options{
			URLSigner: nil,
			CreateDir: true,
			// Restrict created directories to owner-only so other users on the host
			// can't traverse in and read stored objects (gocloud defaults to 0777).
			DirFileMode: cfg.FilesystemConfig.directoryMode(),
		}); err != nil {
			return platformerrors.Wrap(err, "initializing filesystem bucket")
		}
	default:
		return platformerrors.Wrapf(platformerrors.ErrUnknownProvider, "storage provider %q", cfg.Provider)
	}

	// No provider gets a reachability probe here, GCP included — it used to have
	// one, and the asymmetry was the whole of the argument for it.
	//
	// Made uniform by removal rather than by extension, because what gocloud
	// offers is Bucket.IsAccessible, and that is a ListPage. Listing is a
	// distinct permission from reading and writing: the least-privilege policy
	// for a service that only Saves and Opens grants neither s3:ListBucket nor
	// storage.objects.list, so probing would refuse a bucket the Uploader can
	// use perfectly well and refuse it at startup, where the deployment cannot
	// proceed. It also makes constructing an Uploader a network call, which is
	// how a transient blip during a rollout becomes a service that will not come
	// up at all.
	//
	// Unreachability is a runtime condition here, and one this package already
	// models: every operation runs through the circuit breaker, which reports
	// ErrCircuitBroken with the provider's own error behind it. That names the
	// operation that failed and the permission it needed, which a probe cannot.
	if cfg.BucketPrefix != "" {
		// The trailing separator is enforced rather than assumed. gocloud
		// concatenates the prefix with the key verbatim, so a prefix of "acme"
		// turns key "1/x" into "acme1/x" — which is also what tenant "acme1"
		// writes, so two tenants silently share a namespace and List returns each
		// other's objects.
		prefix := cfg.BucketPrefix
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}

		u.bucket = blob.PrefixedBucket(u.bucket, prefix)
	}

	return err
}
