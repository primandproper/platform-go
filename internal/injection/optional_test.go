package injection

import (
	"testing"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type service struct{ name string }

func TestInvokeOptional(T *testing.T) {
	T.Parallel()

	T.Run("returns a registered service", func(t *testing.T) {
		t.Parallel()

		want := &service{name: "registered"}

		i := do.New()
		do.ProvideValue(i, want)

		got, err := InvokeOptional[*service](i)
		must.NoError(t, err)
		test.Eq(t, want, got)
	})

	T.Run("an unregistered service is the zero value, not an error", func(t *testing.T) {
		t.Parallel()

		// The distinction this function exists for: nobody registered one, so
		// the caller gets nothing to work with rather than a failure.
		got, err := InvokeOptional[*service](do.New())
		must.NoError(t, err)
		test.Nil(t, got)
	})

	T.Run("a registered service that fails to build is an error", func(t *testing.T) {
		t.Parallel()

		// The other half: this one was asked for and could not be built, which
		// must not be reported as if it had never been asked for.
		errBuild := errors.New("building the service")

		i := do.New()
		do.Provide(i, func(do.Injector) (*service, error) {
			return nil, errBuild
		})

		got, err := InvokeOptional[*service](i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
		test.Nil(t, got)
	})
}
