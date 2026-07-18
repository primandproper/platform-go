package grpc

import (
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v5/circuitbreaking"
	"github.com/primandproper/platform-go/v5/database"
	platformerrors "github.com/primandproper/platform-go/v5/errors"

	"google.golang.org/grpc/codes"
)

// PlatformMapper maps platform-level errors to gRPC codes.
// It does not depend on any domain.
var PlatformMapper GRPCErrorMapper = platformMapper{}

type platformMapper struct{}

func (platformMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}
	switch {
	case errors.Is(err, database.ErrUserAlreadyExists):
		return codes.AlreadyExists, true
	case errors.Is(err, sql.ErrNoRows):
		return codes.NotFound, true
	case errors.Is(err, circuitbreaking.ErrCircuitBroken):
		return codes.Unavailable, true
	case errors.Is(err, platformerrors.ErrNilInputParameter),
		errors.Is(err, platformerrors.ErrEmptyInputParameter),
		errors.Is(err, platformerrors.ErrNilInputProvided),
		errors.Is(err, platformerrors.ErrInvalidIDProvided),
		errors.Is(err, platformerrors.ErrEmptyInputProvided):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
