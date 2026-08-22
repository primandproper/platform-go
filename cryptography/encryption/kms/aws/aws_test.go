package aws

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

var errKMS = errors.New("kms is unavailable")

// fakeClient records what it was called with and replays a canned answer.
type fakeClient struct {
	encryptIn  *kms.EncryptInput
	decryptIn  *kms.DecryptInput
	err        error
	encryptOut []byte
	decryptOut []byte
}

func (c *fakeClient) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	c.encryptIn = in
	if c.err != nil {
		return nil, c.err
	}

	return &kms.EncryptOutput{CiphertextBlob: c.encryptOut}, nil
}

func (c *fakeClient) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	c.decryptIn = in
	if c.err != nil {
		return nil, c.err
	}

	return &kms.DecryptOutput{Plaintext: c.decryptOut}, nil
}

func validConfig() *Config {
	return &Config{Region: "us-east-1", KeyID: "alias/platform"}
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a valid config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, validConfig().ValidateWithContext(t.Context()))
	})

	T.Run("rejects an absent region", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{KeyID: "alias/platform"}).ValidateWithContext(t.Context()))
	})

	T.Run("rejects an absent key ID", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{Region: "us-east-1"}).ValidateWithContext(t.Context()))
	})
}

func TestNewKeyWrapper(T *testing.T) {
	T.Parallel()

	T.Run("builds over a supplied client", func(t *testing.T) {
		t.Parallel()

		w, err := NewKeyWrapper(t.Context(), validConfig(), &fakeClient{})
		must.NoError(t, err)
		test.NotNil(t, w)
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewKeyWrapper(t.Context(), nil, &fakeClient{})
		test.Error(t, err)
	})

	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewKeyWrapper(t.Context(), &Config{}, &fakeClient{})
		test.Error(t, err)
	})
}

func TestAWSKeyWrapper_Wrap(T *testing.T) {
	T.Parallel()

	T.Run("returns the ciphertext blob", func(t *testing.T) {
		t.Parallel()

		client := &fakeClient{encryptOut: []byte("wrapped")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		wrapped, err := w.Wrap(t.Context(), []byte("data-key"), nil)
		must.NoError(t, err)

		test.Eq(t, []byte("wrapped"), wrapped)
		test.Eq(t, []byte("data-key"), client.encryptIn.Plaintext)
		test.EqOp(t, "alias/platform", *client.encryptIn.KeyId)
	})

	T.Run("renders associated data as a base64 encryption context", func(t *testing.T) {
		t.Parallel()

		// AWS models associated data as a map of printable strings rather than
		// opaque bytes. Encoding into one fixed entry is what keeps the
		// KeyWrapper interface from becoming AWS-shaped for every backend.
		client := &fakeClient{encryptOut: []byte("wrapped")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		aad := []byte{0x00, 0xff, 0xfe}

		_, err = w.Wrap(t.Context(), []byte("data-key"), aad)
		must.NoError(t, err)

		test.MapEq(t, map[string]string{
			associatedDataContextKey: base64.StdEncoding.EncodeToString(aad),
		}, client.encryptIn.EncryptionContext)
	})

	T.Run("sends no encryption context when there is no associated data", func(t *testing.T) {
		t.Parallel()

		client := &fakeClient{encryptOut: []byte("wrapped")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		_, err = w.Wrap(t.Context(), []byte("data-key"), nil)
		must.NoError(t, err)

		test.Nil(t, client.encryptIn.EncryptionContext)
	})

	T.Run("surfaces a client failure", func(t *testing.T) {
		t.Parallel()

		w, err := NewKeyWrapper(t.Context(), validConfig(), &fakeClient{err: errKMS})
		must.NoError(t, err)

		_, err = w.Wrap(t.Context(), []byte("data-key"), nil)
		test.ErrorIs(t, err, errKMS)
	})
}

func TestAWSKeyWrapper_Unwrap(T *testing.T) {
	T.Parallel()

	T.Run("returns the plaintext key", func(t *testing.T) {
		t.Parallel()

		client := &fakeClient{decryptOut: []byte("data-key")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		unwrapped, err := w.Unwrap(t.Context(), []byte("wrapped"), nil)
		must.NoError(t, err)

		test.Eq(t, []byte("data-key"), unwrapped)
		test.Eq(t, []byte("wrapped"), client.decryptIn.CiphertextBlob)
	})

	T.Run("names the key rather than trusting the blob", func(t *testing.T) {
		t.Parallel()

		// KMS can read the key out of the ciphertext. Supplying it anyway is
		// what makes a blob produced under some other key fail instead of
		// succeeding against whatever key it names.
		client := &fakeClient{decryptOut: []byte("data-key")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		_, err = w.Unwrap(t.Context(), []byte("wrapped"), nil)
		must.NoError(t, err)

		must.NotNil(t, client.decryptIn.KeyId)
		test.EqOp(t, "alias/platform", *client.decryptIn.KeyId)
	})

	T.Run("replays the same encryption context", func(t *testing.T) {
		t.Parallel()

		client := &fakeClient{decryptOut: []byte("data-key")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		aad := []byte("subject-42")

		_, err = w.Unwrap(t.Context(), []byte("wrapped"), aad)
		must.NoError(t, err)

		test.MapEq(t, map[string]string{
			associatedDataContextKey: base64.StdEncoding.EncodeToString(aad),
		}, client.decryptIn.EncryptionContext)
	})

	T.Run("surfaces a client failure without calling it authentication", func(t *testing.T) {
		t.Parallel()

		// KMS reports a mismatched context, a disabled key, and a missing
		// permission the same way. Collapsing them to ErrAuthenticationFailed
		// would read two operational problems as an attack.
		w, err := NewKeyWrapper(t.Context(), validConfig(), &fakeClient{err: errKMS})
		must.NoError(t, err)

		_, err = w.Unwrap(t.Context(), []byte("wrapped"), nil)
		test.ErrorIs(t, err, errKMS)
	})
}

// providerFailingOn is a metrics.Provider that refuses to build exactly one
// instrument, so each of NewKeyWrapper's failures can be reached
// independently rather than only the first.
func providerFailingOn(failing string) metrics.Provider {
	delegate := metricsnoop.NewMetricsProvider()

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failing {
				return nil, errInstrument
			}

			return delegate.NewInt64Counter(name, options...)
		},
		NewFloat64HistogramFunc: func(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			if name == failing {
				return nil, errInstrument
			}

			return delegate.NewFloat64Histogram(name, options...)
		},
	}
}

var errInstrument = errors.New("creating the instrument")

func TestNewKeyWrapper_instrumentFailures(T *testing.T) {
	T.Parallel()

	// A metrics provider that cannot build an instrument is a
	// misconfiguration. Wrap latency in particular is the number that decides
	// whether data keys need caching, so carrying on unmetered would hide the
	// thing most worth watching.
	for _, instrument := range []string{
		name + "_operations",
		name + "_errors",
		name + "_latency_ms",
	} {
		T.Run(instrument, func(t *testing.T) {
			t.Parallel()

			_, err := NewKeyWrapper(t.Context(), validConfig(), &fakeClient{},
				WithMetricsProvider(providerFailingOn(instrument)),
			)
			must.Error(t, err)
			test.ErrorIs(t, err, errInstrument)
		})
	}
}

func TestNewKeyWrapper_defaultClient(T *testing.T) {
	T.Parallel()

	T.Run("builds its own client when given none", func(t *testing.T) {
		t.Parallel()

		// Hermetic: LoadDefaultConfig and NewFromConfig resolve configuration
		// and build a client struct without contacting anything, so this
		// exercises the branch without credentials or network. Requests would
		// need both; construction does not.
		w, err := NewKeyWrapper(t.Context(), validConfig(), nil)
		must.NoError(t, err)
		test.NotNil(t, w)
	})
}
