package argon2_test

import (
	"testing"

	"github.com/primandproper/platform-go/authentication/argon2"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test"
)

const (
	examplePassword = "Pa$$w0rdPa$$w0rdPa$$w0rdPa$$w0rd"

	argon2HashedExamplePassword = `$argon2id$v=19$m=65536,t=1,p=2$C+YWiNi21e94acF3ip8UGA$Ru6oL96HZSP7cVcfAbRwOuK9+vwBo/BLhCzOrGrMH0M`
)

func TestArgon2_HashPassword(T *testing.T) {
	T.Parallel()

	x := argon2.ProvideArgon2Authenticator(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		actual, err := x.HashPassword(ctx, examplePassword)
		test.NoError(t, err)
		test.NotEq(t, "", actual)
	})
}

func TestArgon2_PasswordMatches(T *testing.T) {
	T.Parallel()

	x := argon2.ProvideArgon2Authenticator(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())

	T.Run("matching password returns true", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		matches, err := x.PasswordMatches(ctx, argon2HashedExamplePassword, examplePassword)
		test.NoError(t, err)
		test.True(t, matches)
	})

	T.Run("non-matching password returns false with no error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		matches, err := x.PasswordMatches(ctx, argon2HashedExamplePassword, "wrongPassword")
		test.NoError(t, err)
		test.False(t, matches)
	})

	T.Run("malformed hash returns error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		matches, err := x.PasswordMatches(ctx, "       blah blah blah not a valid hash lol           ", examplePassword)
		test.Error(t, err)
		test.False(t, matches)
	})
}

func TestProvideArgon2Authenticator(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		argon2.ProvideArgon2Authenticator(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
	})
}
