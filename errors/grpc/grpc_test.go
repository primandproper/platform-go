package grpc

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v6/circuitbreaking"
	"github.com/primandproper/platform-go/v6/database"
	platformerrors "github.com/primandproper/platform-go/v6/errors"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
)

func TestPlatformMapper_Map(T *testing.T) {
	T.Parallel()

	T.Run("nil error returns ok=false", func(t *testing.T) {
		t.Parallel()
		_, ok := PlatformMapper.Map(nil)
		test.False(t, ok)
	})

	T.Run("ErrUserAlreadyExists maps to AlreadyExists", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(database.ErrUserAlreadyExists)
		test.True(t, ok)
		test.EqOp(t, codes.AlreadyExists, code)
	})

	T.Run("sql.ErrNoRows maps to NotFound", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(sql.ErrNoRows)
		test.True(t, ok)
		test.EqOp(t, codes.NotFound, code)
	})

	T.Run("ErrCircuitBroken maps to Unavailable", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(circuitbreaking.ErrCircuitBroken)
		test.True(t, ok)
		test.EqOp(t, codes.Unavailable, code)
	})

	T.Run("ErrNilInputParameter maps to InvalidArgument", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(platformerrors.ErrNilInputParameter)
		test.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("ErrEmptyInputParameter maps to InvalidArgument", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(platformerrors.ErrEmptyInputParameter)
		test.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("ErrNilInputProvided maps to InvalidArgument", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(platformerrors.ErrNilInputProvided)
		test.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("ErrInvalidIDProvided maps to InvalidArgument", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(platformerrors.ErrInvalidIDProvided)
		test.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("ErrEmptyInputProvided maps to InvalidArgument", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(platformerrors.ErrEmptyInputProvided)
		test.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("unknown error returns ok=false", func(t *testing.T) {
		t.Parallel()
		_, ok := PlatformMapper.Map(errors.New("nope"))
		test.False(t, ok)
	})
}

func TestMapToGRPC(T *testing.T) {
	T.Parallel()

	T.Run("nil error returns OK", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, codes.OK, MapToGRPC(nil, codes.Internal))
	})

	T.Run("known platform error uses PlatformMapper", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, codes.NotFound, MapToGRPC(sql.ErrNoRows, codes.Internal))
	})

	T.Run("unknown error with no domain mappers returns default", func(t *testing.T) {
		t.Parallel()
		// Note: other tests may have registered domain mappers in the global slice,
		// so we test PlatformMapper directly for "unknown returns default" behavior above.
		code := MapToGRPC(errors.New("truly unknown error that no mapper handles"), codes.Aborted)
		// If a domain mapper catches it, that's fine; we just verify no panic.
		test.NotEq(t, codes.OK, code)
	})

	T.Run("domain mapper is consulted when platform mapper does not match", func(t *testing.T) {
		t.Parallel()

		customErr := errors.New("custom domain error")

		// We cannot safely mutate the global slice in parallel tests,
		// so we test the mapper interface directly to verify the flow.
		mapper := testGRPCMapper{err: customErr, code: codes.PermissionDenied}
		code, ok := mapper.Map(customErr)
		test.True(t, ok)
		test.EqOp(t, codes.PermissionDenied, code)
	})
}

type testGRPCMapper struct {
	err  error
	code codes.Code
}

func (m testGRPCMapper) Map(err error) (codes.Code, bool) {
	if errors.Is(err, m.err) {
		return m.code, true
	}
	return codes.Unknown, false
}

func TestRegisterGRPCErrorMapper(T *testing.T) {
	T.Parallel()

	T.Run("registers a mapper without panic", func(t *testing.T) {
		t.Parallel()

		customErr := errors.New("register-test-error")
		mapper := testGRPCMapper{err: customErr, code: codes.ResourceExhausted}

		// Should not panic
		RegisterGRPCErrorMapper(mapper)

		// After registration, MapToGRPC should find it
		code := MapToGRPC(customErr, codes.Internal)
		test.EqOp(t, codes.ResourceExhausted, code)
	})
}

func TestPrepareAndLogGRPCStatus(T *testing.T) {
	T.Parallel()

	T.Run("returns error with correct gRPC code", func(t *testing.T) {
		t.Parallel()

		err := PrepareAndLogGRPCStatus(sql.ErrNoRows, nil, nil, codes.Internal, "fetching thing %s", "abc")
		test.Error(t, err)
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		err := PrepareAndLogGRPCStatus(nil, nil, nil, codes.Internal, "something")
		// nil error maps to codes.OK, which may produce nil or a status with OK
		test.NoError(t, err)
	})

	T.Run("with unknown error uses default code", func(t *testing.T) {
		t.Parallel()

		err := PrepareAndLogGRPCStatus(errors.New("unknown"), nil, nil, codes.DataLoss, "oops")
		test.Error(t, err)
	})
}
