package authorizationcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/authorization"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterPolicyResolver(T *testing.T) {
	T.Parallel()

	T.Run("builds the static resolver without a database.Client or cache", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: ProviderStatic})

		RegisterPolicyResolver(i)

		resolver, err := do.Invoke[authorization.PolicyResolver](i)
		must.NoError(t, err)
		test.NotNil(t, resolver)
	})

	T.Run("errors when the provider is database but no database.Client is registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: ProviderDatabase})

		RegisterPolicyResolver(i)

		resolver, err := do.Invoke[authorization.PolicyResolver](i)
		must.Error(t, err)
		test.Nil(t, resolver)
	})
}
