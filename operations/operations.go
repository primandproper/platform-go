package operations

import (
	"context"
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/filtering"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "operations"

// Observability keys for this package's spans and log fields. Declared once so
// a field set on a span and the same field logged beside it cannot drift, and
// so the operations. prefix is applied uniformly — an un-namespaced attribute
// name collides with every other component writing to the same trace.
//
// Nothing here carries a request or a result. Both are the application's own
// domain data — a subject ID, an export's contents — and a span exporter is a
// durable store the application never chose to put them in.
const (
	operationIDKey = "operations.id"
	kindKey        = "operations.kind"
	stateKey       = "operations.state"
	ownerKey       = "operations.owner"
	revisionKey    = "operations.revision"
	attemptsKey    = "operations.attempts"
	unitKey        = "operations.unit"
	unitsDoneKey   = "operations.units_done"
	countKey       = "operations.count"
	terminalKey    = "operations.terminal"
	cancelledKey   = "operations.cancel_requested"
	durationKey    = "operations.duration_ms"
	batchKey       = "operations.batch"
	recoveredKey   = "operations.recovered"

	// Store-layer keys. The database client traces the statement, but with the
	// SQL text suppressed by default — so without these a trace shows an
	// anonymous query span and no indication of which operation it was about.
	storeOpKey      = "operations.store_operation"
	rowsAffectedKey = "operations.rows_affected"
	guardMissedKey  = "operations.guard_missed"
	resultCountKey  = "operations.result_count"
	resultTotalKey  = "operations.result_total"
	limitKey        = "operations.limit"
	notifyKey       = "operations.notify_channel"
)

// State is where an operation has got to.
//
// There are four, and the set is closed. Google's long-running-operation pattern
// gets by with a single required signal — done — and everything past that is
// this package's own answer to "done how?". A fifth state would be another edge
// every client's switch has to handle, and clients are the thing this surface
// exists to serve.
type State string

const (
	// StatePending is an operation that has been recorded but not yet started.
	// It is the state Start leaves behind, and it is where an operation waits
	// for a worker.
	StatePending State = "pending"

	// StateRunning is an operation a worker has claimed and is executing. It is
	// the only state in which progress moves.
	StateRunning State = "running"

	// StateSucceeded is an operation whose Runner returned without error.
	// Terminal, and the only state in which Result means anything.
	StateSucceeded State = "succeeded"

	// StateFailed is an operation whose Runner exhausted its attempts or
	// returned an error it will not be retried past. Terminal, and the only
	// state in which Error means anything.
	StateFailed State = "failed"

	// StateCancelled is an operation somebody asked to stop, and which stopped.
	// Terminal.
	//
	// It is distinct from StateFailed on purpose. A cancelled operation did not
	// go wrong: somebody changed their mind, and a dashboard that counts it
	// beside genuine failures reports an error rate that is a measure of user
	// behavior.
	StateCancelled State = "cancelled"
)

// Terminal reports whether a state is one no worker will move an operation out
// of. It is the `done` of the Google LRO pattern, and the one signal a client
// may rely on.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	case StatePending, StateRunning:
		return false
	default:
		return false
	}
}

// Valid reports whether s is a state this package writes.
func (s State) Valid() bool {
	switch s {
	case StatePending, StateRunning, StateSucceeded, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// Attempt describes the execution a Runner is in: which operation it is
// running, which attempt this is, and whether it is the last one.
//
// It exists because the package's advice on duplicate execution — the operation
// ID is a stable idempotency key, and Attempts is above one on a retry — was
// advice a Runner had no way to act on. Everything a Runner was handed described
// the work; nothing described the attempt.
//
// Final is the part that cannot be derived. A Runner can count its own retries
// only by writing them down somewhere, and it cannot know the ceiling at all:
// the ceiling is WorkerConfig.MaxAttempts unless the kind overrode it, and
// neither is visible from inside Run. Without it, work that has to tell somebody
// it has given up — a subject with a statutory deadline is the case that
// prompted this — has no moment at which to say so.
type Attempt struct {
	// ID is the operation's ID, stable across every attempt. It is the
	// idempotency key a Runner would otherwise have to invent, and it is what an
	// operator needs in any line the Runner logs.
	ID string

	// Number is which attempt this is, counting from one. It is charged on
	// claim, so it is the number of attempts *made* including this one, rather
	// than the number that failed before it.
	Number int

	// Final reports that no further attempt will be made if this one fails.
	//
	// A Runner that has an obligation to report a permanent failure — rather
	// than only to return one — does it here. Nothing else in the package will:
	// the worker records the failure on the row and has no notion of who was
	// waiting for it.
	//
	// It says nothing about whether this attempt *will* fail, and a Runner that
	// treats it as a reason to try less hard has read it backwards.
	Final bool
}

// Progress is how far along an operation is, in two tiers, neither of which is
// required.
//
// The tiers exist because the two shapes of long-running work report differently
// and only one of them can offer a percentage. Work that fans out over a known
// set of units — dataprivacy's registered data domains, a reindex's shards — has
// a free denominator, and "3 of 9 domains complete" is the answer people want.
// Work inside a unit usually cannot say how much there is without doing a
// counting pass first, which is a second full scan to make a progress bar
// prettier. So within a unit the only claim made is a monotonic count.
type Progress struct {
	// UnitsTotal is the denominator, when there is one. Nil means the work never
	// declared how many units it would have, and no percentage can be computed —
	// which is a fact about the work, not a gap to be filled in with a guess.
	UnitsTotal *int `json:"unitsTotal,omitempty"`
	// Unit names the unit currently in progress: a data domain, a shard, a
	// table. Empty when the work has not declared units, or between them.
	Unit string `json:"unit,omitempty"`

	// Message is whatever the Runner last said, for a human reading a spinner.
	// It is never parsed and nothing branches on it.
	Message string `json:"message,omitempty"`

	// CountLabel is the noun Count counts, taken from the operation's Kind
	// registration: "rows", "records", "files". It lets a generic client render
	// "4,300 rows collected" without knowing what kind of operation it is
	// watching.
	CountLabel string `json:"countLabel,omitempty"`

	// Count is the within-unit tier: a monotonic count of whatever the Runner
	// is getting through, with no total.
	//
	// It does not reset when a unit finishes, which is the one place the reading
	// of "within a unit" is settled by decision rather than by the wording. It
	// is a spinner's number — evidence that something is still happening — and a
	// counter that restarted at zero every unit boundary would have a client
	// that was showing 4,300 suddenly show 12, which reads as a failure rather
	// than as progress. The per-unit structure is already carried by the tier
	// above.
	Count int64 `json:"count"`

	// UnitsDone counts the units finished so far.
	UnitsDone int `json:"unitsDone"`
}

// Fraction reports progress as a value in [0, 1] and whether one could be
// computed at all.
//
// ok is false when the operation declared no unit total. Callers that want a
// percentage must handle that case rather than dividing by zero into a bar that
// sits at 100% from the first tick, which is what every hand-rolled version of
// this does.
func (p Progress) Fraction() (fraction float64, ok bool) {
	if p.UnitsTotal == nil || *p.UnitsTotal <= 0 {
		return 0, false
	}

	if p.UnitsDone >= *p.UnitsTotal {
		return 1, true
	}

	if p.UnitsDone <= 0 {
		return 0, true
	}

	return float64(p.UnitsDone) / float64(*p.UnitsTotal), true
}

// Result is what a successful operation produced.
//
// It is a pointer and a note, never the payload. An export bundle is megabytes
// and belongs in uploads; a reindex's outcome is a count. Putting the artifact
// itself in this row would make every poll of a completed operation stream it
// again, and make the operations table the largest one in the database.
type Result struct {
	// URI addresses whatever the operation produced — most often an uploads key
	// or a signed URL. Empty when the operation produced no artifact.
	//
	// Nothing in this package fetches it, signs it, or checks that it resolves.
	// A URI whose signature expires is the producer's problem, and minting a
	// fresh one at read time is the consumer's endpoint to write.
	URI string `json:"uri,omitempty"`

	// Detail is a small, opaque, already-encoded summary the Runner chose to
	// record: row counts, a manifest, the names of sections that were skipped.
	// The library never looks inside it.
	//
	// It is bounded by MaxResultDetailBytes. A Result is read on every poll of a
	// finished operation, and an unbounded blob here would turn a status endpoint
	// into a download endpoint by accident.
	Detail json.RawMessage `json:"detail,omitempty"`
}

// Error is why a failed operation failed, in a shape a client can branch on.
//
// It is deliberately not the Go error. A rendered error string is an
// implementation detail that changes when somebody rewords a wrap, and every
// client that ever matched on one has been broken by a refactor. Code is the
// stable part, and it is the Runner's to choose.
type Error struct {
	// Code is a short, stable, machine-readable reason. It is whatever the
	// Runner set, or CodeInternal when a Runner failed without naming one.
	Code string `json:"code"`

	// Message is the human-readable rendering. It reaches API clients, so a
	// Runner must not put anything in it that the operation's owner should not
	// read.
	Message string `json:"message,omitempty"`

	// Retryable records whether the failure was one this package would have
	// tried again, had attempts remained. It is what distinguishes "the export
	// service was down for an hour" from "this subject does not exist", after
	// the fact, when only the row is left.
	Retryable bool `json:"retryable"`
}

// Operation is one long-running unit of work and everything known about where it
// got to. It is the whole of what this package promises: a row that a handler
// can point a client at, that survives the process that started it, and that
// always reaches a terminal state.
type Operation struct {

	// CreatedAt is when Start recorded the operation. It never moves.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt is when the row last changed, progress included.
	UpdatedAt time.Time `json:"updatedAt"`

	// StartedAt is when a worker first claimed the operation, and nil while it
	// is still pending. It is separate from CreatedAt because the gap between
	// them is queue latency, which is the number that explains a slow export
	// that ran quickly.
	StartedAt *time.Time `json:"startedAt,omitempty"`

	// FinishedAt is when the operation reached a terminal state, and nil until
	// it does.
	FinishedAt *time.Time `json:"finishedAt,omitempty"`

	// Result is what a succeeded operation produced. Nil in every other state.
	Result *Result `json:"result,omitempty"`

	// Error is why a failed operation failed. Nil in every other state.
	Error *Error `json:"error,omitempty"`

	// ID identifies the operation. It is what Start hands back and what every
	// read path takes.
	ID string `json:"id"`

	// Kind names the registered work this operation runs. It is the string a
	// Registry maps to a Runner, and it is stable across deploys by contract.
	Kind string `json:"kind"`

	// State is where the operation got to. Done is derived from it.
	State State `json:"state"`

	// Owner scopes the operation to whoever it belongs to — a user ID, an
	// account ID, a tenant. It is opaque: this package never parses it, and
	// compares it only for equality.
	//
	// It exists because the read paths are listable. An operations endpoint with
	// no notion of ownership serves every tenant's export status to whoever
	// asks, and that is a bug that gets discovered from the outside.
	Owner string `json:"owner,omitempty"`

	// Request is the input the Runner was started with, still encoded.
	//
	// It is excluded from the JSON rendering rather than merely omitted when
	// empty. The request is what the caller themselves sent a moment ago;
	// echoing it back on every poll of a status endpoint is bytes nobody needs,
	// and it is the field most likely to hold something — a subject's email, a
	// filter naming an account — that the operation's own status page has no
	// business repeating.
	Request json.RawMessage `json:"-"`

	// Progress is how far along the operation is. See Progress for why neither
	// of its tiers is required.
	Progress Progress `json:"progress"`

	// Revision is a monotonic counter incremented on every write to the row.
	//
	// It is what makes the watch path cheap and correct. A notification carries
	// no payload — see the package documentation — so a watcher re-reads the row
	// and needs to know whether what it read is new. Comparing revisions answers
	// that in one integer, where comparing the rest of the struct would have to
	// be right about every field anyone ever adds.
	Revision int64 `json:"revision"`

	// Attempts is how many times a worker has claimed this operation. It is
	// charged on claim rather than on failure, so an operation whose Runner
	// reliably kills its worker exhausts its budget and fails rather than being
	// reclaimed forever.
	Attempts int `json:"attempts"`

	// CancelRequested records that somebody called Cancel. It stays true after
	// the operation reaches StateCancelled, so "this was cancelled, not
	// abandoned" survives in the row.
	//
	// A running operation is not stopped by this flag. It is observed by the
	// Runner through Reporter.Cancelled and acted on by the Runner, because only
	// the Runner knows what an unfinished unit of its work has left behind.
	CancelRequested bool `json:"cancelRequested,omitempty"`

	// Done is the Google LRO signal, and the only field a client is obliged to
	// understand: false while the operation may still change, true once it will
	// not.
	//
	// It is derived from State rather than stored, and filled in on every read.
	// A stored copy would be a second source of truth for the one fact this
	// package exists to publish.
	Done bool `json:"done"`
}

// Terminal reports whether the operation has finished, in whichever way.
func (o *Operation) Terminal() bool {
	return o != nil && o.State.Terminal()
}

// ListScope narrows a listing. A nil *ListScope, or one with every field empty,
// lists everything — which is an operator's query, not an API handler's.
type ListScope struct {
	// Owner narrows to one owner. Empty means all of them.
	//
	// An HTTP surface must always set this. See Operation.Owner.
	Owner string `json:"owner,omitempty"`

	// Kind narrows to one kind of work. Empty means all of them.
	Kind string `json:"kind,omitempty"`

	// States narrows to a set of states. Empty means all of them, and
	// []State{StateFailed} is the query somebody runs at three in the morning.
	States []State `json:"states,omitempty"`
}

// Service is the application-facing seam: start an operation, ask after one,
// stop one.
//
// Running is deliberately not on this interface. A Start that ran the work
// inline would tie a long-running operation to the lifetime of the request that
// asked for it, and outliving that request is the entire guarantee on offer.
// Start writes a row and enqueues a key; a Worker runs it.
type Service interface {
	// Start records a new operation of the named kind, enqueues it, and returns
	// it in StatePending. The operation is durable before Start returns.
	//
	// request must be of the type the kind was registered with; it is encoded
	// once, here. It returns an error wrapping ErrUnknownKind for a kind this
	// process has not registered, and ErrRequestTypeMismatch for a request of
	// the wrong type — checked rather than assumed, because the registry erases
	// the type and the compiler therefore cannot.
	Start(ctx context.Context, kind string, request any, opts ...StartOption) (*Operation, error)

	// StartInTransaction is Start using the caller's executor, so the operation
	// row commits with the writes that decided to start it.
	//
	// It is the one worth reaching for. An operation recorded in its own
	// transaction after the caller's has committed does not exist if the process
	// dies in between — and whatever the caller wrote to justify starting it has
	// already happened.
	//
	// It does not enqueue, because an enqueue cannot join the caller's
	// transaction and one that landed first would offer a worker a row that does
	// not exist yet. Call Enqueue after the commit, or leave the operation to
	// the recovery sweep.
	StartInTransaction(
		ctx context.Context,
		q database.SQLQueryExecutor,
		kind string,
		request any,
		opts ...StartOption,
	) (*Operation, error)

	// Enqueue offers an already-recorded operation to the work queue.
	//
	// It is the companion to StartInTransaction: record the operation with the
	// writes that justify it, commit, then offer it. Calling it is optional —
	// the recovery sweep picks the operation up within Config.RecoverAfter
	// either way — and it is the difference between an operation that starts now
	// and one that starts in a minute.
	//
	// Give it a context that will outlive the transaction. The one scoped to a
	// WithTransaction closure is very often cancelled by the time the commit
	// returns, and an enqueue that fails for that reason puts every operation on
	// the sweep's slow path.
	Enqueue(ctx context.Context, id string, opts ...StartOption) error

	// Get reads one operation. It returns an error wrapping ErrOperationNotFound
	// when there is no such operation.
	Get(ctx context.Context, id string) (*Operation, error)

	// List pages through operations. Pass a scope with an Owner for anything
	// reachable from an API.
	List(
		ctx context.Context,
		scope *ListScope,
		filter *filtering.QueryFilter,
	) (*filtering.QueryFilteredResult[Operation], error)

	// Cancel asks an operation to stop and returns it as it stands.
	//
	// It is a request, not a kill. A pending operation is cancelled outright,
	// because nothing has started and there is nothing to unwind. A running one
	// has the flag set on its row, which its Runner observes through
	// Reporter.Cancelled; the operation reaches StateCancelled when that Runner
	// returns. A Runner that never consults Cancelled runs to completion, and
	// the operation succeeds — which is the honest outcome, since the work was
	// in fact done.
	//
	// Cancelling a terminal operation returns it unchanged rather than failing:
	// the caller wanted it not running, and it is not running.
	Cancel(ctx context.Context, id string) (*Operation, error)

	// Recover re-enqueues operations that are recorded but not queued, returning
	// how many it re-offered.
	//
	// It closes the gap between Start's two writes — see the package
	// documentation — and it belongs on the jobs scheduler rather than on a
	// ticker of its own, so the sweep runs once across a fleet. A deployment
	// that never runs it will strand an operation every time a process dies
	// between recording one and enqueueing it.
	Recover(ctx context.Context) (int, error)

	// Reap deletes terminal operations past Config.Retention, returning how many
	// rows went. Like Recover, it belongs on the jobs scheduler.
	//
	// It is bounded per call by Config.ReapBatchSize, so a long-neglected table
	// drains over several passes rather than in one statement holding locks
	// across the whole backlog.
	Reap(ctx context.Context) (int64, error)
}
