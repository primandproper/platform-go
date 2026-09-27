package series

import (
	"errors"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"google.golang.org/grpc/codes"
)

// The transport mappings for this package's sentinels. errors/http and
// errors/grpc are primitives and cannot import the tier above them, so the
// decision about what a series refusal means on the wire lives beside the
// refusal. Nothing registers these on its own: the composition root does, in
// one call — errormappers.Register.
//
// The endpoints these are for are the consumer's own: the schedule page that
// creates a standing slot, the week view, and the buttons that skip, move,
// close, end and make up. This package ships no transport of its own.
//
// The nil arguments, ErrValueTooLong, ErrNoInstant and ErrInvalidPageSize are
// absent from both switches on purpose: they wrap a platform sentinel the
// platform mappers already answer. internal/sentinelmatrix records which
// sentinel is in which state.
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
	// Absent and somebody else's tenant read the same, so the answer is not an
	// oracle for which series exist elsewhere.
	case errors.Is(err, ErrSeriesNotFound):
		return httperrors.ErrDataNotFound, "no such series", true
	case errors.Is(err, ErrOccurrenceNotFound):
		return httperrors.ErrDataNotFound, "no such occurrence", true

	// The state refusals. Each says what the person asked for that the
	// occurrence or the series is no longer in a state to do, and so what to
	// do instead.
	case errors.Is(err, ErrSeriesEnded):
		return httperrors.ErrResourceConflict, "the series already ends on or before that date", true
	case errors.Is(err, ErrOccurrenceSkipped):
		return httperrors.ErrResourceConflict, "that occurrence is skipped; add a replacement instead", true
	case errors.Is(err, ErrOccurrenceNotSkipped):
		return httperrors.ErrResourceConflict, "that occurrence still happens; move it instead", true
	case errors.Is(err, ErrOccurrenceReplaced):
		return httperrors.ErrResourceConflict, "that occurrence already has a replacement; move the replacement instead", true

	case errors.Is(err, ErrInvalidRule):
		return httperrors.ErrValidatingRequestInput, "the schedule is not a weekly recurrence", true
	case errors.Is(err, ErrUnknownTimeZone):
		return httperrors.ErrValidatingRequestInput, "the schedule's time zone is not recognized", true
	case errors.Is(err, ErrInvalidWindow):
		return httperrors.ErrValidatingRequestInput, "the date range must end after it starts", true
	case errors.Is(err, ErrWindowTooLarge):
		return httperrors.ErrValidatingRequestInput, "the date range holds too many occurrences; ask for a shorter one", true
	case errors.Is(err, ErrTooFarAhead):
		return httperrors.ErrValidatingRequestInput, "that date is too far ahead to schedule yet; ask for a nearer one", true
	case errors.Is(err, ErrTooFarBack):
		return httperrors.ErrValidatingRequestInput, "that date is too far in the past; ask for a later one", true
	default:
		return httperrors.ErrNothingSpecific, "", false
	}
}

func (grpcMapper) Map(err error) (code codes.Code, ok bool) {
	if err == nil {
		return codes.OK, false
	}

	switch {
	case errors.Is(err, ErrSeriesNotFound),
		errors.Is(err, ErrOccurrenceNotFound):
		return codes.NotFound, true

	// FailedPrecondition: the row is not in a state the call can succeed in,
	// and retrying will not put it in one.
	case errors.Is(err, ErrSeriesEnded),
		errors.Is(err, ErrOccurrenceSkipped),
		errors.Is(err, ErrOccurrenceNotSkipped),
		errors.Is(err, ErrOccurrenceReplaced):
		return codes.FailedPrecondition, true

	case errors.Is(err, ErrInvalidRule),
		errors.Is(err, ErrUnknownTimeZone),
		errors.Is(err, ErrInvalidWindow),
		errors.Is(err, ErrWindowTooLarge),
		errors.Is(err, ErrTooFarAhead),
		errors.Is(err, ErrTooFarBack):
		return codes.InvalidArgument, true
	default:
		return codes.Unknown, false
	}
}
