package http

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v6/circuitbreaking"
	"github.com/primandproper/platform-go/v6/database"
	platformerrors "github.com/primandproper/platform-go/v6/errors"

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

	T.Run("ErrNilInputProvided maps to ErrValidatingRequestInput", func(t *testing.T) {
		t.Parallel()
		code, _, ok := PlatformMapper.Map(platformerrors.ErrNilInputProvided)
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
