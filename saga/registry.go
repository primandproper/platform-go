package saga

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/charset"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// definition is a Definition with its state type erased.
//
// This is the answer to the open question the ticket raised: a generic
// Runner[T] cannot be held by a non-generic worker pool or a DI container, and
// a generic Store would make the storage layer generic all the way down for the
// sake of a field the database sees as bytes either way. So T is erased exactly
// once, here, at registration — the closures below own the only Marshal and
// Unmarshal in the package, and everything from the Registry downward moves
// json.RawMessage.
//
// The alternative, threading T through Store and Worker, forces every consumer
// to run one worker pool per state type. That is not a saga engine; it is a
// saga engine per struct.
type definition struct {
	// do runs step i against encoded state and returns the encoded state the
	// step left behind.
	do func(ctx context.Context, i int, state json.RawMessage) (json.RawMessage, error)

	// undo compensates step i the same way. It is only called for steps whose
	// compensates entry is true.
	undo func(ctx context.Context, i int, state json.RawMessage) (json.RawMessage, error)

	// stateType is what T was, kept so a Runner[U] used against this definition
	// is reported rather than silently decoding into the wrong shape.
	stateType reflect.Type

	name        string
	stepNames   []string
	delays      []time.Duration
	compensates []bool
}

// steps reports how many steps the definition has.
func (d *definition) steps() int {
	return len(d.stepNames)
}

// Registry holds the definitions a process can run.
//
// Definitions are code, not data: a step is a Go function, so there is no
// useful way to express one in configuration and no way to load one at
// runtime. The registry exists so that the non-generic Worker can look a
// definition up by the name an instance recorded.
type Registry struct {
	definitions map[string]*definition
	mu          sync.RWMutex
}

// NewRegistry builds an empty Registry.
func NewRegistry() *Registry {
	return &Registry{definitions: map[string]*definition{}}
}

// Register adds a definition under its own name.
//
// It is a free function rather than a method because a method cannot introduce
// its own type parameter, and T has to be bound here — this is the one place in
// the package that knows what a saga's state actually is.
//
// The definition is validated before it is accepted: a saga with no steps, an
// unnamed step, two steps sharing a name, or a step with no Do is refused at
// wiring time rather than when an instance reaches it, because by then the
// earlier steps have already run and there is nothing to do but compensate them.
func Register[T any](r *Registry, def Definition[T]) error {
	if r == nil {
		return ErrNilRegistry
	}

	if err := validateDefinition(def); err != nil {
		return err
	}

	bound := &definition{
		name:        def.Name,
		stateType:   reflect.TypeFor[T](),
		stepNames:   make([]string, len(def.Steps)),
		delays:      make([]time.Duration, len(def.Steps)),
		compensates: make([]bool, len(def.Steps)),
	}

	// Copied rather than captured by reference: the slice belongs to the caller,
	// and a definition whose steps could be reordered after registration would
	// make the drift check a check on nothing.
	steps := slices.Clone(def.Steps)

	for i, step := range steps {
		bound.stepNames[i] = step.Name
		bound.delays[i] = step.Delay
		bound.compensates[i] = step.Undo != nil
	}

	bound.do = func(ctx context.Context, i int, state json.RawMessage) (json.RawMessage, error) {
		return applyStep(ctx, steps[i].Do, state)
	}

	bound.undo = func(ctx context.Context, i int, state json.RawMessage) (json.RawMessage, error) {
		return applyStep(ctx, steps[i].Undo, state)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.definitions[def.Name]; exists {
		return platformerrors.Wrapf(ErrDuplicateDefinition, "saga definition %q", def.Name)
	}

	r.definitions[def.Name] = bound

	return nil
}

// applyStep decodes the state, runs fn against it, and encodes what fn left
// behind.
//
// The state is decoded fresh for every step rather than carried as a live T.
// It costs a round trip through the codec per step and it buys the property
// that makes resumption correct rather than approximately correct: what a step
// sees is exactly what was persisted, so a step that runs after a crash sees
// what it would have seen if the crash had not happened. A cached in-memory T
// would diverge from the row the moment a write failed, and the divergence
// would only be visible on the path nobody tests.
func applyStep[T any](ctx context.Context, fn func(context.Context, *T) error, state json.RawMessage) (json.RawMessage, error) {
	var value T

	// An absent state decodes to the zero T. Start always writes an encoding,
	// so this is reachable only for a state column that was nulled out by hand,
	// and a zero value is a better answer than a decode error nobody can act on.
	if len(state) > 0 {
		if err := json.Unmarshal(state, &value); err != nil {
			return nil, platformerrors.Wrap(err, "decoding saga state")
		}
	}

	if err := fn(ctx, &value); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding saga state")
	}

	return encoded, nil
}

// validateDefinition reports whether a definition can be run at all.
func validateDefinition[T any](def Definition[T]) error {
	if def.Name == "" {
		return platformerrors.Wrap(ErrInvalidDefinition, "empty saga definition name")
	}

	if len(def.Steps) == 0 {
		return platformerrors.Wrapf(ErrInvalidDefinition, "saga definition %q has no steps", def.Name)
	}

	seen := make(map[string]struct{}, len(def.Steps))

	for i, step := range def.Steps {
		if step.Name == "" {
			return platformerrors.Wrapf(ErrInvalidDefinition, "saga definition %q step %d has no name", def.Name, i)
		}

		if !validStepName(step.Name) {
			return platformerrors.Wrapf(
				ErrInvalidDefinition,
				"saga definition %q step %q is not a valid step name", def.Name, step.Name,
			)
		}

		if step.Do == nil {
			return platformerrors.Wrapf(ErrInvalidDefinition, "saga definition %q step %q has no Do", def.Name, step.Name)
		}

		if step.Delay < 0 {
			return platformerrors.Wrapf(ErrInvalidDefinition, "saga definition %q step %q has a negative delay", def.Name, step.Name)
		}

		if _, duplicate := seen[step.Name]; duplicate {
			// Step names are idempotency keys and they are the drift check's
			// only handle on identity. Two steps sharing one would have the
			// second replay the first's recorded result, which is a
			// double-charge in the shape of a typo.
			return platformerrors.Wrapf(ErrInvalidDefinition, "saga definition %q repeats step name %q", def.Name, step.Name)
		}

		seen[step.Name] = struct{}{}
	}

	return nil
}

// maxStepNameLength bounds a step name. Idempotency keys are capped at 255
// bytes by default and one is built from a prefix, an instance ID, a phase, and
// this; 64 leaves the rest of that budget to the caller's prefix.
const maxStepNameLength = 64

// stepName is the rule for a step name usable as part of an idempotency key.
//
// The key is restricted rather than escaped, on the same terms as
// idempotency.ValidateKey: printable ASCII with no spaces. That admits every
// name anyone actually writes — charge_card, reserve-inventory, NotifyPartner —
// while excluding the control characters that would make one step's key parse
// as another's. ':' comes out on top of that because it is this package's own
// separator within the key.
//
// charset counts bytes, which is what this rule wants: the check is over the
// key's wire representation, not over the characters it spells.
var stepName = charset.New(
	charset.VisibleASCII.Without(charset.Bytes(':')),
	charset.WithMaxLength(maxStepNameLength),
)

// validStepName reports whether a step name is usable as part of an idempotency
// key.
func validStepName(name string) bool {
	return stepName.Valid(name)
}

// lookup returns the bound definition registered under name.
func (r *Registry) lookup(name string) (*definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	def, ok := r.definitions[name]

	return def, ok
}

// Names returns the registered definition names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.definitions))
	for name := range r.definitions {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// StepNames returns the step list registered under name, in order. The second
// result reports whether the definition is registered at all.
//
// It is exported because it is what an operator staring at a stuck instance
// needs: the instance carries the step list it started with, and the difference
// between that and this is the whole of ErrDefinitionDrift.
func (r *Registry) StepNames(name string) ([]string, bool) {
	def, ok := r.lookup(name)
	if !ok {
		return nil, false
	}

	return slices.Clone(def.stepNames), true
}
