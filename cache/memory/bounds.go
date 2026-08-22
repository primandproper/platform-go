package memory

import (
	"container/list"
	"context"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
)

// ErrUnknownEvictionPolicy indicates WithMaxEntries was given a policy that is
// not one of this package's constants. It is reported at construction rather
// than defaulted to one of them, because the policy decides which data a full
// cache loses, and a caller who typed a policy is owed the one they named.
var ErrUnknownEvictionPolicy = errors.New("unknown eviction policy")

// EvictionPolicy selects which entry a size-bounded cache drops when a write
// would take it past its bound.
//
// The zero value is not a policy: WithMaxEntries takes one explicitly, so that
// bounding a cache is never separable from saying what it forgets.
type EvictionPolicy uint8

const (
	// EvictLeastRecentlyUsed drops the entry that has gone longest without
	// being read or written. It is what a cache in front of an expensive
	// computation usually wants: the working set stays resident and the tail
	// pays for the bound.
	//
	// Recording a read is a mutation, so a cache using this policy takes the
	// write lock on the read path — Get and GetMany stop being shared reads.
	// Under heavy concurrent hits on a small key set that is the difference
	// between readers running in parallel and readers queueing, which is the
	// case for preferring EvictOldestWritten.
	EvictLeastRecentlyUsed EvictionPolicy = iota + 1

	// EvictOldestWritten drops the entry written longest ago, however often it
	// has been read since. Overwriting a key counts as writing it, so a value
	// that is refreshed stays put; a value that is only read does not.
	//
	// It keeps the read path a shared read, which is what recommends it for a
	// memo whose entries are refreshed on a timer rather than promoted by use.
	EvictOldestWritten
)

// String returns the policy's configuration name, and is what ParseEvictionPolicy
// accepts. An undefined policy renders as "unknown" rather than its number, so a
// message built from it reads the same as one built from a name.
func (p EvictionPolicy) String() string {
	switch p {
	case EvictLeastRecentlyUsed:
		return "least_recently_used"
	case EvictOldestWritten:
		return "oldest_written"
	default:
		return "unknown"
	}
}

// ParseEvictionPolicy resolves a policy's configuration name, case- and
// space-insensitively, and accepts the shorthands "lru" and "fifo". It exists
// for configuration-driven wiring, which can only carry a string; an
// unrecognized name is an error rather than a default, for the reason
// ErrUnknownEvictionPolicy gives.
func ParseEvictionPolicy(name string) (EvictionPolicy, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case EvictLeastRecentlyUsed.String(), "lru":
		return EvictLeastRecentlyUsed, nil
	case EvictOldestWritten.String(), "fifo":
		return EvictOldestWritten, nil
	default:
		return 0, errors.Wrapf(ErrUnknownEvictionPolicy, "eviction policy %q", name)
	}
}

// valid reports whether p names a policy this package implements. WithMaxEntries
// cannot reject one itself — an Option has nowhere to report an error — so the
// check lands at construction, which is still before the cache exists.
func (p EvictionPolicy) valid() bool {
	switch p {
	case EvictLeastRecentlyUsed, EvictOldestWritten:
		return true
	default:
		return false
	}
}

// WithMaxEntries bounds the cache to maxEntries live entries, dropping one
// entry per policy whenever a write would take it past the bound.
//
// Without it the map is bounded only by expiry: entries leave when a read
// discovers them expired, or when a janitor sweeps them, and nothing at all
// leaves a cache whose entries do not expire. That is fine for a keyspace whose
// cardinality is known — a fixed set of feature flags, one entry per tenant.
// It is not fine for a cache keyed by anything a caller can vary freely: a
// request fingerprint, a query's parameters, a rendered URL. There the map
// grows until something evicts it, and TTL only bounds the growth if entries
// arrive slower than they expire.
//
// The bound is on entries, not bytes. This package cannot size an arbitrary T,
// so a caller whose values vary wildly in size should pick maxEntries against
// the largest, not the average.
//
// A non-positive maxEntries leaves the cache unbounded, so a configured bound
// can be turned off without changing the wiring. An undefined policy is
// rejected by the constructor with ErrUnknownEvictionPolicy.
func WithMaxEntries(maxEntries int, policy EvictionPolicy) Option {
	return func(o *options) {
		if maxEntries <= 0 {
			return
		}

		o.maxEntries = maxEntries
		o.evictionPolicy = policy
	}
}

// evictionIndex holds the eviction order for a bounded cache: the front of the
// list is the entry that will survive longest, the back is the next candidate.
//
// A nil *evictionIndex is an unbounded cache and every method is a no-op on it,
// so the cache's mutation paths carry no branch of their own and cannot forget
// to keep an index they do not have.
//
// It is guarded by the cache's mutex rather than one of its own: every method
// is called with that lock already held, and a second lock would mean two
// orderings to keep straight for a structure that is never touched
// independently of the map it indexes.
type evictionIndex struct {
	elements   map[string]*list.Element
	order      *list.List
	maxEntries int
	policy     EvictionPolicy
}

// newEvictionIndex builds the index for a bound, or nil for an unbounded cache.
// The policy is checked by the constructor before this is reached, since an
// index is only ever built for a bound that was already accepted.
func newEvictionIndex(maxEntries int, policy EvictionPolicy) *evictionIndex {
	if maxEntries <= 0 {
		return nil
	}

	return &evictionIndex{
		elements:   make(map[string]*list.Element),
		order:      list.New(),
		maxEntries: maxEntries,
		policy:     policy,
	}
}

// recordsReads reports whether a read has to be written down, which is what
// decides between the read lock and the write lock on the read path.
func (x *evictionIndex) recordsReads() bool {
	return x != nil && x.policy == EvictLeastRecentlyUsed
}

// recordRead promotes key for having been served.
//
// It is a no-op for any policy that does not track reads, which is what makes
// it safe to call under the read lock: a cache that only takes the RLock to
// read is a cache whose policy never mutates here.
func (x *evictionIndex) recordRead(key string) {
	if !x.recordsReads() {
		return
	}

	if el, ok := x.elements[key]; ok {
		x.order.MoveToFront(el)
	}
}

// recordWrite promotes key for having been written, under either policy: a
// value that was just stored is the newest thing in the cache no matter what
// decides eviction.
func (x *evictionIndex) recordWrite(key string) {
	if x == nil {
		return
	}

	if el, ok := x.elements[key]; ok {
		x.order.MoveToFront(el)

		return
	}

	x.elements[key] = x.order.PushFront(key)
}

// forget drops key from the order, for entries leaving by any route other than
// the bound — deleted, flushed, or expired.
func (x *evictionIndex) forget(key string) {
	if x == nil {
		return
	}

	if el, ok := x.elements[key]; ok {
		x.order.Remove(el)
		delete(x.elements, key)
	}
}

// reset empties the order, for a Flush that clears the map wholesale.
func (x *evictionIndex) reset() {
	if x == nil {
		return
	}

	x.order.Init()
	clear(x.elements)
}

// evictOverflow removes candidates from the order until it holds no more than
// maxEntries, and returns their keys so the caller can delete the entries
// themselves. It is the index's only lossy operation, and it does not touch the
// map: keeping the two halves in one place would mean the index knowing the
// cache's type parameter.
func (x *evictionIndex) evictOverflow() []string {
	if x == nil || x.order.Len() <= x.maxEntries {
		return nil
	}

	evicted := make([]string, 0, x.order.Len()-x.maxEntries)

	for x.order.Len() > x.maxEntries {
		el := x.order.Back()
		if el == nil {
			break
		}

		x.order.Remove(el)

		if key, ok := el.Value.(string); ok {
			delete(x.elements, key)
			evicted = append(evicted, key)
		}
	}

	return evicted
}

// evictOverflowLocked drops entries until the cache is back within its bound.
// The caller must already hold the write lock.
//
// Capacity evictions are counted separately from expiry evictions. They answer
// different questions — "is the bound too small" against "is the TTL too short"
// — and a single counter that moved for both would answer neither.
func (i *Cache[T]) evictOverflowLocked(ctx context.Context) {
	for _, key := range i.index.evictOverflow() {
		delete(i.cache, key)
		i.cacheCapacityEvictCounter.Add(ctx, 1)
	}
}
