package http

import (
	"database/sql"
	"errors"
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/links"
	"github.com/primandproper/platform-go/v13/sessions"

	"github.com/shoenig/test"
)

func TestPlatformMapper_Map(T *testing.T) {
	T.Parallel()

	T.Run("nil error returns ok=false", func(t *testing.T) {
		t.Parallel()
		_, _, ok := PlatformMapper.Map(nil)
		test.False(t, ok)
	})

	T.Run("sql.ErrNoRows maps to ErrDataNotFound", func(t *testing.T) {
		t.Parallel()
		code, msg, ok := PlatformMapper.Map(sql.ErrNoRows)
		test.True(t, ok)
		test.EqOp(t, ErrDataNotFound, code)
		test.EqOp(t, "data not found", msg)
	})

	T.Run("ErrUserAlreadyExists maps to ErrValidatingRequestInput", func(t *testing.T) {
		t.Parallel()
		code, msg, ok := PlatformMapper.Map(database.ErrUserAlreadyExists)
		test.True(t, ok)
		test.EqOp(t, ErrValidatingRequestInput, code)
		test.EqOp(t, "user already exists", msg)
	})

	T.Run("ErrCircuitBroken maps to ErrCircuitBroken", func(t *testing.T) {
		t.Parallel()
		code, msg, ok := PlatformMapper.Map(circuitbreaking.ErrCircuitBroken)
		test.True(t, ok)
		test.EqOp(t, ErrCircuitBroken, code)
		test.EqOp(t, "service temporarily unavailable", msg)
	})

	T.Run("ErrResourceInUse maps to ErrResourceConflict", func(t *testing.T) {
		t.Parallel()
		code, msg, ok := PlatformMapper.Map(platformerrors.Wrap(platformerrors.ErrResourceInUse, "deleting provider"))
		test.True(t, ok)
		test.EqOp(t, ErrResourceConflict, code)
		test.EqOp(t, "resource is in use", msg)
		test.EqOp(t, http.StatusConflict, HTTPStatusForCode(code))
	})

	// Every unusable session is a 401 and a message that says nothing about
	// which kind: telling a client apart "no such session" from "that one
	// expired" is an oracle for whether a guessed identifier ever existed.
	T.Run("session errors map to ErrFetchingSessionContextData", func(t *testing.T) {
		t.Parallel()
		for _, err := range []error{
			sessions.ErrNotFound,
			sessions.ErrExpired,
			sessions.ErrIdleTimeout,
			sessions.ErrAbsoluteTimeout,
			platformerrors.Wrap(sessions.ErrIdleTimeout, "reading session"),
		} {
			code, _, ok := PlatformMapper.Map(err)
			test.True(t, ok)
			test.EqOp(t, ErrFetchingSessionContextData, code)
			test.EqOp(t, http.StatusUnauthorized, HTTPStatusForCode(code))
		}
	})

	// ErrExpired wraps ErrNotFound, so ordering decides which message wins —
	// and the more specific one has to.
	T.Run("an expired session is reported as expired", func(t *testing.T) {
		t.Parallel()
		_, msg, ok := PlatformMapper.Map(sessions.ErrIdleTimeout)
		test.True(t, ok)
		test.EqOp(t, "session expired", msg)

		_, msg, ok = PlatformMapper.Map(sessions.ErrNotFound)
		test.True(t, ok)
		test.EqOp(t, "no active session", msg)
	})

	T.Run("every unusable action link maps to one code and a 410", func(t *testing.T) {
		t.Parallel()
		for err, expected := range map[error]string{
			links.ErrLinkAlreadyRedeemed: "this link has already been used",
			links.ErrLinkExpired:         "this link has expired",
			links.ErrLinkRevoked:         "this link is no longer valid",
			links.ErrLinkNotFound:        "this link is not valid",
		} {
			code, msg, ok := PlatformMapper.Map(err)
			test.True(t, ok)
			test.EqOp(t, ErrActionLinkUnusable, code)
			test.EqOp(t, http.StatusGone, HTTPStatusForCode(code))
			// One code, four messages: the distinction that matters is the one
			// a person reads, not one a client branches on.
			test.EqOp(t, expected, msg)
		}
	})

	// A malformed token never named a link, so it is bad input rather than a
	// link outcome, and gets a 400 instead of the 410 above.
	T.Run("ErrInvalidToken maps to ErrValidatingRequestInput", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(links.ErrInvalidToken)
		test.True(t, ok)
		test.EqOp(t, ErrValidatingRequestInput, code)
		test.EqOp(t, http.StatusBadRequest, HTTPStatusForCode(code))
	})

	// The Minter wraps its sentinels on the way out, so the mapper has to match
	// through the wrapping rather than on identity.
	T.Run("maps a wrapped links sentinel", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(platformerrors.Wrap(links.ErrLinkExpired, "redeeming action link"))
		test.True(t, ok)
		test.EqOp(t, ErrActionLinkUnusable, code)
	})

	T.Run("ErrInFlight maps to ErrIdempotencyKeyInFlight", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(idempotency.ErrInFlight)
		test.True(t, ok)
		test.EqOp(t, ErrIdempotencyKeyInFlight, code)
		test.EqOp(t, http.StatusConflict, HTTPStatusForCode(code))
	})

	T.Run("ErrFingerprintMismatch maps to ErrIdempotencyKeyReused", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(idempotency.ErrFingerprintMismatch)
		test.True(t, ok)
		test.EqOp(t, ErrIdempotencyKeyReused, code)
		test.EqOp(t, http.StatusUnprocessableEntity, HTTPStatusForCode(code))
	})

	// A malformed key is bad input, not an idempotency outcome.
	T.Run("key validation errors map to ErrValidatingRequestInput", func(t *testing.T) {
		t.Parallel()
		for _, err := range []error{
			idempotency.ErrKeyRequired,
			idempotency.ErrKeyTooLong,
			idempotency.ErrKeyInvalid,
		} {
			code, _, ok := PlatformMapper.Map(err)
			test.True(t, ok)
			test.EqOp(t, ErrValidatingRequestInput, code)
			test.EqOp(t, http.StatusBadRequest, HTTPStatusForCode(code))
		}
	})

	// The manager wraps its sentinels on the way out, so the mapper has to
	// match through the wrapping rather than on identity.
	T.Run("maps a wrapped idempotency sentinel", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(platformerrors.Wrap(idempotency.ErrInFlight, "checking idempotency claim"))
		test.True(t, ok)
		test.EqOp(t, ErrIdempotencyKeyInFlight, code)
	})

	T.Run("ErrNilInputParameter maps to ErrValidatingRequestInput", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(platformerrors.ErrNilInputParameter)
		test.True(t, ok)
		test.EqOp(t, ErrValidatingRequestInput, code)
	})

	T.Run("ErrEmptyInputParameter maps to ErrValidatingRequestInput", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(platformerrors.ErrEmptyInputParameter)
		test.True(t, ok)
		test.EqOp(t, ErrValidatingRequestInput, code)
	})

	T.Run("ErrInvalidIDProvided maps to ErrValidatingRequestInput", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(platformerrors.ErrInvalidIDProvided)
		test.True(t, ok)
		test.EqOp(t, ErrValidatingRequestInput, code)
	})

	T.Run("ErrEmptyInputProvided maps to ErrValidatingRequestInput", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(platformerrors.ErrEmptyInputProvided)
		test.True(t, ok)
		test.EqOp(t, ErrValidatingRequestInput, code)
	})

	T.Run("unknown error returns ok=false", func(t *testing.T) {
		t.Parallel()
		_, _, ok := PlatformMapper.Map(errors.New("nope"))
		test.False(t, ok)
	})
}

func TestToAPIError(T *testing.T) {
	T.Parallel()

	T.Run("nil error", func(t *testing.T) {
		t.Parallel()
		code, msg := ToAPIError(nil)
		test.EqOp(t, ErrNothingSpecific, code)
		test.EqOp(t, "", msg)
	})

	T.Run("known platform error uses PlatformMapper", func(t *testing.T) {
		t.Parallel()
		code, msg := ToAPIError(sql.ErrNoRows)
		test.EqOp(t, ErrDataNotFound, code)
		test.EqOp(t, "data not found", msg)
	})

	T.Run("unknown error returns fallback", func(t *testing.T) {
		t.Parallel()
		code, msg := ToAPIError(errors.New("totally unknown error that no mapper handles"))
		test.EqOp(t, ErrNothingSpecific, code)
		test.EqOp(t, "an error occurred", msg)
	})

	T.Run("circuit broken error", func(t *testing.T) {
		t.Parallel()
		code, msg := ToAPIError(circuitbreaking.ErrCircuitBroken)
		test.EqOp(t, ErrCircuitBroken, code)
		test.EqOp(t, "service temporarily unavailable", msg)
	})

	T.Run("ErrNilInputParameter", func(t *testing.T) {
		t.Parallel()
		code, msg := ToAPIError(platformerrors.ErrNilInputParameter)
		test.EqOp(t, ErrValidatingRequestInput, code)
		test.EqOp(t, "invalid input", msg)
	})

	T.Run("ErrUserAlreadyExists", func(t *testing.T) {
		t.Parallel()
		code, msg := ToAPIError(database.ErrUserAlreadyExists)
		test.EqOp(t, ErrValidatingRequestInput, code)
		test.EqOp(t, "user already exists", msg)
	})
}

type testHTTPMapper struct {
	err  error
	code ErrorCode
	msg  string
}

func (m testHTTPMapper) Map(err error) (ErrorCode, string, bool) {
	if errors.Is(err, m.err) {
		return m.code, m.msg, true
	}
	return "", "", false
}

func TestRegisterHTTPErrorMapper(T *testing.T) {
	T.Parallel()

	T.Run("registers a mapper that is consulted by ToAPIError", func(t *testing.T) {
		t.Parallel()

		customErr := errors.New("http-register-test-error")
		mapper := testHTTPMapper{err: customErr, code: "E_CUSTOM", msg: "custom message"}

		RegisterHTTPErrorMapper(mapper)

		code, msg := ToAPIError(customErr)
		test.EqOp(t, ErrorCode("E_CUSTOM"), code)
		test.EqOp(t, "custom message", msg)
	})
}
