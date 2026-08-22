package operations

import (
	stderrors "errors"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Failure codes this package writes into Error.Code when a Runner did not name
// one of its own. A Runner's own codes are its to choose and are never
// rewritten; these fill in for the cases the library, not the work, decided.
const (
	// CodeInternal is the code a Runner's unclassified error is recorded under.
	CodeInternal = "internal"

	// CodePanic is the code a Runner that panicked is recorded under. It is
	// distinct from CodeInternal because the two want different responses: one
	// is a dependency having a bad day, the other is a nil map somebody needs to
	// go and fix.
	CodePanic = "panic"

	// CodeAttemptsExhausted is the code an operation that ran out of attempts is
	// recorded under. Error.Message carries the last failure's rendering, which
	// is the one anybody reading it actually wants.
	CodeAttemptsExhausted = "attempts_exhausted"

	// CodeUnknownKind is the code an operation whose kind no build registers is
	// recorded under.
	//
	// It is a failure rather than a retry. A kind vanishes from a build because
	// somebody deleted or renamed it, and retrying a name nothing will ever
	// answer to burns the operation's whole attempt budget arriving at the same
	// place a good deal later.
	CodeUnknownKind = "unknown_kind"

	// CodeCancelled is the code recorded on the rare failure that races a
	// cancellation: a Runner that returned an error after a cancellation was
	// requested is recorded as cancelled, and this is the code left behind for
	// the operator who wants to know it did not exit cleanly.
	CodeCancelled = "cancelled"
)

var (
	// ErrNilConfig indicates a nil *Config. It wraps errors.ErrNilInputParameter,
	// so a caller may check either.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations config")

	// ErrNilStore indicates a nil Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations store")

	// ErrNilDatabaseClient indicates a nil database.Client.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrNilExecutor indicates a Store method that runs in the caller's
	// transaction was called without one.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")

	// ErrNilRegistry indicates a nil *Registry.
	ErrNilRegistry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations registry")

	// ErrNilQueue indicates a Service or Worker built without a work queue.
	//
	// It has no default. A Service without a queue would record operations that
	// nothing ever runs, which looks exactly like a working Service until
	// somebody waits for a result.
	ErrNilQueue = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations work queue")

	// ErrNilOperation indicates a nil operation record.
	ErrNilOperation = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operation")

	// ErrNilService indicates a nil Service.
	ErrNilService = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations service")

	// ErrOperationNotFound indicates an operation ID that is not in the table, or
	// one that is not in the state the write required.
	ErrOperationNotFound = platformerrors.New("operation not found")

	// ErrDuplicateOperation indicates a Start whose WithID collided with an
	// operation that already exists.
	//
	// It is the successful outcome of the idempotency seam rather than a
	// failure: the caller asked for this work under this ID and it is already
	// recorded, so the right response is to hand back the operation that is
	// already running. Service.Start does exactly that, and this sentinel exists
	// for callers that want to tell the two apart.
	ErrDuplicateOperation = platformerrors.New("operation already exists")

	// ErrUnknownKind indicates a kind this process has not registered.
	ErrUnknownKind = platformerrors.New("unknown operation kind")

	// ErrDuplicateKind indicates two registrations under one name. A silent
	// overwrite would swap the Runner under operations that are already queued.
	ErrDuplicateKind = platformerrors.New("duplicate operation kind")

	// ErrInvalidDefinition indicates a Definition that cannot be run: no kind, a
	// kind that is not a legal name, or no Run.
	ErrInvalidDefinition = platformerrors.New("invalid operation definition")

	// ErrRequestTypeMismatch indicates a Start whose request is not the type its
	// kind was registered with. The registry erases that type, so the compiler
	// cannot catch this; it is reported at Start rather than at the far end,
	// where a Runner would receive a zero value that merely happened to decode.
	ErrRequestTypeMismatch = platformerrors.New("operation request type does not match the registered kind")

	// ErrRunnerPanicked indicates a Run that panicked. It is contained and
	// converted into that operation's failure: somebody else's code running in
	// our goroutine should cost its own operation, not every other one in the
	// batch.
	ErrRunnerPanicked = platformerrors.New("operation runner panicked")

	// ErrResultTooLarge indicates a Result.Detail past MaxResultDetailBytes. It
	// is refused rather than truncated, because a truncated encoding is not a
	// smaller document — it is an invalid one, and it would be discovered by
	// whatever tries to decode it days later.
	ErrResultTooLarge = platformerrors.New("operation result detail is too large")

	// ErrRequestTooLarge indicates a request encoding past MaxRequestBytes.
	ErrRequestTooLarge = platformerrors.New("operation request is too large")

	// ErrWatcherClosed indicates a Watch against a Watcher that has been closed.
	ErrWatcherClosed = platformerrors.New("operations watcher is closed")

	// ErrTooManyWatchers indicates a Watch that would exceed
	// WatcherConfig.MaxSubscriptions.
	//
	// It is refused rather than queued. Every subscription costs a row in the
	// watcher's re-read, so an unbounded subscriber count turns one wake into an
	// unbounded query, and a client that reconnects in a loop would take the
	// database with it.
	ErrTooManyWatchers = platformerrors.New("too many operation watchers")
)

// Unretryable marks an error as one this package must not try again.
//
// It is the Runner's way to say the work will not succeed on a second attempt:
// a request naming a subject that does not exist, an export of something that
// has been deleted. Without it, every failure consumes the operation's whole
// attempt budget before the client is told anything.
//
//	if !exists {
//	    return nil, operations.Unretryable(operations.Fail("no_such_subject", "no such subject"))
//	}
func Unretryable(err error) error {
	if err == nil {
		return nil
	}

	return &unretryableError{cause: err}
}

// IsUnretryable reports whether err was marked by Unretryable.
func IsUnretryable(err error) bool {
	if err == nil {
		return false
	}

	var target *unretryableError

	return stderrors.As(err, &target)
}

// unretryableError is the marker Unretryable applies. It is a wrapper type
// rather than a sentinel joined onto the error so that Unretryable(x) still
// satisfies errors.Is(_, x) for whatever x was — a Runner marking its own
// sentinel unretryable must not lose the sentinel in the process.
type unretryableError struct {
	cause error
}

func (e *unretryableError) Error() string { return e.cause.Error() }

func (e *unretryableError) Unwrap() error { return e.cause }

// codedError is an error carrying the stable Error.Code a Runner chose.
type codedError struct {
	cause error
	code  string
}

func (e *codedError) Error() string { return e.cause.Error() }

func (e *codedError) Unwrap() error { return e.cause }

// Fail builds an error carrying a stable code, which is what lands in
// Error.Code and what a client is expected to branch on.
//
// A Runner that fails without one is recorded under CodeInternal, which is
// honest but tells a client nothing it can act on.
func Fail(code, messageFmt string, messageArgs ...any) error {
	return &codedError{
		code:  code,
		cause: platformerrors.Errorf(messageFmt, messageArgs...),
	}
}

// WithCode attaches a stable code to an existing error, for a Runner that has a
// perfectly good error already and only wants to classify it.
func WithCode(code string, err error) error {
	if err == nil {
		return nil
	}

	return &codedError{code: code, cause: err}
}

// codeOf reports the code a Runner attached, or an empty string.
func codeOf(err error) string {
	if target, ok := stderrors.AsType[*codedError](err); ok {
		return target.code
	}

	return ""
}
