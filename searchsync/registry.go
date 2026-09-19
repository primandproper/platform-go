package searchsync

import (
	"context"
	"sync"

	"github.com/primandproper/primitives-go/v2/batching"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Source is the application's half of one index: the Fetcher the change feed
// reads a document back through and the Scanner a rebuild walks.
//
// The two are separate interfaces because a Syncer needs only the first and a
// Reindexer only the second, and this is the type that says they are ordinarily
// one thing — which they have to be, since both must produce the same document
// for the same row. searchsync/source builds one that cannot disagree with
// itself.
type Source[T any] interface {
	Fetcher[T]
	Scanner[T]
}

// IndexSpec is everything one index needs: where its documents come from, where
// they go, and what the pool draining its topic looks like.
//
// It is a struct rather than a parameter list because the fields it is
// genuinely optional about — a pruner, a stamp, a pool config, options for each
// of the three things built from it — outnumber the ones it is not, and a
// constructor taking eight arguments in a fixed order is one a caller gets
// wrong silently whenever two of them share a type.
type IndexSpec[T any] struct {
	// Source reads documents back, for one event and for a whole rebuild.
	// Required.
	Source Source[T]

	// Target is the index itself: TextTarget, VectorTarget, or two methods.
	// Required.
	Target Target[T]

	// Pruner lets a rebuild delete documents whose source rows are gone. Absent
	// means an upsert-only rebuild, which is the right mode for a bootstrap and
	// for a mapping change — see WithReindexPruner.
	Pruner Enumerator

	// Stamp records the documents the index accepted, so last_indexed_at on the
	// rows behind them stays current. It is the bulk write itself —
	// conventionally querygen's MarkXAsIndexed — and the buffering it has to
	// happen behind is built here and closed by Registry.Close. Absent means
	// this index's source table does not carry the column.
	Stamp func(ctx context.Context, ids []string) error

	// PoolConfig is the consuming pool's knobs: concurrency, retry, handler
	// timeout. Nil means the jobs package defaults. Its Topic is overridden by
	// Topic below, so one config may back every index in a service.
	PoolConfig *jobs.PoolConfig

	// Name identifies the index in every span and metric attribute here, and —
	// prefixed with ReindexJobPrefix — as the scheduler lock key its rebuild
	// runs under. Required, and unique within a Registry.
	Name string

	// Topic is the queue topic this index's events arrive on. It is the same
	// string the Rule deriving those events names, which is ordinarily a
	// constant the application declares once and uses in both processes.
	// Required, and unique within a Registry.
	Topic string

	// PoolOptions are applied to this index's pool alone, after whatever the
	// PoolGroup applies to all of them — a dead-letter destination, a retry
	// policy of its own.
	PoolOptions []jobs.PoolOption

	// SyncerOptions, ReindexOptions and StampOptions are passed through to the
	// three things this spec builds. They are applied after the Registry's own
	// pillars, so a spec may override one of them for a single index.
	SyncerOptions  []SyncerOption
	ReindexOptions []ReindexOption
	StampOptions   []batching.Option
}

// Registration is the triple one index is made of, handed back so a caller can
// reach past the Registry where it needs to — registering the rebuild with a
// jobs.Scheduler, or applying an event directly in a test.
//
// The stamp buffer is deliberately not among them. It owns a goroutine, the
// Registry closes it, and a second Close from here would be the kind of
// shutdown ownership that is split between two places and honored by neither.
type Registration[T any] struct {
	// Syncer applies one index event. Its Handle is the jobs.Handler the
	// registry's pool spec for this index carries.
	Syncer *Syncer[T]

	// Reindexer rebuilds the index. Registry.ReindexAll runs it; Reindexer.Job
	// schedules it.
	Reindexer *Reindexer[T]
}

// registered is one index, with its type parameter erased. A Registry holds
// every index a service runs and they do not share a document type, so what it
// keeps is the three type-free things it does something with: the handler a
// pool calls, the rebuild ReindexAll runs, and the buffer Close closes.
type registered struct {
	handler    jobs.Handler
	reindex    func(context.Context) (*ReindexResult, error)
	stamps     *batching.Buffer[string]
	poolConfig *jobs.PoolConfig

	name  string
	topic string

	poolOptions []jobs.PoolOption
}

// Registry is the list of indexes a service runs.
//
// Nine indexes wired by hand is nine pool stanzas that differ in two strings,
// nine stamp-buffer-syncer-reindexer triples, and a rebuild-everything that
// names all nine again — three lists of the same nine things, kept in step by
// whoever remembers. The tenth index is added to two of them and the symptom is
// that the rebuild quietly covers nine tenths of the data.
//
// So there is one list. RegisterIndex writes to it, PoolSpecs and ReindexAll
// read it, and adding an index touches neither of them.
//
//	registry := searchsync.NewRegistry(searchsync.WithRegistryPillars(pillars))
//	defer func() { _ = registry.Close(shutdownCtx) }()
//
//	for _, spec := range indexSpecs {
//	    if _, err = searchsync.RegisterIndex(registry, spec); err != nil {
//	        return err
//	    }
//	}
//
//	group, err := jobs.NewPoolGroup(ctx, registry.PoolSpecs(), consumerProvider)
//
// It owns no goroutine of its own and starts nothing: the pools are the
// PoolGroup's to start and stop, and the rebuilds are a scheduler's or an
// operator's to run. What it owns is the stamp buffers it built, which is why
// it has a Close.
type Registry struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	names   map[string]struct{}
	topics  map[string]struct{}
	entries []*registered

	// mu guards the list. Registration is a wiring-time act and reading is a
	// startup one, so the contention is nil — but a service that registers its
	// indexes from several do.Provide callbacks is registering them from
	// whichever goroutines resolved them, and a sliced append from two of those
	// is a data race rather than a merely surprising order.
	mu sync.Mutex
}

// RegistryOption configures a Registry, and through it everything registered in
// one. The zero configuration works: an absent logger logs nowhere, an absent
// tracer provider traces nowhere, and an absent metrics provider records
// nothing.
//
// The pillars live here rather than on each IndexSpec because they are the same
// three values for every index in a process, and repeating them per entity is
// the repetition this type exists to remove — an index whose metrics provider
// was left off the ninth stanza is an index with no lag histogram and no
// symptom besides.
type RegistryOption func(*Registry)

// WithRegistryLogger attaches a logger to every Syncer, Reindexer and stamp
// buffer built through this Registry.
func WithRegistryLogger(logger logging.Logger) RegistryOption {
	return func(r *Registry) { r.logger = logger }
}

// WithRegistryTracerProvider attaches a tracer provider, enabling a span per
// applied event and per rebuild.
func WithRegistryTracerProvider(tracerProvider tracing.Provider) RegistryOption {
	return func(r *Registry) { r.tracerProvider = tracerProvider }
}

// WithRegistryMetricsProvider attaches a metrics provider. Without one there is
// no lag histogram, which is the one instrument that distinguishes a working
// sync from a stopped one — see the package documentation.
func WithRegistryMetricsProvider(metricsProvider metrics.Provider) RegistryOption {
	return func(r *Registry) { r.metricsProvider = metricsProvider }
}

// WithRegistryPillars attaches a logger, tracer provider and metrics provider
// in one go, for the common case where a caller has already built them
// together. A nil Pillars attaches nothing.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them.
func WithRegistryPillars(p *observability.Pillars) RegistryOption {
	return func(r *Registry) { r.logger, r.tracerProvider, r.metricsProvider = p.Deps() }
}

// NewRegistry returns an empty Registry. It cannot fail: everything that can is
// in RegisterIndex, which is where the index being built is known.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{
		names:  map[string]struct{}{},
		topics: map[string]struct{}{},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	return r
}

// RegisterIndex builds one index's stamp buffer, Syncer and Reindexer and adds
// it to reg.
//
// The three are built together because they are one index. A Syncer without the
// Reindexer behind it cannot repair an index that was wrong before its first
// event; a Reindexer without the Syncer in front of it is a nightly batch job
// wearing a search index's name; and a Syncer built without the stamp buffer
// leaves last_indexed_at unwritten, which is the column the rebuild's own scan
// reads. Built one call at a time, per entity, the ninth of them is where one
// of the three is missing.
//
// It is a function rather than a method because a Registry holds indexes of
// every document type a service has and therefore cannot carry T itself.
//
// The stamp buffer is built only when the spec names a Stamp, and the Syncer is
// told about it only then — a Syncer handed a Stamper that is a nil buffer
// inside a non-nil interface panics on the first document it indexes, some time
// after the wiring that built it returned successfully.
func RegisterIndex[T any](reg *Registry, spec IndexSpec[T]) (*Registration[T], error) {
	switch {
	case reg == nil:
		return nil, ErrNilRegistry
	case spec.Name == "":
		return nil, ErrEmptyName
	case spec.Topic == "":
		return nil, ErrEmptyTopic
	case spec.Source == nil:
		return nil, ErrNilSource
	case spec.Target == nil:
		return nil, ErrNilTarget
	}

	entry := &registered{
		name:        spec.Name,
		topic:       spec.Topic,
		poolConfig:  spec.PoolConfig,
		poolOptions: spec.PoolOptions,
	}

	syncerOpts := []SyncerOption{
		WithSyncerLogger(reg.logger),
		WithSyncerTracerProvider(reg.tracerProvider),
		WithSyncerMetricsProvider(reg.metricsProvider),
	}

	if spec.Stamp != nil {
		stamps, err := NewStampBuffer(spec.Stamp, append([]batching.Option{
			batching.WithLogger(reg.logger),
			batching.WithTracerProvider(reg.tracerProvider),
			batching.WithMetricsProvider(reg.metricsProvider),
		}, spec.StampOptions...)...)
		if err != nil {
			return nil, platformerrors.Wrapf(err, "building %s stamp buffer", spec.Name)
		}

		entry.stamps = stamps
		syncerOpts = append(syncerOpts, WithSyncerStamper(stamps))
	}

	reindexOpts := []ReindexOption{
		WithReindexLogger(reg.logger),
		WithReindexTracerProvider(reg.tracerProvider),
		WithReindexMetricsProvider(reg.metricsProvider),
	}

	if spec.Pruner != nil {
		reindexOpts = append(reindexOpts, WithReindexPruner(spec.Pruner))
	}

	registration, err := buildRegistration(spec,
		append(syncerOpts, spec.SyncerOptions...), append(reindexOpts, spec.ReindexOptions...))
	if err != nil {
		// The buffer is running by now, and nothing else is going to close it:
		// this call is about to return no Registration, so the caller has
		// nothing to hand to reg.Close either.
		reg.discard(entry)

		return nil, err
	}

	entry.handler = registration.Syncer.Handle
	entry.reindex = registration.Reindexer.Reindex

	if err = reg.add(entry); err != nil {
		reg.discard(entry)

		return nil, err
	}

	return registration, nil
}

// buildRegistration is the pair of constructors, split out so RegisterIndex
// owns the bookkeeping and this owns the building.
func buildRegistration[T any](spec IndexSpec[T], syncerOpts []SyncerOption, reindexOpts []ReindexOption) (*Registration[T], error) {
	syncer, err := NewSyncer(spec.Name, spec.Source, spec.Target, syncerOpts...)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building %s syncer", spec.Name)
	}

	reindexer, err := NewReindexer(spec.Name, spec.Source, spec.Target, reindexOpts...)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building %s reindexer", spec.Name)
	}

	return &Registration[T]{Syncer: syncer, Reindexer: reindexer}, nil
}

// add records a built index, refusing a name or a topic another one already
// took.
//
// Both are refused rather than tolerated, and for different reasons. Two
// indexes under one name report one lag histogram covering both and rebuild
// under one scheduler lock, so one of them never rebuilds while the other runs.
// Two pools on one topic each get a share of that topic's messages, so each
// index sees roughly half its own events — which jobs.NewPoolGroup also
// refuses, from the middle of a start that has already brought other pools up.
func (r *Registry) add(entry *registered) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, taken := r.names[entry.name]; taken {
		return platformerrors.Wrapf(ErrDuplicateIndex, "search index %q", entry.name)
	}

	if _, taken := r.topics[entry.topic]; taken {
		return platformerrors.Wrapf(ErrDuplicateIndex, "search index topic %q", entry.topic)
	}

	r.names[entry.name] = struct{}{}
	r.topics[entry.topic] = struct{}{}
	r.entries = append(r.entries, entry)

	return nil
}

// discard closes the goroutine a half-built registration already started. It
// runs on a background context because there is no caller context to bound it —
// the buffer holds nothing yet, so the flush it does on the way out is empty.
func (r *Registry) discard(entry *registered) {
	if entry.stamps != nil {
		//nolint:errcheck // the caller is already being handed the failure that
		// matters; a Close error on top of it has nowhere useful to go.
		_ = entry.stamps.Close(context.Background())
	}
}

// Names returns the registered index names, in registration order.
func (r *Registry) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := make([]string, 0, len(r.entries))
	for _, entry := range r.entries {
		names = append(names, entry.name)
	}

	return names
}

// PoolSpecs returns one jobs.PoolSpec per registered index, ready for
// jobs.NewPoolGroup:
//
//	group, err := jobs.NewPoolGroup(ctx, registry.PoolSpecs(), consumerProvider,
//	    jobs.WithPoolGroupLogger(logger), jobs.WithPoolGroupDeadLetter(deadLetter))
//	if err != nil {
//	    return err
//	}
//
//	if err = group.Start(ctx); err != nil {
//	    return err
//	}
//
//	defer func() { _ = group.Close(shutdownCtx) }()
//
// This is the whole of the per-index pool wiring. What it replaces is one
// stanza per index naming that index's topic, that index's concurrency and that
// index's handler — nine near-identical blocks whose only interesting lines are
// the two strings, and where the tenth index is added by copying the ninth and
// changing one of them.
//
// The group is all-or-nothing about starting, which is what makes handing it
// every index at once better than starting them one at a time: a pool that
// fails to build takes the ones already consuming back down with it, rather
// than leaving a process on its way out still pulling messages nothing will
// finish handling.
func (r *Registry) PoolSpecs() []jobs.PoolSpec {
	r.mu.Lock()
	defer r.mu.Unlock()

	specs := make([]jobs.PoolSpec, 0, len(r.entries))
	for _, entry := range r.entries {
		specs = append(specs, jobs.PoolSpec{
			Config:  entry.poolConfig,
			Handler: entry.handler,
			Topic:   entry.topic,
			Options: entry.poolOptions,
		})
	}

	return specs
}

// ReindexAll rebuilds every registered index, in registration order, and
// returns what each rebuild did keyed by index name.
//
// It walks the registry rather than naming the indexes, which is the difference
// that matters: an index added to a service is rebuilt by this call without the
// call changing, and there is no ninth line for the tenth index to be missing
// from.
//
// A failed rebuild does not stop the others. They are independent indexes, the
// operator running this wants every one of them converged, and a single
// unreachable backend that aborted the walk would leave the eight indexes after
// it in the list untouched with nothing said about them. Every failure comes
// back joined, and the result map still carries what each rebuild landed before
// it stopped — Reindex reports that even when it fails partway.
//
// A cancelled context does stop it, because the remaining rebuilds would fail
// the same way and reporting nine copies of one cancellation says less than
// reporting the one.
func (r *Registry) ReindexAll(ctx context.Context) (map[string]*ReindexResult, error) {
	r.mu.Lock()
	entries := make([]*registered, len(r.entries))
	copy(entries, r.entries)
	r.mu.Unlock()

	results := make(map[string]*ReindexResult, len(entries))

	var errs []error

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			errs = append(errs, platformerrors.Wrapf(err, "reindexing %q", entry.name))

			break
		}

		result, err := entry.reindex(ctx)
		if result != nil {
			results[entry.name] = result
		}

		if err != nil {
			errs = append(errs, platformerrors.Wrapf(err, "reindexing %q", entry.name))
		}
	}

	return results, platformerrors.Join(errs...)
}

// Close closes the stamp buffers this Registry built, flushing whatever they
// still hold on ctx.
//
// It is the counterpart of the buffers not being returned: a Syncer owns no
// goroutine and a Registry only owns these, so this is the one shutdown
// obligation the whole pipeline has that is not a pool's or a scheduler's.
// Close it after the pools have drained — a buffer closed while workers are
// still indexing drops the stamps they produce afterwards, and the column then
// says those documents were never indexed.
//
// Every buffer is closed even if an earlier one fails, and the failures come
// back joined. Closing is safe to repeat.
func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()
	entries := make([]*registered, len(r.entries))
	copy(entries, r.entries)
	r.mu.Unlock()

	var errs []error

	for _, entry := range entries {
		if entry.stamps == nil {
			continue
		}

		if err := entry.stamps.Close(ctx); err != nil {
			errs = append(errs, platformerrors.Wrapf(err, "closing %q stamp buffer", entry.name))
		}
	}

	return platformerrors.Join(errs...)
}
