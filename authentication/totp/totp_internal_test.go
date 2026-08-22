package totp

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/observability"

	"github.com/pquerna/otp/totp"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const exampleInternalSecret = "HEREISASECRETWHICHIVEMADEUPBECAUSEIWANNATESTRELIABLY"

// newRecordingVerifier builds a verifier with a RecordingObserver swapped in, so a
// test can drive Verify and assert that it opened and closed an operation.
func newRecordingVerifier(t *testing.T) (*TOTPVerifier, *observability.RecordingObserver) {
	t.Helper()

	obs := observability.NewRecordingObserver()

	return &TOTPVerifier{o11y: obs}, obs
}

func TestVerifier_Verify_observability(T *testing.T) {
	T.Parallel()

	T.Run("valid code opens and ends an operation", func(t *testing.T) {
		t.Parallel()

		v, obs := newRecordingVerifier(t)

		code, err := totp.GenerateCode(exampleInternalSecret, time.Now().UTC())
		must.NoError(t, err)

		test.NoError(t, v.Verify(t.Context(), exampleInternalSecret, code))

		must.SliceLen(t, 1, obs.Operations)
		must.True(t, obs.Operations[0].Ended)
		must.SliceEmpty(t, obs.Operations[0].Errors)
	})

	T.Run("empty code still ends the operation", func(t *testing.T) {
		t.Parallel()

		v, obs := newRecordingVerifier(t)

		test.ErrorIs(t, v.Verify(t.Context(), exampleInternalSecret, ""), ErrCodeRequired)

		must.SliceLen(t, 1, obs.Operations)
		must.True(t, obs.Operations[0].Ended)
	})

	T.Run("invalid code still ends the operation", func(t *testing.T) {
		t.Parallel()

		v, obs := newRecordingVerifier(t)

		test.ErrorIs(t, v.Verify(t.Context(), exampleInternalSecret, "000000"), ErrInvalidCode)

		must.SliceLen(t, 1, obs.Operations)
		must.True(t, obs.Operations[0].Ended)
	})
}

func TestTOTPVerifier_RecordsFailures(T *testing.T) {
	T.Parallel()

	// A failed second factor is the interesting half of this package, so it has
	// to reach the pillars rather than only the caller.
	for _, tc := range []struct {
		sentinel error
		name     string
		code     string
	}{
		{name: "an absent code", code: "", sentinel: ErrCodeRequired},
		{name: "a code that does not validate", code: "000000", sentinel: ErrInvalidCode},
	} {
		T.Run(tc.name+" is recorded", func(t *testing.T) {
			t.Parallel()

			v, obs := newRecordingVerifier(t)

			test.ErrorIs(t, v.Verify(t.Context(), exampleInternalSecret, tc.code), tc.sentinel)

			must.SliceLen(t, 1, obs.Operations)
			must.SliceLen(t, 1, obs.Operations[0].Errors)
			test.ErrorIs(t, obs.Operations[0].Errors[0], tc.sentinel)
		})
	}
}
