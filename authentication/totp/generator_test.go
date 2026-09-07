package totp_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	authtotp "github.com/primandproper/platform-go/v14/authentication/totp"
	platformerrors "github.com/primandproper/platform-go/v14/errors"

	"github.com/pquerna/otp/totp"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewGenerator(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		g := authtotp.NewGenerator()
		must.NotNil(t, g)
	})
}

func TestGenerator_Generate(T *testing.T) {
	T.Parallel()

	T.Run("mints a secret the verifier accepts", func(t *testing.T) {
		t.Parallel()

		enrollment, err := authtotp.NewGenerator().Generate(t.Context(), "Example", "user@example.com")
		must.NoError(t, err)
		must.NotNil(t, enrollment)
		test.NotEq(t, "", enrollment.Secret)

		// The whole point of the pair: a code derived from the secret this
		// returned verifies against it, so an enrollment is usable without
		// anything in between reinterpreting the value.
		code, err := totp.GenerateCode(enrollment.Secret, time.Now().UTC())
		must.NoError(t, err)

		test.NoError(t, authtotp.NewVerifier().Verify(t.Context(), enrollment.Secret, code))
	})

	T.Run("the URI carries the labels and the same secret", func(t *testing.T) {
		t.Parallel()

		enrollment, err := authtotp.NewGenerator().Generate(t.Context(), "Example", "user@example.com")
		must.NoError(t, err)

		parsed, err := url.Parse(enrollment.URI)
		must.NoError(t, err)

		test.EqOp(t, "otpauth", parsed.Scheme)
		test.EqOp(t, "totp", parsed.Host)
		test.True(t, strings.Contains(parsed.Path, "user@example.com"))
		test.EqOp(t, "Example", parsed.Query().Get("issuer"))
		test.EqOp(t, enrollment.Secret, parsed.Query().Get("secret"))
	})

	T.Run("two calls mint two secrets", func(t *testing.T) {
		t.Parallel()

		g := authtotp.NewGenerator()

		first, err := g.Generate(t.Context(), "Example", "user@example.com")
		must.NoError(t, err)

		second, err := g.Generate(t.Context(), "Example", "user@example.com")
		must.NoError(t, err)

		test.NotEq(t, first.Secret, second.Secret)
	})

	T.Run("empty issuer", func(t *testing.T) {
		t.Parallel()

		enrollment, err := authtotp.NewGenerator().Generate(t.Context(), "", "user@example.com")
		test.Nil(t, enrollment)
		test.ErrorIs(t, err, authtotp.ErrEmptyIssuer)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	T.Run("empty account name", func(t *testing.T) {
		t.Parallel()

		enrollment, err := authtotp.NewGenerator().Generate(t.Context(), "Example", "")
		test.Nil(t, enrollment)
		test.ErrorIs(t, err, authtotp.ErrEmptyAccountName)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})
}
