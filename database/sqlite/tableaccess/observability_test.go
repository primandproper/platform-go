package tableaccess

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestManager_Observability(T *testing.T) {
	T.Parallel()

	T.Run("a refusal is recorded rather than only returned", func(t *testing.T) {
		t.Parallel()

		obs := observability.NewRecordingObserver()
		manager := &Manager{o11y: obs}

		test.ErrorIs(t, manager.CreateUser(t.Context(), "someone", "hunter2"), ErrNotSupported)

		must.SliceLen(t, 1, obs.Operations)
		must.True(t, obs.Operations[0].Ended)
		must.SliceLen(t, 1, obs.Operations[0].Errors)
		test.ErrorIs(t, obs.Operations[0].Errors[0], ErrNotSupported)
	})

	T.Run("an unconfigured manager still builds", func(t *testing.T) {
		t.Parallel()

		manager := NewManager()

		must.NotNil(t, manager)
		test.NotNil(t, manager.o11y)
	})
}
