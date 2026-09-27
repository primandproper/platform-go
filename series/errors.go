package series

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// The sentinels this package returns. They live together because a caller
// deciding what to do next is choosing between them.
var (
	// ErrNilDatabaseClient indicates a nil database.Client handed to
	// NewSQLStore or NewWorker.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil series database client")

	// ErrNilExecutor indicates a nil executor. Every method on the Store runs
	// on one the caller supplies.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil series query executor")

	// ErrNilRule indicates a nil Rule handed to Store.CreateSeries.
	ErrNilRule = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil series rule")

	// ErrNilStore indicates a nil Store handed to NewWorker.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil series store")

	// ErrNilLocker indicates a nil locker handed to NewWorker. It is required
	// rather than defaulted: the unique index already makes two workers'
	// passes converge, and the lock is what stops every replica paying for the
	// same pass at once.
	ErrNilLocker = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil series horizon lock")

	// ErrNilConfig indicates a nil WorkerConfig handed to NewWorker.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil series worker config")

	// ErrValueTooLong indicates a value longer than the column that stores it
	// — see MaxReasonLength and MaxTimeZoneLength. It wraps
	// errors.ErrUnrecognizedInputValue, so the platform mapper answers it as a
	// bad request.
	ErrValueTooLong = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "series value is too long")

	// ErrInvalidRule indicates a rule that does not describe a recurrence: a
	// weekday or time of day that does not exist, an interval outside 1 to
	// MaxIntervalWeeks, a date that is not one, or an end on or before the
	// start. The wrapped message says which.
	ErrInvalidRule = platformerrors.New("series rule is not a weekly recurrence")

	// ErrUnknownTimeZone indicates a rule naming a zone the zone database does
	// not know — or naming none, which is refused rather than read as UTC.
	ErrUnknownTimeZone = platformerrors.New("series time zone is not an IANA zone")

	// ErrInvalidWindow indicates a window whose end is not after its start, or
	// that leaves either end unset.
	ErrInvalidWindow = platformerrors.New("series window must end after it starts")

	// ErrWindowTooLarge indicates a window holding more than
	// MaxOccurrencesPerRead occurrences. The answer is a narrower window, not
	// the first part of this one.
	ErrWindowTooLarge = platformerrors.New("series window holds too many occurrences to read at once")

	// ErrNoInstant indicates a move, a replacement or a materialization that
	// named the zero time as its instant. It wraps
	// errors.ErrUnrecognizedInputValue.
	ErrNoInstant = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "series instant is required")

	// ErrInvalidPageSize indicates a series listing asking for no rows, or for
	// more than MaxSeriesPerPage. It wraps errors.ErrUnrecognizedInputValue.
	ErrInvalidPageSize = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue, "series page size is out of range")

	// ErrSeriesNotFound indicates no live series where the call looked. A
	// series in another scope reads as absent, which is what it is from here.
	ErrSeriesNotFound = platformerrors.New("series not found")

	// ErrOccurrenceNotFound indicates no live occurrence where the call looked.
	ErrOccurrenceNotFound = platformerrors.New("series occurrence not found")

	// ErrSeriesEnded indicates an end on or after the date the series already
	// ends on. An end only ever brings a series' end nearer: pushing it later
	// would be un-skipping occurrences a consumer may already have cancelled
	// bookings for, and that is a new series, not an end.
	ErrSeriesEnded = platformerrors.New("series already ends on or before that date")

	// ErrOccurrenceSkipped indicates a skip or a move of an occurrence that is
	// already skipped. A skipped occurrence is brought back by adding a
	// replacement against it, which leaves a record of both.
	ErrOccurrenceSkipped = platformerrors.New("series occurrence is skipped")

	// ErrOccurrenceNotSkipped indicates a replacement added against an
	// occurrence that still happens. Moving it is the command for that.
	ErrOccurrenceNotSkipped = platformerrors.New("series occurrence is not skipped, so there is nothing to replace")

	// ErrOccurrenceReplaced indicates a replacement added against a skipped
	// occurrence that already has one. The first replacement is the one that
	// stands; a second make-up for one missed lesson is a move of the first.
	ErrOccurrenceReplaced = platformerrors.New("series occurrence already has a replacement")
)
