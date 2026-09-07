package injection

import (
	"testing"

	"github.com/primandproper/platform-go/v14/errors"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type service struct{ name string }

// dependency is what a service's provider might ask the container for and not
// find.
type dependency struct{}

// iface is what a concrete may be aliased to.
type iface interface{ Name() string }

func (s *service) Name() string { return s.name }

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

	T.Run("a registered service whose provider invokes an unregistered dependency is an error", func(t *testing.T) {
		t.Parallel()

		// The case errors.Is(err, do.ErrServiceNotFound) gets wrong: do reports
		// the nested miss with the same sentinel as an absent service, so a
		// helper reading the sentinel would hand back the zero value here and
		// the container would wire up around a dependency the consumer
		// configured and never got.
		i := do.New()
		do.Provide(i, func(i do.Injector) (*service, error) {
			if _, err := do.Invoke[*dependency](i); err != nil {
				return nil, err
			}

			return &service{name: "built"}, nil
		})

		got, err := InvokeOptional[*service](i)
		must.Error(t, err)
		test.ErrorIs(t, err, do.ErrServiceNotFound, test.Sprint("the nested miss is reported as do reports it"))
		test.Nil(t, got)
	})

	T.Run("an explicit alias resolves", func(t *testing.T) {
		t.Parallel()

		// The presence check has to agree with do.Invoke wherever do.Invoke
		// would succeed, and an alias is the case where the registered name
		// and the provided type differ.
		want := &service{name: "aliased"}

		i := do.New()
		do.ProvideValue(i, want)
		must.NoError(t, do.As[*service, iface](i))

		got, err := InvokeOptional[iface](i)
		must.NoError(t, err)
		test.Eq[iface](t, want, got)
	})

	T.Run("a service registered on an ancestor scope resolves", func(t *testing.T) {
		t.Parallel()

		want := &service{name: "inherited"}

		root := do.New()
		do.ProvideValue(root, want)

		got, err := InvokeOptional[*service](root.Scope("child"))
		must.NoError(t, err)
		test.Eq(t, want, got)
	})

	T.Run("resolves from inside a provider", func(t *testing.T) {
		t.Parallel()

		// A registration's provider is handed do's virtual scope, not the
		// scope it registered on, and that is where every caller in this
		// module invokes from.
		want := &service{name: "dependency"}

		root := do.New()
		do.ProvideValue(root, want)

		child := root.Scope("child")
		do.Provide(child, func(i do.Injector) (*dependency, error) {
			got, err := InvokeOptional[*service](i)
			must.NoError(t, err)
			test.Eq(t, want, got)

			absent, err := InvokeOptional[iface](i)
			must.NoError(t, err)
			test.Nil(t, absent)

			return &dependency{}, nil
		})

		_, err := do.Invoke[*dependency](child)
		must.NoError(t, err)
	})
}
