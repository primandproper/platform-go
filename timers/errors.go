package timers

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrNilDatabaseClient indicates a nil database.Client was passed to New. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrNilConfig indicates a nil Config was passed to New. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil timers config")

	// ErrNilTimers indicates a nil *Timers was passed to NewWorker. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilTimers = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil timers")

	// ErrNilHandler indicates a nil Handler was passed to NewWorker. A worker
	// with nothing to call would claim every timer and mark it fired, which
	// looks exactly like a working deployment right up until somebody asks why
	// no reminders went out.
	ErrNilHandler = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil timer handler")

	// ErrEmptySetName indicates a Config with no Name. There is no default: one
	// table holds every logical timer set, and an unnamed set would silently
	// share rows with every other unnamed set in the database.
	ErrEmptySetName = platformerrors.New("empty timer set name")

	// ErrInvalidLease indicates a non-positive lease was supplied to Claim. A
	// zero lease would be handed out already expired, so every concurrent
	// claimer would fire the same timer.
	ErrInvalidLease = platformerrors.New("invalid timer lease")

	// ErrInvalidPollInterval indicates a non-positive poll was supplied to Wait.
	// The poll is the backstop that makes a lost wakeup survivable, so a loop
	// without one would stop forever the first time a notification went
	// missing — which is a normal event, not an exceptional one.
	ErrInvalidPollInterval = platformerrors.New("invalid timer poll interval")

	// ErrZeroRunAt indicates a Timer scheduled for the zero time. It is rejected
	// rather than read as "now", because the zero time is what an unset field
	// looks like: admitting it would turn a forgotten assignment into a timer
	// that fires immediately, which is the one outcome a scheduler must never
	// produce by accident. Schedule something for the past on purpose with an
	// explicit past instant, or use ScheduleIn with a zero delay.
	ErrZeroRunAt = platformerrors.New("timer has no run time")

	// ErrKeyTooLong indicates a key whose encoded form exceeds MaxKeyLength. It
	// is reported rather than truncated: two keys that differ only past the
	// limit would become one row, and the second timer would silently replace
	// the first.
	ErrKeyTooLong = platformerrors.New("encoded timer key is too long")

	// ErrEmptyKey indicates a key whose encoded form is empty. An empty primary
	// key is legal SQL and always a mistake — it is what a zero-valued key
	// encodes to, so admitting it would let every unset key collapse onto one
	// row.
	ErrEmptyKey = platformerrors.New("empty timer key")

	// ErrKeyContainsControlCharacter indicates a key whose encoded form contains
	// a NUL, a newline, or a carriage return. Postgres accepts all three in a
	// primary key, and every one of them is a key built by concatenating
	// unvalidated input — which makes every log line and every psql session that
	// touches the row unreadable.
	//
	// It is separate from ErrEmptyKey rather than a shade of it: a key with a
	// newline in it is not missing, it is malformed, and a caller told the former
	// reaches for a fix that does nothing about the latter.
	ErrKeyContainsControlCharacter = platformerrors.New("timer key contains a control character")

	// ErrPayloadTooLarge indicates a payload over MaxPayloadSize. The limit is
	// this package's rather than the column's: BYTEA would take a gigabyte
	// happily, and a timer table is read by a poller on an interval, so one
	// oversized row is paid for on every pass.
	ErrPayloadTooLarge = platformerrors.New("timer payload is too large")

	// ErrKeyCodecTypeMismatch indicates WithKeyCodec was given a codec for a
	// type other than the set's. Option carries no type parameter, so the
	// compiler cannot catch this; New reports it instead, at construction.
	ErrKeyCodecTypeMismatch = platformerrors.New("key codec type does not match timer key type")

	// ErrHandlerPanicked indicates a Handler panicked. The panic is contained
	// and converted rather than allowed to unwind the worker: one bad timer must
	// not take the loop down with it.
	ErrHandlerPanicked = platformerrors.New("timer handler panicked")
)
