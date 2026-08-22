package gcp

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	gax "github.com/googleapis/gax-go/v2"
)

const name = "gcp_kms_key_wrapper"

const keyNameKey = "kms.key.name"

// CryptoKeyClient is the slice of the Cloud KMS client this package uses.
// Narrow because it is the seam tests substitute at, and because the real
// client's surface is enormous next to the two calls that matter.
type CryptoKeyClient interface {
	Encrypt(ctx context.Context, req *kmspb.EncryptRequest, opts ...gax.CallOption) (*kmspb.EncryptResponse, error)
	Decrypt(ctx context.Context, req *kmspb.DecryptRequest, opts ...gax.CallOption) (*kmspb.DecryptResponse, error)
	Close() error
}

// KeyWrapper is the Cloud KMS encryption.KeyWrapper implementation. It is
// exported, and returned by NewKeyWrapper, so a caller who has chosen Cloud KMS
// can depend on that choice rather than on the interface every wrapper shares —
// most concretely Close, which encryption.KeyWrapper does not carry and which
// this wrapper needs whenever it built the gRPC client itself.
type KeyWrapper struct {
	o11y         observability.Observer
	client       CryptoKeyClient
	wrapCounter  metrics.Int64Counter
	errorCounter metrics.Int64Counter
	latencyHist  metrics.Float64Histogram
	keyName      string
}

var _ encryption.KeyWrapper = (*KeyWrapper)(nil)

// NewKeyWrapper builds a KeyWrapper backed by Cloud KMS.
//
// If client is nil, one is created using Application Default Credentials.
//
// The wrapping key never enters this process: Cloud KMS performs the wrap and
// unwrap inside its own boundary and returns only the result. That is the
// entire reason to prefer this over the local wrapper, and it is also why
// every wrap and unwrap is a network round trip — a per-subject data key
// should be unwrapped once and cached, not unwrapped per row read.
func NewKeyWrapper(ctx context.Context, cfg *Config, client CryptoKeyClient, opts ...Option) (*KeyWrapper, error) {
	if cfg == nil {
		return nil, errors.New("gcp kms key wrapper: config is required")
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "gcp kms key wrapper")
	}

	o := newOptions(opts)
	o11y := observability.NewObserverWithValues(name, o.logger, o.tracerProvider,
		map[string]any{keyNameKey: cfg.KeyName})
	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	wrapCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_operations", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating operation counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating latency histogram")
	}

	if client == nil {
		kmsClient, kmsErr := kms.NewKeyManagementClient(ctx)
		if kmsErr != nil {
			return nil, errors.Wrap(kmsErr, "gcp kms key wrapper: creating client")
		}

		client = kmsClient
	}

	return &KeyWrapper{
		o11y:         o11y,
		client:       client,
		wrapCounter:  wrapCounter,
		errorCounter: errorCounter,
		latencyHist:  latencyHist,
		keyName:      cfg.KeyName,
	}, nil
}

func (w *KeyWrapper) Wrap(ctx context.Context, key, associatedData []byte) ([]byte, error) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	defer w.recordLatency(ctx, time.Now())

	resp, err := w.client.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:                        w.keyName,
		Plaintext:                   key,
		AdditionalAuthenticatedData: associatedData,
	})
	if err != nil {
		w.errorCounter.Add(ctx, 1)

		return nil, op.Error(err, "wrapping key with cloud kms")
	}

	w.wrapCounter.Add(ctx, 1)

	return resp.GetCiphertext(), nil
}

func (w *KeyWrapper) Unwrap(ctx context.Context, wrapped, associatedData []byte) ([]byte, error) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	defer w.recordLatency(ctx, time.Now())

	resp, err := w.client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:                        w.keyName,
		Ciphertext:                  wrapped,
		AdditionalAuthenticatedData: associatedData,
	})
	if err != nil {
		w.errorCounter.Add(ctx, 1)

		// Not mapped to ErrAuthenticationFailed. Cloud KMS returns the same
		// code for a tampered ciphertext, mismatched associated data, a
		// destroyed key version, and a revoked permission — and the last two
		// are operational problems a caller has to be able to see rather than
		// read as "someone is attacking us".
		return nil, op.Error(err, "unwrapping key with cloud kms")
	}

	w.wrapCounter.Add(ctx, 1)

	return resp.GetPlaintext(), nil
}

// Close releases the underlying client.
func (w *KeyWrapper) Close() error {
	return w.client.Close()
}

func (w *KeyWrapper) recordLatency(ctx context.Context, start time.Time) {
	w.latencyHist.Record(ctx, float64(time.Since(start).Milliseconds()))
}
