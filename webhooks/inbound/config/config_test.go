package inboundcfg

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	"github.com/primandproper/platform-go/v13/cryptography/hashing/hmac"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	mqmock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var errNoSuchTopic = platformerrors.New("no such topic")

// publisherProvider hands out a publisher for any topic, recording which ones were asked for.
func publisherProvider(err error, topics *[]string) messagequeue.PublisherProvider {
	return &mqmock.PublisherProviderMock{
		NewPublisherFunc: func(_ context.Context, topic string) (messagequeue.Publisher, error) {
			*topics = append(*topics, topic)

			if err != nil {
				return nil, err
			}

			return &mqmock.PublisherMock{StopFunc: func() {}}, nil
		},
		CloseFunc: func() {},
	}
}

// validConfig is a Config that passes validation, for tests that vary one field.
func validConfig() *Config {
	return &Config{
		Provider: ProviderGitHub,
		Topic:    "webhooks.github",
		Secret:   "It's a Secret to Everybody",
	}
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills the unset knobs", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.EqOp(t, inbound.DefaultTolerance, cfg.Tolerance)
		test.EqOp(t, inbound.DefaultMaxBodyBytes, cfg.MaxBodyBytes)
	})

	T.Run("leaves what was set alone", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Tolerance: time.Minute, MaxBodyBytes: 1024}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Minute, cfg.Tolerance)
		test.EqOp(t, int64(1024), cfg.MaxBodyBytes)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a complete config", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, validConfig().ValidateWithContext(t.Context()))
	})

	// Dispatch lowercases and trims, so validating the raw value would reject spellings the
	// factory happily accepts.
	T.Run("accepts a provider in any case", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Provider = "  GitHub "

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Provider = "paypal"

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("requires a provider, a topic, and a secret", func(t *testing.T) {
		t.Parallel()

		for name, mangle := range map[string]func(*Config){
			"no provider": func(cfg *Config) { cfg.Provider = "" },
			"no topic":    func(cfg *Config) { cfg.Topic = "" },
			"no secret":   func(cfg *Config) { cfg.Secret = "" },
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				cfg := validConfig()
				mangle(cfg)

				test.Error(t, cfg.ValidateWithContext(t.Context()))
			})
		}
	})

	T.Run("requires the HMAC section only for the hmac provider", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Provider = ProviderHMAC

		test.Error(t, cfg.ValidateWithContext(t.Context()))

		cfg.HMAC = HMACConfig{Provider: "acme", Header: "X-Acme-Signature"}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))

		// The same empty section is fine for a provider that does not read it.
		github := validConfig()
		test.NoError(t, github.ValidateWithContext(t.Context()))
	})
}

func TestNewVerifier(T *testing.T) {
	T.Parallel()

	body := []byte("Hello, World!")

	T.Run("builds the GitHub scheme", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewVerifier(t.Context(), validConfig())
		must.NoError(t, err)

		headers := http.Header{}
		headers.Set(inbound.GitHubSignatureHeader, "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17")

		test.EqOp(t, "github", verifier.Provider())
		test.NoError(t, verifier.Verify(t.Context(), headers, body))
	})

	T.Run("builds the Stripe scheme", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Provider = ProviderStripe

		verifier, err := NewVerifier(t.Context(), cfg)
		must.NoError(t, err)

		test.EqOp(t, "stripe", verifier.Provider())
	})

	T.Run("builds the generic HMAC scheme", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Provider = ProviderHMAC
		cfg.Secret = "sekrit"
		cfg.HMAC = HMACConfig{
			Provider: "acme",
			Header:   "X-Acme-Signature",
			Digest:   "SHA512",
			Encoding: "Hex",
		}

		verifier, err := NewVerifier(t.Context(), cfg)
		must.NoError(t, err)

		headers := http.Header{}
		headers.Set("X-Acme-Signature", hashing.Hex(hmac.NewHMACSHA512Hasher([]byte("sekrit")), body))

		test.EqOp(t, "acme", verifier.Provider())
		test.NoError(t, verifier.Verify(t.Context(), headers, body))
	})

	T.Run("passes the configured additional secrets through", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Secret = "incoming"
		cfg.AdditionalSecrets = []string{"outgoing"}

		verifier, err := NewVerifier(t.Context(), cfg)
		must.NoError(t, err)

		headers := http.Header{}
		headers.Set(inbound.GitHubSignatureHeader, "sha256="+hashing.Hex(hmac.NewHMACSHA256Hasher([]byte("outgoing")), body))

		test.NoError(t, verifier.Verify(t.Context(), headers, body))
	})

	T.Run("applies explicit options after the configured ones", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Secret = "configured"

		verifier, err := NewVerifier(t.Context(), cfg, WithVerifierOptions(inbound.WithAdditionalSecrets("explicit")))
		must.NoError(t, err)

		headers := http.Header{}
		headers.Set(inbound.GitHubSignatureHeader, "sha256="+hashing.Hex(hmac.NewHMACSHA256Hasher([]byte("explicit")), body))

		test.NoError(t, verifier.Verify(t.Context(), headers, body))
	})

	T.Run("refuses a nil or invalid config", func(t *testing.T) {
		t.Parallel()

		verifier, err := NewVerifier(t.Context(), nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, verifier)

		verifier, err = NewVerifier(t.Context(), &Config{Provider: "paypal", Topic: "t", Secret: "s"})
		test.Error(t, err)
		test.Nil(t, verifier)
	})
}

func TestNewReceiver(T *testing.T) {
	T.Parallel()

	T.Run("builds a publisher for the configured topic", func(t *testing.T) {
		t.Parallel()

		var topics []string

		receiver, err := NewReceiver(t.Context(), validConfig(), publisherProvider(nil, &topics))

		must.NoError(t, err)
		test.NotNil(t, receiver)
		test.Eq(t, []string{"webhooks.github"}, topics)
	})

	T.Run("reports a publisher that could not be built", func(t *testing.T) {
		t.Parallel()

		var topics []string

		receiver, err := NewReceiver(t.Context(), validConfig(), publisherProvider(errNoSuchTopic, &topics))

		test.ErrorIs(t, err, errNoSuchTopic)
		test.Nil(t, receiver)
	})

	T.Run("refuses a nil publisher provider", func(t *testing.T) {
		t.Parallel()

		receiver, err := NewReceiver(t.Context(), validConfig(), nil)

		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, receiver)
	})

	// The verifier is built first, so a config that cannot describe a scheme never reaches the
	// broker at all.
	T.Run("refuses an invalid config without touching the broker", func(t *testing.T) {
		t.Parallel()

		var topics []string

		receiver, err := NewReceiver(t.Context(), &Config{}, publisherProvider(nil, &topics))

		test.Error(t, err)
		test.Nil(t, receiver)
		test.SliceLen(t, 0, topics)
	})
}
