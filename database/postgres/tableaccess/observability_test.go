package tableaccess

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestManager_Observability(T *testing.T) {
	T.Parallel()

	T.Run("a refused privilege is recorded rather than only returned", func(t *testing.T) {
		t.Parallel()

		obs := observability.NewRecordingObserver()
		manager := &Manager{o11y: obs}

		// The one rejection that never reaches the database, and so the one this
		// package can assert without one.
		err := manager.GrantUserAccessToTable(t.Context(), "someone", "public", "widgets", "DROP")
		must.Error(t, err)

		must.SliceLen(t, 1, obs.Operations)
		must.True(t, obs.Operations[0].Ended)
		must.SliceLen(t, 1, obs.Operations[0].Errors)

		// The span says who was asking for what, which is the whole of what an
		// audit of a refused grant wants.
		obs.ObservedOperationWithData(t, map[string]any{
			usernameKey:  "someone",
			schemaKey:    "public",
			tableKey:     "widgets",
			privilegeKey: "DROP",
		})
	})

	T.Run("an unconfigured manager still builds", func(t *testing.T) {
		t.Parallel()

		manager := NewManager(nil)

		must.NotNil(t, manager)
		test.NotNil(t, manager.o11y)
	})
}
