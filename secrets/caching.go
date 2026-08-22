package secrets

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/panicking"
	"github.com/primandproper/platform-go/v13/retry"

	"golang.org/x/sync/singleflight"
)

const cachingSourceName = "caching_secret_source"

var (
	// ErrInvalidCacheTTL indicates NewCachingSource was given a non-positive
	// TTL. There is no "cache forever" setting on purpose: an entry that never
	// expires is read-once-hold-forever with extra steps, which is the pattern
	// this decorator exists to replace.
	ErrInvalidCacheTTL = errors.New("caching secret source: ttl must be positive")

	// ErrInvalidRefreshInterval indicates a refresh interval that cannot beat
	// the TTL it was paired with, so the refresh could never fire before the
	// entry it was meant to keep warm had already expired.
	ErrInvalidRefreshInterval = errors.New("caching secret source: refresh interval must be shorter than the ttl")
)

// ChangeFunc is a rotation hook: the callback OnChange registers, called with
// the value a secret used to have and the value it has now.
//
// It runs on its own goroutine, so a slow hook delays neither the refresh that
// observed the change nor any caller reading a secret. A panic is contained and
// logged rather than taking the process down with it.
type ChangeFunc func(oldValue, newValue string)

// CachingSource is what NewCachingSource returns: a SecretSource that answers
// from memory, plus the one thing caching makes possible that fetch-per-call
// does not — being told when a value changed.
//
// It is a distinct interface rather than an addition to SecretSource because
// only a source that re-reads has anything to report. A provider that fetches
// on every call has no "before" to compare against, and widening the interface
// every implementation satisfies would oblige each of them to grow a method
// that could only ever be a stub.
type CachingSource interface {
	SecretSource

	// OnChange registers fn to be called when this source observes name's value
	// change, and returns a function that unregisters it. Registering the same
	// name twice registers two hooks; both fire.
	//
	// A change is observed by re-reading, so hooks fire for the secrets this
	// source holds — a name that has never been read has nothing to compare
	// against and no entry for the refresh to visit, so its hooks stay silent
	// until the first read of it. Callers wiring a hook for a secret they have
	// not read yet should read it once, which is what a boot-time resolution
	// does anyway.
	OnChange(name string, fn ChangeFunc) (cancel func())
}

var _ CachingSource = (*TTLCachingSource)(nil)

// cachedSecret is one held value. fetchedAt is when the backend last answered
// for it, which is both what the TTL is measured from and what the staleness
// gauge reports.
type cachedSecret struct {
	fetchedAt time.Time
	value     string
}

// TTLCachingSource is the CachingSource that holds each secret for a TTL and
// refreshes it in the background. It is exported, and returned by
// NewCachingSource, so a caller can depend on the source it built rather than on
// the CachingSource seam.
type TTLCachingSource struct {
	o11y                  observability.Observer
	clock                 clock.Clock
	source                SecretSource
	hitCounter            metrics.Int64Counter
	missCounter           metrics.Int64Counter
	refreshCounter        metrics.Int64Counter
	refreshFailureCounter metrics.Int64Counter
	rotationCounter       metrics.Int64Counter
	staleReadCounter      metrics.Int64Counter
	stalenessGauge        metrics.Float64Gauge
	// jitter spreads a fleet's refreshes apart, and only ever shortens the
	// interval. retry.Equal is what gives it that property: a wait that could
	// land *after* its configured interval could land after the TTL too, and
	// the interval-shorter-than-TTL check at construction would then be
	// promising something the scheduling does not deliver.
	jitter retry.Jitter
	// flight collapses concurrent resolutions of one name, so a cold-key
	// stampede costs one backend call rather than one per caller.
	flight      singleflight.Group
	closeErr    error
	stopRefresh context.CancelFunc
	refreshDone chan struct{}
	entries     map[string]cachedSecret
	hooks       map[string]map[uint64]ChangeFunc
	ttl         time.Duration
	nextHookID  uint64
	entriesMu   sync.RWMutex
	hooksMu     sync.RWMutex
	closeOnce   sync.Once
}

// NewCachingSource wraps source in a read-through cache whose entries live for
// ttl, so repeated reads of a secret cost one backend round-trip per TTL
// instead of one per call.
//
// It exists because the alternative callers reach for is worse. A GetSecret
// against GCP or SSM is a network call, which pushes every caller toward
// resolving at boot and holding the value for the life of the process — and
// that is precisely the shape that turns a key rotation into an outage, since
// nothing in the process can react to the backend's value changing. A TTL is
// the smallest thing that fixes both halves: the round-trip is amortized, and
// the value is re-read often enough that a rotation is noticed.
//
// Pair it with WithRefresh to keep entries warm in the background, and with
// OnChange to be told when a re-read sees a new value. Refresh is what moves
// the round-trip off the hot path entirely; OnChange is what lets a caller
// re-derive whatever it built out of the old value — a signing keyring, a
// database credential, an SDK client — without a restart.
//
// The TTL must be positive. Close closes the wrapped source, so a caller closes
// what this returns and nothing else.
//
// # What is and is not cached
//
// A secret the backend reports as absent is not cached: ErrSecretNotFound goes
// straight back to the caller and nothing is stored, so a secret created after
// this source started is visible on the next read rather than a TTL later. For
// the same reason a re-read that comes back ErrSecretNotFound drops whatever
// was held — an affirmative "no such secret" is an answer, not a failed lookup,
// and a deleted secret must stop being served.
//
// Every other backend failure is treated as a failure to reach an answer, not
// as an answer. A read whose fetch fails is served the held value if there is
// one, counted as a stale read, and reported on the staleness gauge; a
// background refresh that fails leaves the entry alone and logs. Old secret
// beats no secret for every purpose except revocation, and revocation outlives
// any TTL a process could pick anyway.
func NewCachingSource(source SecretSource, ttl time.Duration, opts ...Option) (*TTLCachingSource, error) {
	if source == nil {
		return nil, errors.New("caching secret source: source is required")
	}
	if ttl <= 0 {
		return nil, errors.Wrapf(ErrInvalidCacheTTL, "ttl %s", ttl)
	}

	o := newOptions(opts)

	// Rejected here rather than clamped, because both readings of a too-long
	// interval are wrong to guess at: the operator meant a shorter refresh, or
	// they meant a longer TTL, and silently picking one hides which.
	if o.refreshInterval > 0 && o.refreshInterval >= ttl {
		return nil, errors.Wrapf(ErrInvalidRefreshInterval, "refresh interval %s, ttl %s", o.refreshInterval, ttl)
	}

	c := &TTLCachingSource{
		o11y:    observability.NewObserver(cachingSourceName, o.logger, o.tracerProvider),
		clock:   clock.NewClock(),
		jitter:  retry.Equal(o.rand),
		source:  source,
		entries: make(map[string]cachedSecret),
		hooks:   make(map[string]map[uint64]ChangeFunc),
		ttl:     ttl,
	}

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error

	if c.hitCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_hits", cachingSourceName)); err != nil {
		return nil, errors.Wrap(err, "creating hit counter")
	}

	if c.missCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_misses", cachingSourceName)); err != nil {
		return nil, errors.Wrap(err, "creating miss counter")
	}

	if c.refreshCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_refreshes", cachingSourceName)); err != nil {
		return nil, errors.Wrap(err, "creating refresh counter")
	}

	if c.refreshFailureCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_refresh_failures", cachingSourceName)); err != nil {
		return nil, errors.Wrap(err, "creating refresh failure counter")
	}

	if c.rotationCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_rotations", cachingSourceName)); err != nil {
		return nil, errors.Wrap(err, "creating rotation counter")
	}

	if c.staleReadCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_stale_reads", cachingSourceName)); err != nil {
		return nil, errors.Wrap(err, "creating stale read counter")
	}

	// A gauge rather than a counter: staleness is a level, and the question it
	// answers — "how old is the value this process is actually using?" — is the
	// one that matters when a backend has been failing quietly.
	if c.stalenessGauge, err = mp.NewFloat64Gauge(fmt.Sprintf("%s_staleness_seconds", cachingSourceName)); err != nil {
		return nil, errors.Wrap(err, "creating staleness gauge")
	}

	// Started last, so the refresh cannot observe a half-built source: the
	// counters it records to exist by the time it can run.
	if o.refreshCtx != nil && o.refreshInterval > 0 {
		refreshCtx, stop := context.WithCancel(o.refreshCtx)
		c.stopRefresh = stop
		c.refreshDone = make(chan struct{})

		go c.refreshEvery(refreshCtx, o.refreshInterval)
	}

	return c, nil
}

// GetSecret answers from the cache when it holds an unexpired value, and
// resolves through the wrapped source otherwise.
func (c *TTLCachingSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := c.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.SecretNameKey, name)

	if entry, ok := c.lookup(name); ok && !c.expired(entry) {
		c.hitCounter.Add(ctx, 1)
		c.recordStaleness(ctx, entry)

		return entry.value, nil
	}

	c.missCounter.Add(ctx, 1)

	value, err := c.resolve(ctx, name)
	if err == nil {
		return value, nil
	}

	// The held value is the fallback, but only for a failure to reach an
	// answer. ErrSecretNotFound is an answer, and fetch has already dropped the
	// entry by the time this runs, so the lookup below finds nothing for it —
	// the explicit check states the intent rather than relying on that ordering.
	if !stderrors.Is(err, ErrSecretNotFound) {
		if entry, ok := c.lookup(name); ok {
			c.staleReadCounter.Add(ctx, 1)
			c.recordStaleness(ctx, entry)
			op.Acknowledge(err, "resolving secret %q, serving the value held since %s", name, entry.fetchedAt)

			return entry.value, nil
		}
	}

	return "", op.Error(err, "resolving secret %q", name)
}

// OnChange registers a rotation hook for name.
func (c *TTLCachingSource) OnChange(name string, fn ChangeFunc) func() {
	if fn == nil {
		return func() {}
	}

	c.hooksMu.Lock()
	defer c.hooksMu.Unlock()

	c.nextHookID++
	id := c.nextHookID

	if c.hooks[name] == nil {
		c.hooks[name] = make(map[uint64]ChangeFunc)
	}
	c.hooks[name][id] = fn

	return func() {
		c.hooksMu.Lock()
		defer c.hooksMu.Unlock()

		delete(c.hooks[name], id)
		if len(c.hooks[name]) == 0 {
			delete(c.hooks, name)
		}
	}
}

// Close stops the refresh, waits for it to finish so the wrapped source is not
// closed out from under an in-flight fetch, and then closes that source.
//
// It is idempotent and reports the same error every time: closing the wrapped
// source twice is the wrapped source's problem, and a decorator should not
// create it.
func (c *TTLCachingSource) Close() error {
	c.closeOnce.Do(func() {
		if c.stopRefresh != nil {
			c.stopRefresh()
			<-c.refreshDone
		}

		c.closeErr = c.source.Close()
	})

	return c.closeErr
}

// resolve reads name through the wrapped source, collapsing concurrent
// resolutions of one name into a single call.
//
// The fetch runs on a context detached from whichever caller happened to start
// it, since its result belongs to every caller waiting on it and one of them
// giving up must not cancel the others' round-trip. A caller whose own context
// ends first stops waiting and gets that context's error, while the fetch
// continues for the rest.
func (c *TTLCachingSource) resolve(ctx context.Context, name string) (string, error) {
	fetchCtx := context.WithoutCancel(ctx)

	return c.join(ctx, name, func() (any, error) {
		// A resolution that started after another one for the same name
		// finished may find the entry already filled, so it re-reads before
		// going to the backend. Callers that joined a flight already in
		// progress never reach this func at all — they share its result — so
		// this covers only the serial case, which is where a stampede of slow
		// callers lands once the first one returns.
		if entry, ok := c.lookup(name); ok && !c.expired(entry) {
			return entry.value, nil
		}

		return c.fetch(fetchCtx, name)
	})
}

// join runs fn under the flight for name and waits for whichever flight it
// joined, giving up early if ctx ends.
func (c *TTLCachingSource) join(ctx context.Context, name string, fn func() (any, error)) (string, error) {
	ch := c.flight.DoChan(name, fn)

	select {
	case <-ctx.Done():
		return "", errors.Wrapf(ctx.Err(), "resolving %q", name)
	case res := <-ch:
		if res.Err != nil {
			// Passed through unwrapped so a caller's errors.Is(err,
			// ErrSecretNotFound) reads the same whether the miss came from this
			// source or from the provider underneath it.
			return "", res.Err
		}

		value, ok := res.Val.(string)
		if !ok {
			return "", errors.Newf("resolving %q returned %T, want string", name, res.Val)
		}

		return value, nil
	}
}

// fetch reads name from the wrapped source and stores what it gets. It is the
// only place the backend is called, and therefore the only place a change can
// be observed.
func (c *TTLCachingSource) fetch(ctx context.Context, name string) (string, error) {
	value, err := c.source.GetSecret(ctx, name)
	if err != nil {
		if stderrors.Is(err, ErrSecretNotFound) {
			c.evict(name)
		}

		return "", err
	}

	oldValue, existed := c.store(name, value)
	if existed && oldValue != value {
		c.rotationCounter.Add(ctx, 1)
		c.notify(name, oldValue, value)
	}

	return value, nil
}

// refreshEvery re-resolves the held secrets until ctx is done.
//
// It sleeps rather than ticks because each wait is jittered independently, and
// it sleeps through the injected clock, so inside a testing/synctest bubble the
// refresh advances with the bubble's fake time and needs no test double.
func (c *TTLCachingSource) refreshEvery(ctx context.Context, interval time.Duration) {
	defer close(c.refreshDone)

	for {
		if err := c.clock.Sleep(ctx, c.jitter(interval)); err != nil {
			return
		}

		c.refresh(ctx)
	}
}

// refresh re-resolves every held secret once.
//
// The names are snapshotted first so the map is not held across the round
// trips, and each one goes through the flight, so a refresh and a caller's cold
// read of the same name cost one backend call between them rather than two.
func (c *TTLCachingSource) refresh(ctx context.Context) {
	ctx, op := c.o11y.BeginCustom(ctx, "refresh")
	defer op.End()

	for _, name := range c.names() {
		// Checked between names rather than only at the sleep, so a Close
		// midway through a wide refresh stops at the next name instead of
		// working through the rest of them.
		if ctx.Err() != nil {
			return
		}

		c.refreshCounter.Add(ctx, 1)

		if _, err := c.join(ctx, name, func() (any, error) { return c.fetch(ctx, name) }); err != nil {
			c.refreshFailureCounter.Add(ctx, 1)

			// The entry is deliberately left as it was — value and fetchedAt
			// both — so reads keep being answered from it and its staleness
			// keeps growing where the gauge can be seen. Stamping it fresh
			// would hide exactly the failure worth alerting on.
			op.Acknowledge(err, "refreshing secret %q", name)
		}
	}
}

// notify runs name's hooks, each on its own goroutine.
//
// Off the calling goroutine because the caller is either a refresh, which must
// keep sweeping, or a read, which is somebody's request. Contained because a
// hook is caller-supplied code — re-deriving a keyring, rebuilding a client —
// and a panic in it should cost the rotation, not the process.
func (c *TTLCachingSource) notify(name, oldValue, newValue string) {
	c.hooksMu.RLock()
	hooks := slices.Collect(maps.Values(c.hooks[name]))
	c.hooksMu.RUnlock()

	logger := c.o11y.Logger().WithValue(keys.SecretNameKey, name)

	for _, hook := range hooks {
		go func() {
			if err := panicking.Contain(func() error { hook(oldValue, newValue); return nil }); err != nil {
				logger.Error("running a rotation hook", err)
			}
		}()
	}
}

// lookup returns the held entry for name, if there is one.
func (c *TTLCachingSource) lookup(name string) (cachedSecret, bool) {
	c.entriesMu.RLock()
	defer c.entriesMu.RUnlock()

	entry, ok := c.entries[name]

	return entry, ok
}

// store records a freshly fetched value, reporting what it replaced. The
// comparison a caller makes against the returned value happens under the same
// lock that installed the new one, so two fetches racing on a name cannot both
// report the same change.
func (c *TTLCachingSource) store(name, value string) (oldValue string, existed bool) {
	c.entriesMu.Lock()
	defer c.entriesMu.Unlock()

	previous, existed := c.entries[name]
	c.entries[name] = cachedSecret{value: value, fetchedAt: c.clock.Now()}

	return previous.value, existed
}

// evict drops name's entry.
func (c *TTLCachingSource) evict(name string) {
	c.entriesMu.Lock()
	defer c.entriesMu.Unlock()

	delete(c.entries, name)
}

// names snapshots the held keys.
func (c *TTLCachingSource) names() []string {
	c.entriesMu.RLock()
	defer c.entriesMu.RUnlock()

	return slices.Collect(maps.Keys(c.entries))
}

// expired reports whether entry has outlived the TTL.
func (c *TTLCachingSource) expired(entry cachedSecret) bool {
	return c.clock.Since(entry.fetchedAt) >= c.ttl
}

// recordStaleness reports how old the value being served is.
func (c *TTLCachingSource) recordStaleness(ctx context.Context, entry cachedSecret) {
	c.stalenessGauge.Record(ctx, c.clock.Since(entry.fetchedAt).Seconds())
}
