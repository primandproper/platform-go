package textsearchcfg

import (
	"context"
	"testing"

	textsearch "github.com/primandproper/platform-go/v13/search/text"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type exampleDocument struct {
	ID string `json:"id"`
}

func TestRegisterIndex(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: ProviderNoop})

		RegisterIndex[exampleDocument](i, "example")

		index, err := do.Invoke[textsearch.Index[exampleDocument]](i)
		must.NoError(t, err)
		test.NotNil(t, index)
	})
}
