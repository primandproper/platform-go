package workqueue

import (
	"github.com/primandproper/platform-go/v13/batching"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrNilDatabaseClient indicates a nil database.Client was passed to New. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrNilConfig indicates a nil Config was passed to New. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil work queue config")

	// ErrEmptyQueueName indicates a Config with no Name. There is no default:
	// one table holds every logical queue, and an unnamed queue would silently
	// share rows with every other unnamed queue in the database.
	ErrEmptyQueueName = platformerrors.New("empty work queue name")

	// ErrInvalidLease indicates a non-positive lease was supplied to Claim. A
	// zero lease would be handed out already expired, so every concurrent
	// claimer would take the same item.
	ErrInvalidLease = platformerrors.New("invalid work queue lease")

	// ErrInvalidPollInterval indicates a non-positive poll was supplied to Wait.
	// The poll is the backstop that makes a lost wakeup survivable, so a loop
	// without one would stop forever the first time a notification went
	// missing — which is a normal event, not an exceptional one.
	ErrInvalidPollInterval = platformerrors.New("invalid work queue poll interval")

	// ErrKeyTooLong indicates a key whose encoded form exceeds MaxKeyLength. It
	// is reported rather than truncated: two keys that differ only past the
	// limit would become one row, and the second unit of work would silently
	// disappear into the first.
	ErrKeyTooLong = platformerrors.New("encoded work queue key is too long")

	// ErrEmptyKey indicates a key whose encoded form is empty. An empty primary
	// key is legal SQL and always a mistake — it is what a zero-valued key
	// encodes to, so admitting it would let every unset key collapse onto one
	// row.
	ErrEmptyKey = platformerrors.New("empty work queue key")

	// ErrKeyContainsControlCharacter indicates a key whose encoded form contains
	// a NUL, a newline, or a carriage return. Postgres accepts all three in a
	// primary key, and every one of them is a key built by concatenating
	// unvalidated input — which makes every log line and every psql session that
	// touches the row unreadable.
	//
	// It is separate from ErrEmptyKey rather than a shade of it. A caller that
	// branches on "the key came out empty" and gets this instead reaches for the
	// wrong fix: a key with a newline in it is not missing, it is malformed, and
	// nothing about defaulting an unset key addresses it.
	ErrKeyContainsControlCharacter = platformerrors.New("work queue key contains a control character")

	// ErrKeyCodecTypeMismatch indicates WithKeyCodec was given a codec for a
	// type other than the Queue's. Option carries no type parameter, so the
	// compiler cannot catch this; New reports it instead, at construction.
	ErrKeyCodecTypeMismatch = platformerrors.New("key codec type does not match queue key type")

	// ErrClosed indicates an Enqueue that arrived after Close. It is returned
	// rather than parking the caller on a batch nothing will ever flush.
	//
	// It wraps batching.ErrClosed, which is where the refusal actually
	// originates, so a caller may check either — but the queue is what the
	// caller closed, and the queue is what the error should name.
	ErrClosed = platformerrors.Wrap(batching.ErrClosed, "work queue is closed")
)
