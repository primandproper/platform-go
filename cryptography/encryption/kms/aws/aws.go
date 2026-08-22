package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

const name = "aws_kms_key_wrapper"

const keyIDKey = "kms.key.id"

// associatedDataContextKey is the single encryption-context entry this package
// stores associated data under.
//
// AWS models associated data as an "encryption context": a map of printable
// strings, not the opaque byte string every other AEAD in this module takes.
// Rather than expose that difference upward — which would make the KeyWrapper
// interface AWS-shaped for everyone — the bytes are base64'd into one entry
// under a fixed key. It round-trips exactly, and it keeps a caller from having
// to know which cloud is underneath.
//
// The cost is that the context is opaque in CloudTrail, where a structured map
// would have been greppable. That is a real loss and the reason to mention it
// here rather than bury it: the audit trail shows that a wrap happened with
// some binding, not what the binding was.
const associatedDataContextKey = "platform-go-aad"

// CryptoKeyClient is the slice of the KMS client this package uses.
type CryptoKeyClient interface {
	Encrypt(ctx context.Context, params *kms.EncryptInput, optFns ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, params *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// KeyWrapper is the AWS KMS encryption.KeyWrapper implementation. It is
// exported, and returned by NewKeyWrapper, so a caller who has chosen AWS KMS
// can depend on that choice rather than on the interface every wrapper shares.
type KeyWrapper struct {
	o11y         observability.Observer
	client       CryptoKeyClient
	wrapCounter  metrics.Int64Counter
	errorCounter metrics.Int64Counter
	latencyHist  metrics.Float64Histogram
	keyID        string
}

var _ encryption.KeyWrapper = (*KeyWrapper)(nil)

// NewKeyWrapper builds a KeyWrapper backed by AWS KMS.
//
// If client is nil, one is created from the default credential chain.
//
// The wrapping key never enters this process: KMS performs the wrap and unwrap
// inside its own boundary and returns only the result. That is the reason to
// prefer this over the local wrapper, and also why every wrap and unwrap is a
// network round trip — a per-subject data key should be unwrapped once and
// cached, not unwrapped per row read.
func NewKeyWrapper(ctx context.Context, cfg *Config, client CryptoKeyClient, opts ...Option) (*KeyWrapper, error) {
	if cfg == nil {
		return nil, errors.New("aws kms key wrapper: config is required")
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "aws kms key wrapper")
	}

	o := newOptions(opts)
	o11y := observability.NewObserverWithValues(name, o.logger, o.tracerProvider,
		map[string]any{keyIDKey: cfg.KeyID})
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
		awsCfg, loadErr := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
		if loadErr != nil {
			return nil, errors.Wrap(loadErr, "aws kms key wrapper: loading aws config")
		}

		client = kms.NewFromConfig(awsCfg)
	}

	return &KeyWrapper{
		o11y:         o11y,
		client:       client,
		wrapCounter:  wrapCounter,
		errorCounter: errorCounter,
		latencyHist:  latencyHist,
		keyID:        cfg.KeyID,
	}, nil
}

func (w *KeyWrapper) Wrap(ctx context.Context, key, associatedData []byte) ([]byte, error) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	defer w.recordLatency(ctx, time.Now())

	out, err := w.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             &w.keyID,
		Plaintext:         key,
		EncryptionContext: encryptionContext(associatedData),
	})
	if err != nil {
		w.errorCounter.Add(ctx, 1)

		return nil, op.Error(err, "wrapping key with aws kms")
	}

	w.wrapCounter.Add(ctx, 1)

	return out.CiphertextBlob, nil
}

func (w *KeyWrapper) Unwrap(ctx context.Context, wrapped, associatedData []byte) ([]byte, error) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	defer w.recordLatency(ctx, time.Now())

	// KeyId is supplied even though KMS can read it out of the blob. Naming it
	// makes the call fail when the ciphertext was produced under a different
	// key, rather than succeeding against whatever key it happens to name —
	// which is the difference between a wrapped key that is ours and one that
	// was substituted.
	out, err := w.client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    wrapped,
		KeyId:             &w.keyID,
		EncryptionContext: encryptionContext(associatedData),
	})
	if err != nil {
		w.errorCounter.Add(ctx, 1)

		// Deliberately not collapsed to ErrAuthenticationFailed: KMS reports
		// a mismatched encryption context, a disabled key, and a missing
		// permission through the same path, and the last two are operational
		// problems rather than evidence of tampering.
		return nil, op.Error(err, "unwrapping key with aws kms")
	}

	w.wrapCounter.Add(ctx, 1)

	return out.Plaintext, nil
}

// encryptionContext renders associated data as the map AWS expects, or nil
// when there is none. An empty map and a nil map behave identically at the
// API, but nil says "no binding" more clearly at a call site.
func encryptionContext(associatedData []byte) map[string]string {
	if len(associatedData) == 0 {
		return nil
	}

	return map[string]string{
		associatedDataContextKey: base64.StdEncoding.EncodeToString(associatedData),
	}
}

func (w *KeyWrapper) recordLatency(ctx context.Context, start time.Time) {
	w.latencyHist.Record(ctx, float64(time.Since(start).Milliseconds()))
}
