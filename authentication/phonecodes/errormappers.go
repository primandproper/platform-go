package phonecodes

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels. errors/http and
// errors/grpc are primitives and cannot import the tier above them, so the
// decision about what a phone code refusal means on the wire lives beside the
// refusal. Nothing registers these on its own: the composition root does, in
// one call — errormappers.Register.
//
// The endpoints these are for are the consumer's own: the one a person types a
// number into to be sent a code, and the one they type the code into. This
// package ships no transport of its own.
//
// The nil arguments, ErrValueTooLong, ErrInvalidMaxAttempts and
// ErrInvalidSetting are absent from both switches on purpose: they wrap a
// platform sentinel the platform mappers already answer. internal/sentinelmatrix
// records which sentinel is in which state.
var (
	// HTTPMapper maps this package's sentinels onto HTTP error codes.
	HTTPMapper httperrors.HTTPErrorMapper = httpMapper{}

	// GRPCMapper maps this package's sentinels onto gRPC codes, covering the
	// same sentinels HTTPMapper does.
	GRPCMapper grpcerrors.GRPCErrorMapper = grpcMapper{}
)

type (
	httpMapper struct{}
	grpcMapper struct{}
)

func (httpMapper) Map(err error) (code httperrors.ErrorCode, msg string, ok bool) {
	if err == nil {
		return httperrors.ErrNothingSpecific, "", false
	}

	switch {
	// Every refusal a redemption produces, collapsed, under the code a failed
	// sign-in gets for the same reason: telling the refusals apart is the
	// disclosure. Not not-found, because the number is not the resource — the
	// person proving they hold it is the request, and this is the answer that
	// they did not.
	case errors.Is(err, ErrCodeInvalid):
		return httperrors.ErrAuthenticationFailed, "that code is not valid; request a new one if it has expired", true

	case errors.Is(err, ErrInvalidPhoneNumber):
		return httperrors.ErrValidatingRequestInput, "a phone number must be in E.164 form, like +15555550100", true
	case errors.Is(err, ErrEmptySubjectID):
		return httperrors.ErrValidatingRequestInput, "a phone code needs a subject", true
	case errors.Is(err, ErrEmptyCode):
		return httperrors.ErrValidatingRequestInput, "enter the code you were sent", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrCodeInvalid):
		return codes.Unauthenticated, true

	case errors.Is(err, ErrInvalidPhoneNumber),
		errors.Is(err, ErrEmptySubjectID),
		errors.Is(err, ErrEmptyCode):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
