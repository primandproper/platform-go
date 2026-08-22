package encryption

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

var errInstrument = errors.New("creating the instrument")

// providerFailingOn is a metrics.Provider that refuses to build exactly one
// counter, so each of NewKeyring's instrument failures can be reached
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
	}
}

// failingCipher refuses in both directions.
type failingCipher struct{}

func (failingCipher) Seal(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errInstrument
}

func (failingCipher) Open(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errInstrument
}

func TestNewKeyring_instrumentFailures(T *testing.T) {
	T.Parallel()

	// A metrics provider that cannot build a counter is a misconfiguration,
	// and the constructor has to say so rather than carry on unmetered — the
	// counters are the only way a rotation's progress is visible.
	for _, counter := range []string{
		keyringName + "_encryptions",
		keyringName + "_decryptions",
		keyringName + "_unknown_key_ids",
	} {
		T.Run(counter, func(t *testing.T) {
			t.Parallel()

			_, err := NewKeyring("k1",
				[]RingKey{{ID: "k1", Cipher: fakeCipher{tag: 1}}},
				WithMetricsProvider(providerFailingOn(counter)),
			)
			must.Error(t, err)
			test.ErrorIs(t, err, errInstrument)
		})
	}
}

func TestKeyring_cipherFailures(T *testing.T) {
	T.Parallel()

	T.Run("a cipher that cannot seal fails the encryption", func(t *testing.T) {
		t.Parallel()

		ring, err := NewKeyring("k1", []RingKey{{ID: "k1", Cipher: failingCipher{}}})
		must.NoError(t, err)

		_, err = ring.Encrypt(t.Context(), []byte("secret"), nil)
		test.ErrorIs(t, err, errInstrument)
	})

	T.Run("a cipher that cannot open fails the decryption", func(t *testing.T) {
		t.Parallel()

		// Framed by a working ring so the failure is the Cipher's rather than
		// the frame's — the header still has to parse for Open to be reached.
		writer, err := NewKeyring("k1", []RingKey{{ID: "k1", Cipher: fakeCipher{tag: 1}}})
		must.NoError(t, err)

		ciphertext, err := writer.Encrypt(t.Context(), []byte("secret"), nil)
		must.NoError(t, err)

		reader, err := NewKeyring("k1", []RingKey{{ID: "k1", Cipher: failingCipher{}}})
		must.NoError(t, err)

		_, err = reader.Decrypt(t.Context(), ciphertext, nil)
		test.ErrorIs(t, err, errInstrument)
	})
}
