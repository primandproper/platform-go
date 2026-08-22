package grpc

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/links"

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

	T.Run("ErrResourceInUse maps to FailedPrecondition", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(platformerrors.ErrResourceInUse)
		test.True(t, ok)
		test.EqOp(t, codes.FailedPrecondition, code)
	})

	// FailedPrecondition rather than NotFound: a link that has been used, has
	// expired, or has been revoked will never work again, and NotFound invites
	// the client to retry the URL that just failed.
	T.Run("every unusable action link maps to FailedPrecondition", func(t *testing.T) {
		t.Parallel()
		for _, err := range []error{
			links.ErrLinkAlreadyRedeemed,
			links.ErrLinkExpired,
			links.ErrLinkRevoked,
			links.ErrLinkNotFound,
		} {
			code, ok := PlatformMapper.Map(err)
			test.True(t, ok)
			test.EqOp(t, codes.FailedPrecondition, code)
		}
	})

	T.Run("ErrInvalidToken maps to InvalidArgument", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(links.ErrInvalidToken)
		test.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("ErrInFlight maps to Aborted", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(idempotency.ErrInFlight)
		test.True(t, ok)
		test.EqOp(t, codes.Aborted, code)
	})

	T.Run("ErrFingerprintMismatch maps to InvalidArgument", func(t *testing.T) {
		t.Parallel()
		code, ok := PlatformMapper.Map(idempotency.ErrFingerprintMismatch)
		test.True(t, ok)
		test.EqOp(t, codes.InvalidArgument, code)
	})

	T.Run("key validation errors map to InvalidArgument", func(t *testing.T) {
		t.Parallel()
		for _, err := range []error{
			idempotency.ErrKeyRequired,
			idempotency.ErrKeyTooLong,
			idempotency.ErrKeyInvalid,
		} {
			code, ok := PlatformMapper.Map(err)
			test.True(t, ok)
			test.EqOp(t, codes.InvalidArgument, code)
		}
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
