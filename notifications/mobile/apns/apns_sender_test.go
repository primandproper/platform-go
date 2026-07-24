package apns

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v6/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

const validDeviceToken = "a1b2c3d4e5f67890a1b2c3d4e5f67890a1b2c3d4e5f67890a1b2c3d4e5f67890"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func createTestSenderWithTransport(t *testing.T, fn roundTripFunc) (*Sender, *observability.RecordingObserver) {
	t.Helper()

	p8Path := createTestP8File(t)
	cfg := &Config{
		AuthKeyPath: p8Path,
		KeyID:       "KEY123",
		TeamID:      "TEAM123",
		BundleID:    "com.example.app",
	}
	sender, err := NewSender(cfg, tracingnoop.NewTracerProvider(), loggingnoop.NewLogger(), nil)
	must.NoError(t, err)

	sender.client.HTTPClient = &http.Client{Transport: fn}
	sender.client.Host = "https://test.example.com"

	obs := observability.NewRecordingObserver()
	sender.o11y = obs

	return sender, obs
}

func createTestP8File(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must.NoError(t, err)

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	must.NoError(t, err)

	block := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "AuthKey.p8")
	must.NoError(t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600))
	return path
}

func TestNewSender(T *testing.T) {
	T.Parallel()

	logger := loggingnoop.NewLogger()
	tracingProvider := tracingnoop.NewTracerProvider()

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		sender, err := NewSender(nil, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "missing required config")
	})

	T.Run("with empty auth key path", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			KeyID:      "KEY123",
			TeamID:     "TEAM123",
			BundleID:   "com.example.app",
			Production: false,
		}
		sender, err := NewSender(cfg, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "missing required config")
	})

	T.Run("with empty key ID", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := &Config{
			AuthKeyPath: p8Path,
			TeamID:      "TEAM123",
			BundleID:    "com.example.app",
			Production:  false,
		}
		sender, err := NewSender(cfg, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "missing required config")
	})

	T.Run("with non-existent auth key file", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			AuthKeyPath: filepath.Join(t.TempDir(), "nonexistent.p8"),
			KeyID:       "KEY123",
			TeamID:      "TEAM123",
			BundleID:    "com.example.app",
			Production:  false,
		}
		sender, err := NewSender(cfg, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "loading auth key")
	})

	T.Run("with empty team ID", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := &Config{
			AuthKeyPath: p8Path,
			KeyID:       "KEY123",
			BundleID:    "com.example.app",
		}
		sender, err := NewSender(cfg, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "missing required config")
	})

	T.Run("with empty bundle ID", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := &Config{
			AuthKeyPath: p8Path,
			KeyID:       "KEY123",
			TeamID:      "TEAM123",
		}
		sender, err := NewSender(cfg, tracingProvider, logger, nil)
		test.Nil(t, sender)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "missing required config")
	})

	T.Run("with valid config", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := &Config{
			AuthKeyPath: p8Path,
			KeyID:       "KEY123",
			TeamID:      "TEAM123",
			BundleID:    "com.example.app",
			Production:  false,
		}
		sender, err := NewSender(cfg, tracingProvider, logger, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
		test.EqOp(t, "com.example.app", sender.topic)
	})

	T.Run("with production config", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := &Config{
			AuthKeyPath: p8Path,
			KeyID:       "KEY123",
			TeamID:      "TEAM123",
			BundleID:    "com.example.app",
			Production:  true,
		}
		sender, err := NewSender(cfg, tracingProvider, logger, nil)
		must.NoError(t, err)
		must.NotNil(t, sender)
		test.EqOp(t, "com.example.app", sender.topic)
	})

	T.Run("with send counter creation error", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := &Config{
			AuthKeyPath: p8Path,
			KeyID:       "KEY123",
			TeamID:      "TEAM123",
			BundleID:    "com.example.app",
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, o11yName+"_sends", counterName)
				return (*metrics.Int64CounterImpl)(nil), errors.New("counter error")
			},
		}

		sender, err := NewSender(cfg, tracingProvider, logger, mp)
		test.Nil(t, sender)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "creating send counter")

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error counter creation error", func(t *testing.T) {
		t.Parallel()

		p8Path := createTestP8File(t)
		cfg := &Config{
			AuthKeyPath: p8Path,
			KeyID:       "KEY123",
			TeamID:      "TEAM123",
			BundleID:    "com.example.app",
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case o11yName + "_sends":
					return (*metrics.Int64CounterImpl)(nil), nil
				case o11yName + "_errors":
					return (*metrics.Int64CounterImpl)(nil), errors.New("counter error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		sender, err := NewSender(cfg, tracingProvider, logger, mp)
		test.Nil(t, sender)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "creating error counter")

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})
}

func TestSender_Send(T *testing.T) {
	T.Parallel()

	ctx := T.Context()

	T.Run("successful push", func(t *testing.T) {
		t.Parallel()

		sender, obs := createTestSenderWithTransport(t, func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Apns-Id": {"test-apns-id"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})

		err := sender.Send(ctx, validDeviceToken, "title", "body", nil)
		test.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			"title": "title",
		})
	})

	T.Run("successful push with badge count", func(t *testing.T) {
		t.Parallel()

		sender, _ := createTestSenderWithTransport(t, func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Apns-Id": {"test-apns-id"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		})

		badge := 5
		err := sender.Send(ctx, validDeviceToken, "title", "body", &badge)
		test.NoError(t, err)
	})

	T.Run("push returns transport error", func(t *testing.T) {
		t.Parallel()

		sender, _ := createTestSenderWithTransport(t, func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("transport error")
		})

		err := sender.Send(ctx, validDeviceToken, "title", "body", nil)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "push failed")
	})

	T.Run("push returns non-sent response", func(t *testing.T) {
		t.Parallel()

		sender, obs := createTestSenderWithTransport(t, func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Apns-Id": {"test-apns-id"}},
				Body:       io.NopCloser(strings.NewReader(`{"reason":"BadDeviceToken"}`)),
			}, nil
		})

		err := sender.Send(ctx, validDeviceToken, "title", "body", nil)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "BadDeviceToken")

		// Even though the push failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"title":        "title",
			keys.ReasonKey: "BadDeviceToken",
			"statusCode":   http.StatusBadRequest,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestSender_Send_rejectsInvalidDeviceToken(T *testing.T) {
	T.Parallel()

	p8Path := createTestP8File(T)
	cfg := &Config{
		AuthKeyPath: p8Path,
		KeyID:       "KEY123",
		TeamID:      "TEAM123",
		BundleID:    "com.example.app",
		Production:  false,
	}
	sender, err := NewSender(cfg, tracingnoop.NewTracerProvider(), loggingnoop.NewLogger(), nil)
	must.NoError(T, err)

	ctx := T.Context()

	T.Run("rejects binary/garbage token", func(t *testing.T) {
		t.Parallel()
		// Simulates decrypted garbage (e.g. wrong key or corrupted data)
		invalidToken := "x\x89\xbf\x1f\xa0\x93\x12\xf5"
		sendErr := sender.Send(ctx, invalidToken, "title", "body", nil)
		must.Error(t, sendErr)
		test.StrContains(t, sendErr.Error(), "invalid device token format")
	})

	T.Run("rejects token with control characters", func(t *testing.T) {
		t.Parallel()
		invalidToken := "a1b2c3d4e5f6789012345678901234567890abcdef1234567890abcdef12345\t"
		sendErr := sender.Send(ctx, invalidToken, "title", "body", nil)
		must.Error(t, sendErr)
		test.StrContains(t, sendErr.Error(), "invalid device token format")
	})

	T.Run("rejects too short token", func(t *testing.T) {
		t.Parallel()
		sendErr := sender.Send(ctx, "abc123", "title", "body", nil)
		must.Error(t, sendErr)
		test.StrContains(t, sendErr.Error(), "invalid device token format")
	})
}
