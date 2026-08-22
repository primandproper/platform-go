package routingcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"
	"github.com/primandproper/platform-go/v13/routing/backends/gin"
	"github.com/primandproper/platform-go/v13/routing/backends/httprouter"
	"github.com/primandproper/platform-go/v13/routing/backends/stdlib"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func testEncoder() encoding.ServerEncoderDecoder {
	return encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	configured := []struct {
		cfg      *Config
		provider string
	}{
		{provider: ProviderChi, cfg: &Config{Provider: ProviderChi, Chi: &chi.Config{ServiceName: "svc"}}},
		{provider: ProviderStdlib, cfg: &Config{Provider: ProviderStdlib, Stdlib: &stdlib.Config{ServiceName: "svc"}}},
		{provider: ProviderHTTPRouter, cfg: &Config{Provider: ProviderHTTPRouter, HTTPRouter: &httprouter.Config{ServiceName: "svc"}}},
		{provider: ProviderGin, cfg: &Config{Provider: ProviderGin, Gin: &gin.Config{ServiceName: "svc"}}},
	}

	for _, tc := range configured {
		T.Run(tc.provider+" is a valid provider when its backend is configured", func(t *testing.T) {
			t.Parallel()

			test.NoError(t, tc.cfg.ValidateWithContext(t.Context()))
		})
	}

	// Every backend requires a ServiceName, and naming one without configuring
	// it used to validate clean and then run with an empty service name on all
	// of its spans.
	for _, provider := range []string{ProviderChi, ProviderStdlib, ProviderHTTPRouter, ProviderGin} {
		T.Run(provider+" is refused when its backend is not configured", func(t *testing.T) {
			t.Parallel()

			cfg := &Config{Provider: provider}
			test.Error(t, cfg.ValidateWithContext(t.Context()))
		})
	}

	// Dispatch has always normalized; validation used to compare the raw string,
	// so a provider spelled with capitals reached neither.
	T.Run("a provider is matched case- and space-insensitively", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: " Chi ", Chi: &chi.Config{ServiceName: "svc"}}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "bogus",
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestNewBackend(T *testing.T) {
	T.Parallel()

	cases := []struct {
		cfg  *Config
		name string
	}{
		{name: "chi", cfg: &Config{Provider: ProviderChi, Chi: &chi.Config{ServiceName: "chi"}}},
		{name: "stdlib", cfg: &Config{Provider: ProviderStdlib, Stdlib: &stdlib.Config{ServiceName: "stdlib"}}},
		{name: "httprouter", cfg: &Config{Provider: ProviderHTTPRouter, HTTPRouter: &httprouter.Config{ServiceName: "httprouter"}}},
		{name: "gin", cfg: &Config{Provider: ProviderGin, Gin: &gin.Config{ServiceName: "gin"}}},
	}

	for _, tc := range cases {
		T.Run("with "+tc.name+" provider", func(t *testing.T) {
			t.Parallel()

			backend, err := NewBackend(t.Context(), tc.cfg)
			must.NoError(t, err)
			test.NotNil(t, backend)
		})
	}

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		backend, err := NewBackend(t.Context(), cfg)
		test.Nil(t, backend)
		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
	})
}

func TestNewRouter(T *testing.T) {
	T.Parallel()

	T.Run("with chi provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderChi,
			Chi:      &chi.Config{ServiceName: t.Name()},
		}

		router, err := NewRouter(t.Context(), cfg, testEncoder())
		must.NoError(t, err)
		test.NotNil(t, router)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "bogus",
		}

		router, err := NewRouter(t.Context(), cfg, testEncoder())
		test.Nil(t, router)
		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
	})
}
