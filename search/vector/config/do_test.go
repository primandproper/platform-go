package vectorsearchcfg

import (
	"context"
	"testing"

	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type exampleDocument struct {
	ID string `json:"id"`
}

func TestRegisterIndex(T *testing.T) {
	T.Parallel()

	T.Run("builds without a registered database.Client when the provider is not pgvector", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: ProviderNoop})

		RegisterIndex[exampleDocument](i, "example")

		index, err := do.Invoke[vectorsearch.Index[exampleDocument]](i)
		must.NoError(t, err)
		test.NotNil(t, index)
	})

	T.Run("errors when the provider is pgvector but no database.Client is registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: PGvectorProvider})

		RegisterIndex[exampleDocument](i, "example")

		index, err := do.Invoke[vectorsearch.Index[exampleDocument]](i)
		must.Error(t, err)
		test.Nil(t, index)
	})
}
