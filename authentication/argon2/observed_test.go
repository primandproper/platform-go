package argon2

import (
	"testing"

	"github.com/primandproper/platform-go/observability"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

const (
	observedExamplePassword = "Pa$$w0rdPa$$w0rdPa$$w0rdPa$$w0rd"

	observedArgon2HashedExamplePassword = `$argon2id$v=19$m=65536,t=1,p=2$C+YWiNi21e94acF3ip8UGA$Ru6oL96HZSP7cVcfAbRwOuK9+vwBo/BLhCzOrGrMH0M`
)

// newRecordingAuthenticator builds an Argon2Authenticator with a RecordingObserver
// swapped in, so a test can both drive a method and assert that it opened and ended
// an operation.
func newRecordingAuthenticator(t *testing.T) (*Argon2Authenticator, *observability.RecordingObserver) {
	t.Helper()

	a := &Argon2Authenticator{
		o11y: observability.NewObserver(serviceName, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider()),
	}

	obs := observability.NewRecordingObserver()
	a.o11y = obs

	return a, obs
}

func TestArgon2Authenticator_HashPassword_observed(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		a, obs := newRecordingAuthenticator(t)

		actual, err := a.HashPassword(t.Context(), observedExamplePassword)
		must.NoError(t, err)
		must.NotEq(t, "", actual)

		op := obs.ObservedOperationWithData(t, map[string]any{
			"argon2.memory":      argonParams.Memory,
			"argon2.iterations":  argonParams.Iterations,
			"argon2.parallelism": argonParams.Parallelism,
			"argon2.key_length":  argonParams.KeyLength,
		})

		must.SliceLen(t, 1, obs.Operations)
		must.True(t, op.Ended)
	})
}

func TestArgon2Authenticator_PasswordMatches_observed(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		a, obs := newRecordingAuthenticator(t)

		matches, err := a.PasswordMatches(t.Context(), observedArgon2HashedExamplePassword, observedExamplePassword)
		must.NoError(t, err)
		must.True(t, matches)

		op := obs.ObservedOperationWithData(t, map[string]any{
			"argon2.memory":      argonParams.Memory,
			"argon2.iterations":  argonParams.Iterations,
			"argon2.parallelism": argonParams.Parallelism,
			"argon2.key_length":  argonParams.KeyLength,
		})

		must.SliceLen(t, 1, obs.Operations)
		must.True(t, op.Ended)
	})

	T.Run("malformed hash returns error and ends operation", func(t *testing.T) {
		t.Parallel()

		a, obs := newRecordingAuthenticator(t)

		matches, err := a.PasswordMatches(t.Context(), "       blah blah blah not a valid hash lol           ", observedExamplePassword)
		must.Error(t, err)
		must.False(t, matches)

		op := obs.ObservedOperationWithData(t, map[string]any{
			"argon2.memory":      argonParams.Memory,
			"argon2.iterations":  argonParams.Iterations,
			"argon2.parallelism": argonParams.Parallelism,
			"argon2.key_length":  argonParams.KeyLength,
		})

		must.SliceLen(t, 1, obs.Operations)
		must.True(t, op.Ended)
	})
}
