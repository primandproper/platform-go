package batching

import (
	"cmp"
	"maps"
	"slices"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

const (
	// DefaultFlushTimeout bounds one flush when WithFlushTimeout is not
	// supplied. It is generous on purpose: the batch's waiters have already
	// given up their own deadlines to it, and a write that lands late still
	// lands.
	DefaultFlushTimeout = 10 * time.Second

	// DefaultFlushInterval is how often a Buffer flushes when
	// WithFlushInterval is not supplied. GroupCommit has no interval at all —
	// see NewGroupCommit.
	DefaultFlushInterval = 5 * time.Second

	// DefaultMaxPending is how many distinct keys a Buffer accumulates before
	// flushing early, when WithMaxPending is not supplied.
	DefaultMaxPending = 1024
)

// Instrument and observability names. The two shapes are named apart rather
// than sharing one prefix because a process commonly runs both, and a single
// pair of counters cannot distinguish a group commit that is failing from a
// buffer that is.
const (
	groupCommitName = "batching_group_commit"
	bufferName      = "batching_buffer"

	itemCountKey = "batching.item_count"
)

// accumulator gathers submitted items into the slice one flush writes. It is
// the seam between "what arrived" and "what is written", which is where both
// deduplication and ordering live: a keyed accumulator collapses repeats and
// emits in key order, a plain one preserves arrival order and emits everything.
type accumulator[T any] struct {
	add     func(items []T)
	collect func() []T
	len     func() int
}

// newSliceAccumulator is the unconfigured case: every item is written, in the
// order it arrived.
func newSliceAccumulator[T any]() accumulator[T] {
	var items []T

	return accumulator[T]{
		add:     func(in []T) { items = append(items, in...) },
		collect: func() []T { return items },
		len:     func() int { return len(items) },
	}
}

// newKeyedAccumulator collapses items that share a key and emits them in key
// order.
//
// The order is the point, not a side effect of using a map. It is the total
// order every writer of the target table acquires locks in, so two processes
// merging different sets of the same popular keys queue behind each other
// rather than deadlocking. A map iteration would give a different order per
// flush, which is the worst case available.
//
// merge has to agree with however the write function resolves a conflict
// against storage. If merging inside the batch were more permissive than
// merging against the table, two callers naming one key would get a different
// result depending on whether they happened to land in the same flush — which
// is the kind of bug that only appears under load. It is called only when there
// is something to fold into; a nil merge keeps whichever item arrived last.
//
// It is a closure over a map rather than a type with methods because it is the
// one place K is known: GroupCommit carries no key type, so the accumulator has
// to erase it, and the erasing is the whole reason this seam exists.
func newKeyedAccumulator[T any, K cmp.Ordered](key func(T) K, merge func(existing, incoming T) T) accumulator[T] {
	items := make(map[K]T)

	return accumulator[T]{
		add: func(in []T) {
			for i := range in {
				k := key(in[i])

				if existing, ok := items[k]; ok && merge != nil {
					items[k] = merge(existing, in[i])

					continue
				}

				items[k] = in[i]
			}
		},
		collect: func() []T {
			out := make([]T, 0, len(items))
			for _, k := range slices.Sorted(maps.Keys(items)) {
				out = append(out, items[k])
			}

			return out
		},
		len: func() int { return len(items) },
	}
}

// batchShape is what the options that name the item type resolve to: how a
// batch accumulates, and how it is ordered on the way out. A zero order means
// none was configured, which is an ordinary outcome rather than an absence
// worth a second return value.
type batchShape[T any] struct {
	newAccumulator func() accumulator[T]
	order          func(a, b T) int
}

// resolveOrdering resolves the ordering, which is the half of the shape both
// batchers have, over a shape that otherwise accumulates and writes everything
// in arrival order. That default is the honest one: a batcher that invented a
// key would collapse items its caller meant to keep apart, and one that
// invented an order would claim a lock discipline it was never given.
func resolveOrdering[T any](o *options) (batchShape[T], error) {
	shape := batchShape[T]{newAccumulator: newSliceAccumulator[T]}

	if o.order == nil {
		return shape, nil
	}

	order, ok := o.order.(func(a, b T) int)
	if !ok {
		return shape, platformerrors.Wrapf(ErrItemTypeMismatch,
			"order is %T, want func(a, b %T) int", o.order, *new(T))
	}

	shape.order = order

	return shape, nil
}

// resolveShape is resolveOrdering plus the merge, for the batcher that has one.
// Buffer does not call it: a Buffer dedupes by its key type itself, so WithMerge
// is neither its to apply nor its to reject — which is what lets one slice of
// options build both.
func resolveShape[T any](o *options) (batchShape[T], error) {
	shape, err := resolveOrdering[T](o)
	if err != nil || o.newAccumulator == nil {
		return shape, err
	}

	newAccumulator, ok := o.newAccumulator.(func() accumulator[T])
	if !ok {
		return shape, platformerrors.Wrapf(ErrItemTypeMismatch,
			"merge is %T, want a merge over %T", o.newAccumulator, *new(T))
	}

	shape.newAccumulator = newAccumulator

	return shape, nil
}

// buildInstruments creates the instruments both shapes record under name: the
// operation trio for the flush, plus how large each flushed batch was.
func buildInstruments(mp metrics.Provider, name string) (*metrics.OperationSet, metrics.Float64Histogram, error) {
	set, err := metrics.NewOperationSet(mp, name)
	if err != nil {
		return nil, nil, platformerrors.Wrapf(err, "creating %s instruments", name)
	}

	sizes, err := metrics.EnsureMetricsProvider(mp).NewFloat64Histogram(name + "_batch_size")
	if err != nil {
		return nil, nil, platformerrors.Wrapf(err, "creating %s batch size histogram", name)
	}

	return set, sizes, nil
}
