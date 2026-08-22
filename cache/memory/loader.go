package memory

import (
	"context"
	stderrors "errors"
	"sync"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/errors"

	"golang.org/x/sync/errgroup"
)

// ErrLoaderTypeMismatch indicates WithLoader was given a loader for a type
// other than the one the cache was built for.
var ErrLoaderTypeMismatch = errors.New("loader type does not match cache type")

// Loader computes the value for a key the cache does not hold.
//
// Returning cache.ErrNotFound means the key genuinely has no value; the caller's
// Get returns cache.ErrNotFound and nothing is stored, so absence is not cached.
// Returning (nil, nil) is a value — a nil *T is stored and served like any
// other, which is how a loader says "the answer is nothing" rather than "there
// is no answer". Any other error is returned to the caller as-is and, again,
// nothing is stored: a failed computation must not become a cached one.
type Loader[T any] func(ctx context.Context, key string) (*T, error)

// WithLoader makes the cache read through to loader: a Get that misses runs the
// loader, stores what it returns, and returns it, and concurrent misses on one
// key produce a single loader call whose result they all share.
//
// Without it the cache only answers for what has been written to it, so every
// caller that misses computes its own value. For a memo in front of an
// expensive computation — an aggregate query, a remote fetch — that turns each
// expiry into a thundering herd, one computation per concurrent reader, which
// is the load the memo was added to remove.
//
// GetMany reads through too, loading its missing keys concurrently. The
// concurrency is the batch's, so a caller handing it a thousand missing keys
// gets a thousand loads in flight; batch size is the caller's to choose, and a
// loader that must not be called that widely should limit itself.
//
// The loader runs on a context detached from the cancellation of whichever
// caller happened to start it, since its result belongs to every caller waiting
// on it and one of them giving up must not cancel the others' computation.
// Values and the surrounding trace carry over; deadlines do not, so a loader
// that needs a time limit has to impose its own. A caller whose own context
// ends first stops waiting and gets that context's error, while the load
// continues for the others.
//
// T is inferred from the loader, so this needs no type argument:
//
//	memory.WithLoader(func(ctx context.Context, key string) (*Stats, error) {
//		return q.aggregate(ctx, key)
//	})
//
// It must match the cache it configures. Because Option carries no type
// parameter, a loader for the wrong type cannot be rejected by the compiler;
// NewInMemoryCache returns ErrLoaderTypeMismatch instead, at construction.
func WithLoader[T any](loader Loader[T]) Option {
	return func(o *options) {
		if loader != nil {
			o.loader = loader
		}
	}
}

// load resolves key through the loader, collapsing concurrent calls for the
// same key into one.
//
// The flight is joined rather than the value merely recomputed, so N concurrent
// misses cost one computation. The gap this closes is between the read that
// misses and the write that fills: every caller arriving in that window would
// otherwise miss too, and the window is exactly as wide as the computation is
// slow — the more expensive the loader, the more callers pile into it.
func (i *Cache[T]) load(ctx context.Context, key string) (*T, error) {
	loadCtx := context.WithoutCancel(ctx)

	ch := i.flight.DoChan(key, func() (any, error) {
		// A flight that started after another one for the same key finished may
		// find the key already filled, so it re-reads before computing. Joiners
		// of a flight already in progress never reach this func at all — they
		// share its result — so this covers only the serial case, which is
		// where a stampede of slow callers lands once the first one returns.
		if e, ok := i.lookup(key); ok && !i.expired(e) {
			return e.value, nil
		}

		i.cacheLoadCounter.Add(loadCtx, 1)

		value, err := i.loader(loadCtx, key)
		if err != nil {
			return nil, err
		}

		if err = i.Set(loadCtx, key, value); err != nil {
			return nil, err
		}

		return value, nil
	})

	select {
	case <-ctx.Done():
		return nil, errors.Wrapf(ctx.Err(), "loading %q", key)
	case res := <-ch:
		if res.Err != nil {
			// Passed through unwrapped so a caller's errors.Is(err,
			// cache.ErrNotFound) reads the same whether the miss came from an
			// empty cache or from a loader reporting there is nothing to load.
			if stderrors.Is(res.Err, cache.ErrNotFound) {
				return nil, cache.ErrNotFound
			}

			return nil, errors.Wrapf(res.Err, "loading %q", key)
		}

		value, ok := res.Val.(*T)
		if !ok {
			return nil, errors.Newf("loader for %q returned %T, want *%T", key, res.Val, *new(T))
		}

		return value, nil
	}
}

// loadMany resolves the keys a batch read missed, concurrently.
//
// Duplicate keys are not filtered: two loads of one key join the same flight,
// so the deduplication that load already does covers the case for free.
//
// A key the loader reports as absent is omitted, matching GetMany's contract
// that a missing key is a miss. Any other error fails the whole call — a
// partial map indistinguishable from a partial miss is precisely what GetMany's
// callers cannot detect.
func (i *Cache[T]) loadMany(ctx context.Context, keys []string) (map[string]*T, error) {
	var (
		mu     sync.Mutex
		loaded = make(map[string]*T, len(keys))
	)

	group, groupCtx := errgroup.WithContext(ctx)

	for _, key := range keys {
		group.Go(func() error {
			value, err := i.load(groupCtx, key)
			if err != nil {
				if stderrors.Is(err, cache.ErrNotFound) {
					return nil
				}

				return err
			}

			mu.Lock()
			defer mu.Unlock()

			loaded[key] = value

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}

	return loaded, nil
}
