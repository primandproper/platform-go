package operations

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"sync"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// MaxKindLength bounds a kind name. It is the width of the column and the width
// of the index on it, and a name longer than this is a description.
const MaxKindLength = 128

// kindPattern is what a kind name may be: lowercase letters, digits, and the
// two separators, starting with a letter.
//
// It is restricted rather than escaped because a kind is durable. It is written
// into every row, it is the string a future build has to still register, and it
// is what a client switches on. "dataprivacy.export" and "search_reindex" are
// fine; a sentence with a subject's name in it is not, and would be in the
// table forever.
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9]*([._-][a-z0-9]+)*$`)

// Definition is one kind of long-running work: a name, a function, and how its
// progress should read.
//
// Req is the request type. It is bound exactly once, at Register, where the
// closure that decodes a stored request and calls Run is built; everything below
// that moves json.RawMessage. That is what lets one non-generic Worker run every
// kind in the process, and one DI container hold the Service they all share.
//
// The alternative — threading Req through Store, Service, and Worker — forces a
// worker pool per request struct.
type Definition[Req any] struct {
	// Run does the work.
	//
	// It receives the decoded request and a Reporter, and returns what the
	// operation produced. A nil *Result is a success that produced nothing,
	// which is the ordinary outcome for work whose whole point was the side
	// effect.
	//
	// It must be idempotent. A lease that lapses while its holder is merely slow
	// hands the same operation to somebody else, and both will run — see the
	// package documentation on the duplicate window. The request is the same on
	// every attempt and the operation ID is stable, so an idempotency key is
	// available without the Runner inventing one.
	//
	// Returning an error retries the operation until its attempts run out.
	// Wrapping it in Unretryable fails it now, which is the right answer for a
	// rejection that will not become an acceptance. Fail attaches the stable
	// code the failure is recorded and reported under.
	//
	// The context is the worker's, so it is cancelled at shutdown. Cancellation
	// requested by a caller arrives through Reporter.Cancelled instead, and is
	// deliberately not a context cancellation: abandoning the work halfway is
	// what a shutdown means, and stopping cleanly is what a cancellation means,
	// and a Runner that cannot tell them apart will do the wrong one.
	//
	// Required.
	Run func(ctx context.Context, req Req, rep Reporter) (*Result, error)

	// Kind names this work. It is written into every operation row, so it must
	// stay stable across deploys: a renamed kind strands every operation already
	// queued under the old name, which fails them with CodeUnknownKind.
	//
	// Lowercase, dot-, dash-, or underscore-separated, up to MaxKindLength.
	//
	// Required.
	Kind string

	// CountLabel is the noun Progress.Count counts — "rows", "records",
	// "messages". It is registered rather than reported because it is a property
	// of the kind of work rather than of a moment in it, and a client rendering
	// "4,300 rows collected" should not have to wait for the first progress
	// flush to learn the word.
	//
	// Empty renders the count without a noun, which is what a client that has
	// its own labeling wants.
	CountLabel string

	// MaxAttempts is how many times an operation of this kind may be claimed
	// before it is failed with CodeAttemptsExhausted. Zero means
	// WorkerConfig.MaxAttempts.
	//
	// It is per-kind because attempt budgets are per-kind in practice: a reindex
	// that takes an hour and a webhook replay that takes a second do not want
	// the same number, and the difference belongs next to the work rather than
	// in the one config every kind shares.
	MaxAttempts int
}

// runner is a Definition with its request type erased.
type runner struct {
	// run decodes an encoded request and executes the definition against it.
	run func(ctx context.Context, request json.RawMessage, rep Reporter) (*Result, error)

	// encode renders a caller's request value, and reports a value of the wrong
	// type rather than encoding it into something that will decode to a zero.
	encode func(ctx context.Context, request any) (json.RawMessage, error)

	countLabel  string
	maxAttempts int
}

// Registry holds the kinds a process can run.
//
// Kinds are code, not data: a Run is a Go function, so there is no useful way to
// express one in configuration and no way to load one at runtime. The registry
// exists so that the non-generic Worker can look a Runner up by the name an
// operation row recorded.
//
// It is safe for concurrent use, though the ordinary shape is to register
// everything at wiring time and only read afterwards.
type Registry struct {
	runners map[string]*runner
	mu      sync.RWMutex
}

// NewRegistry builds an empty Registry.
func NewRegistry() *Registry {
	return &Registry{runners: map[string]*runner{}}
}

// Register adds a kind of work under its own name.
//
// It is a free function rather than a method because a method cannot introduce
// its own type parameter, and Req has to be bound here — this is the one place
// in the package that knows what an operation's request actually is.
//
// Registering a kind twice is an error rather than an overwrite. A silent
// overwrite would swap the Runner under operations that are already queued, and
// the symptom — an export produced by the wrong code — arrives without anything
// to connect it to the second registration.
func Register[Req any](r *Registry, def Definition[Req]) error {
	if r == nil {
		return ErrNilRegistry
	}

	if err := validateDefinition(def.Kind, def.Run == nil); err != nil {
		return err
	}

	requestType := reflect.TypeFor[Req]()

	// One codec per registered kind, built here rather than per call, and shared
	// by both closures below so a request is decoded by exactly what encoded it.
	//
	// JSON, because the request is stored in a JSONB column and read by hand in
	// a psql session more often than anyone plans for. The values still move as
	// json.RawMessage for that reason: it is the column's type, not a choice
	// about how this package turns a value into bytes.
	codec := encoding.NewClientEncoder(encoding.ContentTypeJSON)

	bound := &runner{
		countLabel:  def.CountLabel,
		maxAttempts: max(def.MaxAttempts, 0),

		run: func(ctx context.Context, request json.RawMessage, rep Reporter) (*Result, error) {
			var req Req

			// An absent request decodes to the zero value rather than failing.
			// Plenty of work takes no parameters — "reindex everything" — and
			// requiring a caller to Start it with an empty struct encoded to
			// "{}" would be ceremony over a distinction nothing observes.
			if len(request) > 0 {
				if err := codec.Unmarshal(ctx, request, &req); err != nil {
					return nil, Unretryable(WithCode(CodeInternal,
						platformerrors.Wrapf(err, "decoding %q operation request", def.Kind)))
				}
			}

			return def.Run(ctx, req, rep)
		},

		encode: func(ctx context.Context, request any) (json.RawMessage, error) {
			if request == nil {
				return nil, nil
			}

			// Checked before encoding rather than after decoding. A request of
			// the wrong type frequently encodes to something the right type will
			// happily decode — every struct decodes from a JSON object, filling
			// in whichever fields happen to match — so the far end would receive
			// a plausible zero value instead of an error, hours later, in a
			// worker.
			if got := reflect.TypeOf(request); got != requestType {
				return nil, platformerrors.Wrapf(ErrRequestTypeMismatch,
					"kind %q takes %s, got %s", def.Kind, requestType, got)
			}

			encoded, err := codec.Marshal(ctx, request)
			if err != nil {
				return nil, platformerrors.Wrapf(err, "encoding %q operation request", def.Kind)
			}

			if len(encoded) > MaxRequestBytes {
				return nil, platformerrors.Wrapf(ErrRequestTooLarge,
					"%d bytes, limit %d", len(encoded), MaxRequestBytes)
			}

			return encoded, nil
		},
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.runners[def.Kind]; exists {
		return platformerrors.Wrapf(ErrDuplicateKind, "operation kind %q", def.Kind)
	}

	r.runners[def.Kind] = bound

	return nil
}

// MustRegister is Register for wiring code that has nowhere to return an error,
// and panics instead. A kind that will not register is a programming error
// caught at boot, not a condition to recover from.
func MustRegister[Req any](r *Registry, def Definition[Req]) {
	if err := Register(r, def); err != nil {
		panic(err)
	}
}

// Kinds reports the kinds this build registers, sorted.
//
// It is the readable half of the diagnosis when an operation fails with
// CodeUnknownKind: the row says what it wanted and this says what is on offer.
func (r *Registry) Kinds() []string {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	kinds := make([]string, 0, len(r.runners))
	for kind := range r.runners {
		kinds = append(kinds, kind)
	}

	slices.Sort(kinds)

	return kinds
}

// Has reports whether a kind is registered.
func (r *Registry) Has(kind string) bool {
	_, err := r.lookup(kind)

	return err == nil
}

// lookup resolves a kind, or reports ErrUnknownKind naming what is registered.
func (r *Registry) lookup(kind string) (*runner, error) {
	if r == nil {
		return nil, ErrNilRegistry
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	bound, ok := r.runners[kind]
	if !ok {
		return nil, platformerrors.Wrapf(ErrUnknownKind, "operation kind %q", kind)
	}

	return bound, nil
}

// validateDefinition vets the parts of a Definition that do not depend on Req,
// so Register and its tests share one answer to what a usable kind is.
func validateDefinition(kind string, missingRun bool) error {
	switch {
	case kind == "":
		return platformerrors.Wrap(ErrInvalidDefinition, "operation kind is required")
	case len(kind) > MaxKindLength:
		return platformerrors.Wrapf(ErrInvalidDefinition,
			"operation kind %q is %d bytes, limit %d", kind, len(kind), MaxKindLength)
	case !kindPattern.MatchString(kind):
		return platformerrors.Wrapf(ErrInvalidDefinition,
			"operation kind %q is not a legal name", kind)
	case missingRun:
		return platformerrors.Wrapf(ErrInvalidDefinition, "operation kind %q has no Run", kind)
	default:
		return nil
	}
}
