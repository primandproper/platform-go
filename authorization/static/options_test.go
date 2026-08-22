package static

import (
	"testing"

	"github.com/primandproper/platform-go/v13/authorization"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithLogger attaches a logger", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(testRoles(), WithLogger(loggingnoop.NewLogger()))

		must.NoError(t, err)
		test.NotNil(t, r.logger)
	})

	// The warning path a zero-role policy takes at construction: it must build
	// and log rather than refuse, or the default provider would be unusable
	// without configuration.
	T.Run("WithLogger sees the empty-policy warning", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(nil, WithLogger(loggingnoop.NewLogger()))

		must.NoError(t, err)
		test.NotNil(t, r)
	})

	T.Run("a nil option is ignored", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(testRoles(), nil)

		must.NoError(t, err)
		test.NotNil(t, r)
	})

	T.Run("a nil logger is replaced rather than dereferenced", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(testRoles(), WithLogger(nil))

		must.NoError(t, err)
		test.NotNil(t, r.logger)
	})
}

func TestResolver_ImplementsPolicyResolver(T *testing.T) {
	T.Parallel()

	T.Run("satisfies the interface", func(t *testing.T) {
		t.Parallel()

		var resolver authorization.PolicyResolver = newTestResolver(t)

		set, err := resolver.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)
		test.True(t, set.Has(permRead))

		roles, err := resolver.Roles(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, 3, roles)
	})
}
