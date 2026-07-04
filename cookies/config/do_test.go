package cookiescfg

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v3/cookies"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterCookieManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		key := base64.StdEncoding.EncodeToString([]byte("HEREISA32CHARSECRETWHICHISMADEUP"))
		cfg := &cookies.Config{
			CookieName:            t.Name(),
			Base64EncodedHashKey:  key,
			Base64EncodedBlockKey: key,
			Lifetime:              24 * time.Hour,
		}

		i := do.New()
		do.ProvideValue(i, cfg)
		do.ProvideValue(i, tracingnoop.NewTracerProvider())

		RegisterCookieManager(i)

		manager, err := do.Invoke[cookies.Manager](i)
		must.NoError(t, err)
		test.NotNil(t, manager)
	})
}
