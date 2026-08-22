package gcp

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"cloud.google.com/go/kms/apiv1/kmspb"
	gax "github.com/googleapis/gax-go/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

const testKeyName = "projects/p/locations/l/keyRings/r/cryptoKeys/k"

var errKMS = errors.New("cloud kms is unavailable")

// fakeClient records what it was called with and replays a canned answer.
type fakeClient struct {
	encryptReq *kmspb.EncryptRequest
	decryptReq *kmspb.DecryptRequest
	err        error
	ciphertext []byte
	plaintext  []byte
	closed     bool
}

func (c *fakeClient) Encrypt(_ context.Context, req *kmspb.EncryptRequest, _ ...gax.CallOption) (*kmspb.EncryptResponse, error) {
	c.encryptReq = req
	if c.err != nil {
		return nil, c.err
	}

	return &kmspb.EncryptResponse{Ciphertext: c.ciphertext}, nil
}

func (c *fakeClient) Decrypt(_ context.Context, req *kmspb.DecryptRequest, _ ...gax.CallOption) (*kmspb.DecryptResponse, error) {
	c.decryptReq = req
	if c.err != nil {
		return nil, c.err
	}

	return &kmspb.DecryptResponse{Plaintext: c.plaintext}, nil
}

func (c *fakeClient) Close() error {
	c.closed = true

	return nil
}

func validConfig() *Config {
	return &Config{KeyName: testKeyName}
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a full resource name", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, validConfig().ValidateWithContext(t.Context()))
	})

	T.Run("rejects an absent key name", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{}).ValidateWithContext(t.Context()))
	})

	T.Run("rejects a bare key name", func(t *testing.T) {
		t.Parallel()

		// Cloud KMS wants the whole resource path. A bare name would fail at
		// the API with a less obvious message than this one.
		test.Error(t, (&Config{KeyName: "my-key"}).ValidateWithContext(t.Context()))
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

		_, err := NewKeyWrapper(t.Context(), &Config{KeyName: "my-key"}, &fakeClient{})
		test.Error(t, err)
	})
}

func TestGCPKeyWrapper_Wrap(T *testing.T) {
	T.Parallel()

	T.Run("returns the ciphertext and names the key", func(t *testing.T) {
		t.Parallel()

		client := &fakeClient{ciphertext: []byte("wrapped")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		wrapped, err := w.Wrap(t.Context(), []byte("data-key"), nil)
		must.NoError(t, err)

		test.Eq(t, []byte("wrapped"), wrapped)
		test.EqOp(t, testKeyName, client.encryptReq.GetName())
		test.Eq(t, []byte("data-key"), client.encryptReq.GetPlaintext())
	})

	T.Run("passes associated data through natively", func(t *testing.T) {
		t.Parallel()

		// Unlike AWS, Cloud KMS takes raw AAD bytes, so nothing needs encoding.
		client := &fakeClient{ciphertext: []byte("wrapped")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		aad := []byte{0x00, 0xff, 0xfe}

		_, err = w.Wrap(t.Context(), []byte("data-key"), aad)
		must.NoError(t, err)

		test.Eq(t, aad, client.encryptReq.GetAdditionalAuthenticatedData())
	})

	T.Run("surfaces a client failure", func(t *testing.T) {
		t.Parallel()

		w, err := NewKeyWrapper(t.Context(), validConfig(), &fakeClient{err: errKMS})
		must.NoError(t, err)

		_, err = w.Wrap(t.Context(), []byte("data-key"), nil)
		test.ErrorIs(t, err, errKMS)
	})
}

func TestGCPKeyWrapper_Unwrap(T *testing.T) {
	T.Parallel()

	T.Run("returns the plaintext key", func(t *testing.T) {
		t.Parallel()

		client := &fakeClient{plaintext: []byte("data-key")}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		unwrapped, err := w.Unwrap(t.Context(), []byte("wrapped"), []byte("subject-42"))
		must.NoError(t, err)

		test.Eq(t, []byte("data-key"), unwrapped)
		test.EqOp(t, testKeyName, client.decryptReq.GetName())
		test.Eq(t, []byte("wrapped"), client.decryptReq.GetCiphertext())
		test.Eq(t, []byte("subject-42"), client.decryptReq.GetAdditionalAuthenticatedData())
	})

	T.Run("surfaces a client failure without calling it authentication", func(t *testing.T) {
		t.Parallel()

		// A destroyed key version and a revoked permission arrive the same way
		// as a mismatched AAD, and the first two are operational problems.
		w, err := NewKeyWrapper(t.Context(), validConfig(), &fakeClient{err: errKMS})
		must.NoError(t, err)

		_, err = w.Unwrap(t.Context(), []byte("wrapped"), nil)
		test.ErrorIs(t, err, errKMS)
	})
}

func TestGCPKeyWrapper_Close(T *testing.T) {
	T.Parallel()

	T.Run("releases the underlying client", func(t *testing.T) {
		t.Parallel()

		client := &fakeClient{}

		w, err := NewKeyWrapper(t.Context(), validConfig(), client)
		must.NoError(t, err)

		must.NoError(t, w.Close())
		test.True(t, client.closed)
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
