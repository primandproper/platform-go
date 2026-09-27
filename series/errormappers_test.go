package series_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/series"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// The mappings, spelled once. internal/sentinelmatrix checks that every exported
// sentinel here is decided about; this file says what each was decided to be.
func TestMappers(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		err      error
		httpMsg  string
		httpCode httperrors.ErrorCode
		grpcCode codes.Code
	}{
		"no series": {
			err:      series.ErrSeriesNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such series",
			grpcCode: codes.NotFound,
		},
		"no occurrence": {
			err:      series.ErrOccurrenceNotFound,
			httpCode: httperrors.ErrDataNotFound,
			httpMsg:  "no such occurrence",
			grpcCode: codes.NotFound,
		},
		"an end that is not nearer": {
			err:      series.ErrSeriesEnded,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "the series already ends on or before that date",
			grpcCode: codes.FailedPrecondition,
		},
		"acting on a skipped occurrence": {
			err:      series.ErrOccurrenceSkipped,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "that occurrence is skipped; add a replacement instead",
			grpcCode: codes.FailedPrecondition,
		},
		"replacing one that still happens": {
			err:      series.ErrOccurrenceNotSkipped,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "that occurrence still happens; move it instead",
			grpcCode: codes.FailedPrecondition,
		},
		"a second replacement": {
			err:      series.ErrOccurrenceReplaced,
			httpCode: httperrors.ErrResourceConflict,
			httpMsg:  "that occurrence already has a replacement; move the replacement instead",
			grpcCode: codes.FailedPrecondition,
		},
		"a rule that is not one": {
			err:      series.ErrInvalidRule,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the schedule is not a weekly recurrence",
			grpcCode: codes.InvalidArgument,
		},
		"a zone that is not one": {
			err:      series.ErrUnknownTimeZone,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the schedule's time zone is not recognized",
			grpcCode: codes.InvalidArgument,
		},
		"a backwards window": {
			err:      series.ErrInvalidWindow,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the date range must end after it starts",
			grpcCode: codes.InvalidArgument,
		},
		"a window too full to read": {
			err:      series.ErrWindowTooLarge,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "the date range holds too many occurrences; ask for a shorter one",
			grpcCode: codes.InvalidArgument,
		},
		"a write too far ahead": {
			err:      series.ErrTooFarAhead,
			httpCode: httperrors.ErrValidatingRequestInput,
			httpMsg:  "that date is too far ahead to schedule yet; ask for a nearer one",
			grpcCode: codes.InvalidArgument,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			httpCode, httpMsg, ok := series.HTTPMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.httpCode, httpCode)
			test.EqOp(t, tc.httpMsg, httpMsg)

			grpcCode, ok := series.GRPCMapper.Map(tc.err)
			must.True(t, ok)
			test.EqOp(t, tc.grpcCode, grpcCode)

			wrapped := platformerrors.Wrap(tc.err, "skipping a lesson")

			_, _, ok = series.HTTPMapper.Map(wrapped)
			test.True(t, ok)

			_, ok = series.GRPCMapper.Map(wrapped)
			test.True(t, ok)
		})
	}
}

func TestMappers_LeaveTheRestAlone(T *testing.T) {
	T.Parallel()

	for _, err := range []error{nil, series.ErrValueTooLong, series.ErrNoInstant, series.ErrNilExecutor, platformerrors.New("something else")} {
		_, _, ok := series.HTTPMapper.Map(err)
		test.False(T, ok)

		_, ok = series.GRPCMapper.Map(err)
		test.False(T, ok)
	}
}
