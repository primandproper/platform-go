package jobs

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Scheduler sentinels.
var (
	// ErrJobPanicked wraps the value recovered from a scheduled job that
	// panicked. The Scheduler contains the panic rather than letting it unwind
	// the job's goroutine, which would stop that job — and only that job —
	// silently for the life of the process.
	ErrJobPanicked = platformerrors.New("scheduled job panicked")
	// ErrNilLocker indicates a nil Locker was passed to NewScheduler. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilLocker = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil distributed locker")
	// ErrSchedulerRunning indicates Register was called after Run. The job set
	// is fixed at Run, because each job owns a goroutine started there.
	ErrSchedulerRunning = platformerrors.New("scheduler is already running")
	// ErrDuplicateJob indicates two jobs were registered under one name. Names
	// are the lock keys, so duplicates would contend with each other rather
	// than run independently.
	ErrDuplicateJob = platformerrors.New("duplicate job name")
	// ErrInvalidJob indicates a job with no name, no function, or no usable
	// cadence — neither a positive interval nor a schedule, both at once, or a
	// schedule that will never fire.
	ErrInvalidJob = platformerrors.New("invalid job")
)

// Pool sentinels.
var (
	// ErrHandlerPanicked wraps the value recovered from a handler that panicked.
	// The Pool contains the panic rather than letting it unwind the worker
	// goroutine and take the process with it, then treats it as an ordinary
	// attempt failure.
	ErrHandlerPanicked = platformerrors.New("job handler panicked")
	// ErrNilHandler indicates a nil Handler was passed to NewPool. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilHandler = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil job handler")
	// ErrNilConsumerProvider indicates a nil ConsumerProvider was passed to
	// NewPool. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilConsumerProvider = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil consumer provider")
	// ErrNilPublisherProvider indicates a nil PublisherProvider was passed to
	// NewTopicDeadLetter. It wraps errors.ErrNilInputParameter, so a caller may
	// check either.
	ErrNilPublisherProvider = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil publisher provider")
)

// PoolGroup sentinels.
var (
	// ErrNoPoolSpecs indicates NewPoolGroup was given no specs. An empty group
	// is a worker process that drains nothing while reporting a clean start,
	// which is the shape of a topic list that failed to load rather than of a
	// deliberate choice.
	ErrNoPoolSpecs = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no job pool specs")
	// ErrDuplicateTopic indicates two specs resolved to one topic. A
	// ConsumerProvider hands out one consumer per topic, so the second pool
	// could not have been built anyway — this reports it before the first is
	// consuming rather than partway through a start.
	ErrDuplicateTopic = platformerrors.New("duplicate job pool topic")
	// ErrPoolGroupStarted indicates Start was called on a group that has already
	// been started or closed. A group is single-use, because the Pools it owns
	// are: a Pool's stop channel is closed for good, so a restart would hand
	// back pools that decline every message they are given.
	ErrPoolGroupStarted = platformerrors.New("job pool group is already started")
)

// ErrInvalidCronSpec indicates a cron expression that could not be parsed. It
// joins the parser's own error, whose message names the offending field, so the
// sentinel is checkable and the detail is still readable.
var ErrInvalidCronSpec = platformerrors.New("invalid cron spec")
