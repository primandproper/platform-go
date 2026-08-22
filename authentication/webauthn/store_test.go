package webauthn

import (
	stderrors "errors"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
)

func TestValidateSession(T *testing.T) {
	T.Parallel()

	T.Run("accepts a ceremony with a challenge and a positive TTL", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, ValidateSession(&SessionData{Challenge: "chal"}, time.Minute))
	})

	T.Run("refuses a nil ceremony", func(t *testing.T) {
		t.Parallel()

		err := ValidateSession(nil, time.Minute)
		test.ErrorIs(t, err, ErrNilSession)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	// The challenge is the ceremony's identity. Stored under nothing, it would
	// be handed to the next lookup that also had nothing.
	T.Run("refuses a ceremony with no challenge", func(t *testing.T) {
		t.Parallel()

		err := ValidateSession(&SessionData{}, time.Minute)
		test.ErrorIs(t, err, ErrChallengeRequired)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	// Zero is refused rather than read as "no expiry", which is the reading
	// every cache gives it: ceremony state that never expires is a challenge
	// that can be answered next year.
	T.Run("refuses a TTL that is not positive", func(t *testing.T) {
		t.Parallel()

		for _, ttl := range []time.Duration{0, -time.Nanosecond, -time.Hour} {
			test.ErrorIs(t, ValidateSession(&SessionData{Challenge: "chal"}, ttl), ErrNonPositiveTTL,
				test.Sprintf("ttl %v", ttl))
		}
	})

	// The order matters for the answer a caller gets: a nil session has no
	// challenge to complain about, so complaining about the challenge would
	// send them looking at the wrong argument.
	T.Run("reports the first thing that is wrong", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ValidateSession(nil, 0), ErrNilSession)
		test.ErrorIs(t, ValidateSession(&SessionData{}, 0), ErrChallengeRequired)
	})
}

func TestSentinels(T *testing.T) {
	T.Parallel()

	// The chain a caller checks against. Every unusable challenge reads as
	// ErrSessionNotFound, whether the store could tell why or not, so a caller
	// that does not care writes one check.
	T.Run("an expired ceremony reads as an absent one", func(t *testing.T) {
		t.Parallel()

		test.True(t, stderrors.Is(ErrSessionExpired, ErrSessionNotFound))
		test.False(t, stderrors.Is(ErrSessionNotFound, ErrSessionExpired))
	})

	T.Run("the argument sentinels wrap the platform's own", func(t *testing.T) {
		t.Parallel()

		for name, err := range map[string]error{
			"nil session":  ErrNilSession,
			"nil store":    ErrNilStore,
			"nil user":     ErrNilUser,
			"nil handler":  ErrNilHandler,
			"nil response": ErrNilResponse,
		} {
			test.ErrorIs(t, err, platformerrors.ErrNilInputParameter, test.Sprintf("sentinel %q", name))
		}

		test.ErrorIs(t, ErrChallengeRequired, platformerrors.ErrEmptyInputParameter)
	})
}
