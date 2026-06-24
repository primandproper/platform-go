package httpclient

import (
	"net/http"
	"testing"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterHTTPClient(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()

		cfg := &Config{}
		cfg.EnsureDefaults()
		do.ProvideValue(i, cfg)

		test.NotPanic(t, func() {
			RegisterHTTPClient(i)
		})

		client, err := do.Invoke[*http.Client](i)
		must.NoError(t, err)
		test.NotNil(t, client)
	})
}
