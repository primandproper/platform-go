package workqueue

import (
	"github.com/primandproper/primitives-go/v2/batching"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

var (
	// ErrNilDatabaseClient indicates a nil database.Client was passed to New. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil work queue database client")

	// ErrNilConfig indicates a nil Config was passed to New. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil work queue config")

	// ErrNilQueue indicates a nil *Queue was passed to NewRunner. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilQueue = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil work queue")

	// ErrNilHandler indicates a nil Handler was passed to NewRunner. A runner
	// with nothing to call would claim every item and complete it, which looks
	// exactly like a working deployment right up until somebody asks why none of
	// the work was done.
	ErrNilHandler = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil work queue handler")

	// ErrHandlerPanicked is what a Runner records for an item whose handler
	// panicked. The panic is contained and the item is held back like any other
	// failure — one poisonous key must not take the loop down with it — and the
	// panic value is wrapped into this so the row says what happened rather than
	// only that something did.
	ErrHandlerPanicked = platformerrors.New("work queue handler panicked")

	// ErrRunnerStopped is what a Runner records for an item it claimed and never
	// started, because a shutdown arrived first. Those items are handed straight
	// back rather than left to lapse, and this is the cause the row carries: the
	// claim already spent an attempt on them, so a reader chasing an attempt
	// count that rose without the work running needs to see why.
	ErrRunnerStopped = platformerrors.New("work queue runner stopped before the item was started")

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

	// ErrNotifyUnsupported indicates Config.NotifyChannel was set on a dialect
	// with no LISTEN/NOTIFY. It wraps dialect.ErrUnsupported, so a caller may
	// check either.
	//
	// Refused rather than ignored: a queue that dropped the channel would be a
	// deployment that believes an enqueue wakes its workers and is in fact
	// running on the poll interval, which looks like working until somebody
	// measures the latency.
	ErrNotifyUnsupported = platformerrors.Wrap(dialect.ErrUnsupported, "work queue notifications require postgres")

	// ErrClosed indicates an Enqueue that arrived after Close. It is returned
	// rather than parking the caller on a batch nothing will ever flush.
	//
	// It wraps batching.ErrClosed, which is where the refusal actually
	// originates, so a caller may check either — but the queue is what the
	// caller closed, and the queue is what the error should name.
	ErrClosed = platformerrors.Wrap(batching.ErrClosed, "work queue is closed")
)
