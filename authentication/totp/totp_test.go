package totp_test

import (
	"testing"
	"time"

	authtotp "github.com/primandproper/platform-go/authentication/totp"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/pquerna/otp/totp"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const exampleSecret = "HEREISASECRETWHICHIVEMADEUPBECAUSEIWANNATESTRELIABLY"

func TestNewVerifier(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		v := authtotp.NewVerifier(tracingnoop.NewTracerProvider())
		must.NotNil(t, v)
	})
}

func TestVerifier_Verify(T *testing.T) {
	T.Parallel()

	T.Run("valid code", func(t *testing.T) {
		t.Parallel()

		v := authtotp.NewVerifier(tracingnoop.NewTracerProvider())

		code, err := totp.GenerateCode(exampleSecret, time.Now().UTC())
		must.NoError(t, err)

		test.NoError(t, v.Verify(t.Context(), exampleSecret, code))
	})

	T.Run("empty code returns ErrCodeRequired", func(t *testing.T) {
		t.Parallel()

		v := authtotp.NewVerifier(tracingnoop.NewTracerProvider())

		err := v.Verify(t.Context(), exampleSecret, "")
		test.ErrorIs(t, err, authtotp.ErrCodeRequired)
	})

	T.Run("invalid code returns ErrInvalidCode", func(t *testing.T) {
		t.Parallel()

		v := authtotp.NewVerifier(tracingnoop.NewTracerProvider())

		err := v.Verify(t.Context(), exampleSecret, "000000")
		test.ErrorIs(t, err, authtotp.ErrInvalidCode)
	})
}
