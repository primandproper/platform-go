package httpclient

import (
	"net/http"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_BuildClient(T *testing.T) {
	T.Parallel()

	T.Run("with tracing enabled", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Timeout:       2 * time.Second,
			EnableTracing: true,
		}
		cfg.EnsureDefaults()

		client := cfg.BuildClient()
		must.NotNil(t, client)
		test.EqOp(t, 2*time.Second, client.Timeout)
		test.NotNil(t, client.Transport)
	})

	T.Run("with tracing disabled", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Timeout:       3 * time.Second,
			EnableTracing: false,
		}
		cfg.EnsureDefaults()

		client := cfg.BuildClient()
		must.NotNil(t, client)
		test.EqOp(t, 3*time.Second, client.Timeout)
		test.NotNil(t, client.Transport)
	})

	T.Run("applies MaxIdleConns and MaxIdleConnsPerHost", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Timeout:             time.Second,
			MaxIdleConns:        42,
			MaxIdleConnsPerHost: 21,
			EnableTracing:       false,
		}
		cfg.EnsureDefaults()

		client := cfg.BuildClient()
		must.NotNil(t, client)

		transport, ok := client.Transport.(*http.Transport)
		must.True(t, ok)
		test.EqOp(t, 42, transport.MaxIdleConns)
		test.EqOp(t, 21, transport.MaxIdleConnsPerHost)
	})
}

func TestNewHTTPClient(T *testing.T) {
	T.Parallel()

	T.Run("with nil config uses defaults", func(t *testing.T) {
		t.Parallel()

		client := NewHTTPClient(nil)
		must.NotNil(t, client)
		test.EqOp(t, defaultTimeout, client.Timeout)
	})

	T.Run("with config uses config values", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Timeout: 7 * time.Second,
		}
		client := NewHTTPClient(cfg)
		must.NotNil(t, client)
		test.EqOp(t, 7*time.Second, client.Timeout)
	})
}
