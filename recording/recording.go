package recording

import (
	"context"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/webhooks"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

const (
	// serviceName scopes this package's spans and logger.
	serviceName = "recording"

	scopeKey      = serviceName + ".scope"
	entryCountKey = serviceName + ".entries"
	eventTypeKey  = serviceName + ".event_type"
	actorKey      = serviceName + ".actor"
)

// The sentinels this package returns. Every one is a wiring failure rather
// than anything a caller's request said, and each wraps
// errors.ErrNilInputParameter so the platform mappers answer it.
var (
	// ErrNilAuditRecorder indicates a nil audit.Recorder.
	ErrNilAuditRecorder = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recording audit recorder")

	// ErrNilEmitter indicates a nil webhooks.Emitter. A deployment with no
	// webhook tables that still wants audit entries beside its writes has not
	// been met yet; when it is, it arrives as an option, not as a nil here.
	ErrNilEmitter = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recording event emitter")

	// ErrNilPrincipalExtractor indicates a nil callers.PrincipalExtractor. It is
	// required rather than defaulted because an extractor that answers "nobody"
	// for every call records every write as unattributed, which is a decision a
	// consumer should have to spell.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recording principal extractor")

	// ErrNilExecutor indicates a nil database.Tx. Both halves run on the
	// caller's transaction and there is none of this type's own to fall back to.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recording query executor")

	// ErrNilEntry indicates a nil *Entry among the entries to record.
	ErrNilEntry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil recording entry")

	// ErrNothingToRecord indicates a Record call with no entries and no event.
	// A hook that has decided to record nothing returns nil without calling
	// Record; a call that reaches here with nothing is a hook that forgot.
	ErrNothingToRecord = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "a recording names no entry and no event")
)

// Entry is one audit entry a write owes, before the Recorder has decided who
// made it and where it is filed.
//
// It is the caller-supplied half of an audit.Entry and nothing else: the actor
// comes off the context and the scope off the write, and a hook that could set
// either would be a hook that could misfile an entry under another tenant's
// chain. SubjectID is who the entry is about, as distinct from who acted, and
// is read by a ScopeResolver and by nothing else here.
type Entry struct {
	// Changes is the per-field before and after of an update, as audit.Diff
	// builds it, and nil for every other write.
	Changes map[string]audit.Change
	// Metadata is free-form context for the entry. It passes through the audit
	// recorder's Redaction on the same field names Changes does.
	Metadata map[string]string
	// ResourceType names what kind of thing the entry is about.
	ResourceType string
	// ResourceID names which one.
	ResourceID string
	// SubjectID names the person or account the entry concerns, where there is
	// one, for a ScopeResolver that files entries by subject. It is not written
	// to the entry; a hook that wants it recorded puts it in Metadata.
	SubjectID string
	// EventType is the audit vocabulary's word for what happened.
	EventType audit.EventType
}

// ScopeResolver decides which tenancy.Scope an entry is filed under, given the
// scope the write ran in and the entry about to be recorded.
//
// The default returns the write's scope. A deployment whose store is global
// but whose entries are about a subject returns tenancy.Of(entry.SubjectID)
// when the entry names one, so "what happened to this person" is a chain the
// log can walk rather than a search across the global one.
type ScopeResolver func(ctx context.Context, scope tenancy.Scope, entry *Entry) tenancy.Scope

// Recorder writes the audit entries and the one domain event a store write
// owes, on the write's own transaction.
//
// It holds no database handle. Every Record takes the caller's Tx, so one
// Recorder serves every transaction in the process, as audit.ChainRecorder and
// webhooks.Emitter each do.
type Recorder struct {
	entries    audit.Recorder
	events     *webhooks.Emitter
	principals callers.PrincipalExtractor
	scopeFor   ScopeResolver
	o11y       observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger         logging.Logger
	tracerProvider tracing.Provider
}

// Option configures a Recorder.
type Option func(*Recorder)

// WithScopeResolver replaces the default filing rule, which is the write's
// scope. A nil resolver keeps the default.
func WithScopeResolver(resolve ScopeResolver) Option {
	return func(r *Recorder) {
		if resolve != nil {
			r.scopeFor = resolve
		}
	}
}

// WithLogger sets the logger. Absent means none.
func WithLogger(logger logging.Logger) Option {
	return func(r *Recorder) {
		r.logger = logger
	}
}

// WithTracerProvider sets the tracer provider. Absent means none.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(r *Recorder) {
		r.tracerProvider = tracerProvider
	}
}

// New builds a Recorder over an audit recorder, an event emitter and the
// extractor that reads a write's principal off its context.
//
// All three are required. A nil in any slot refuses here rather than degrading
// to a recorder that does half the job, which is the split this type exists
// to close.
func New(
	entries audit.Recorder,
	events *webhooks.Emitter,
	principals callers.PrincipalExtractor,
	opts ...Option,
) (*Recorder, error) {
	if entries == nil {
		return nil, ErrNilAuditRecorder
	}

	if events == nil {
		return nil, ErrNilEmitter
	}

	if principals == nil {
		return nil, ErrNilPrincipalExtractor
	}

	r := &Recorder{
		entries:    entries,
		events:     events,
		principals: principals,
		scopeFor:   writeScope,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)

	return r, nil
}

// writeScope is the default ScopeResolver: an entry is filed where the write
// ran.
func writeScope(_ context.Context, scope tenancy.Scope, _ *Entry) tenancy.Scope {
	return scope
}

// Record writes every entry and then emits event, all on tx.
//
// Several entries and one event is the ordinary case rather than a special
// one: a registration writes a user, an account and a membership, and it is one
// thing that happened. The log holds a row per resource and the broker holds
// one event, because a subscriber told three times that somebody arrived would
// act three times.
//
// Either half may be absent, not both. An event with no entries is a write the
// deployment publishes and does not audit; entries with no event are a write it
// audits and keeps to itself. A call with neither is refused, because a hook
// that has decided to record nothing says so by not calling this.
//
// An error from either half fails the write the hook was called from, and the
// caller's transaction rolls back with it. The entries go first, so a refused
// entry never leaves an event in the outbox describing a row that is about to
// be unwound.
func (r *Recorder) Record(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	event *webhooks.Event,
	entries ...*Entry,
) error {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if tx == nil {
		return op.Error(ErrNilExecutor, "recording a write")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "recording a write")
	}

	if event == nil && len(entries) == 0 {
		return op.Error(ErrNothingToRecord, "recording a write")
	}

	op.Set(entryCountKey, len(entries))

	if event != nil {
		op.Set(eventTypeKey, event.EventType.String())
	}

	actor := r.actor(ctx)
	op.Set(actorKey, actor.ID)

	batches, order, err := r.file(ctx, scope, actor, entries)
	if err != nil {
		return op.Error(err, "recording a write")
	}

	for _, filed := range order {
		if err = r.entries.Record(ctx, tx, filed, batches[filed]...); err != nil {
			return op.Error(err, "recording audit entries under %s", filed)
		}
	}

	if event != nil {
		if err = r.events.Emit(ctx, tx, scope, event); err != nil {
			return op.Error(err, "emitting %q", event.EventType)
		}
	}

	return nil
}

// actor is who the context says is writing, or the named absence when it says
// nobody.
func (r *Recorder) actor(ctx context.Context) audit.Actor {
	if principal, ok := r.principals(ctx); ok && principal != nil {
		return audit.PrincipalActor(principal)
	}

	return audit.Actor{ID: audit.ActorUnattributed, Type: audit.ActorUnattributed}
}

// file resolves each entry's scope and groups the entries by it, keeping the
// scopes in the order they were first seen so a batch is recorded in the order
// the hook named its entries.
func (r *Recorder) file(
	ctx context.Context,
	scope tenancy.Scope,
	actor audit.Actor,
	entries []*Entry,
) (batches map[tenancy.Scope][]*audit.Entry, order []tenancy.Scope, err error) {
	batches = make(map[tenancy.Scope][]*audit.Entry, 1)

	for _, entry := range entries {
		if entry == nil {
			return nil, nil, ErrNilEntry
		}

		filed := r.scopeFor(ctx, scope, entry)
		if _, seen := batches[filed]; !seen {
			order = append(order, filed)
		}

		batches[filed] = append(batches[filed], &audit.Entry{
			Actor:        actor,
			Scope:        filed,
			ResourceType: entry.ResourceType,
			ResourceID:   entry.ResourceID,
			EventType:    entry.EventType,
			Changes:      entry.Changes,
			Metadata:     entry.Metadata,
		})
	}

	return batches, order, nil
}
