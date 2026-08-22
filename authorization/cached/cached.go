// Package cached wraps any authorization.PolicyResolver in a cache.
//
// It is a decorator rather than a third backend: it composes with static (where
// it is redundant) and with database (where it is the difference between a
// query per session build and a query per policy change).
//
// The cache is keyed by role names, not by principal. That is what makes it
// worth having — a deployment with five roles has five hot entries shared by
// every principal, rather than one per user, so the hit rate approaches one and
// the memory cost does not grow with traffic.
//
// # Why caching is safe here and would not be elsewhere
//
// A resolved PermissionSet may live in a cache. It may never live in a
// credential. That distinction is the reason this package's seam sits at the
// resolver: a cache entry that fails to decode after a deploy degrades to a
// query, while a session or token that fails to decode logs its holder out.
// This package is consequently the only place in authorization where an
// encoding change can bite, and the only place where being bitten costs
// nothing — keys carry a format version, and any decode failure is treated as a
// miss rather than an error.
//
// The cost of caching is staleness: a policy edit takes effect when the entry
// expires, not immediately. Invalidate narrows that window in the process that
// made the edit; other replicas wait out the TTL. Set the TTL to the longest
// delay you would accept between revoking a role's authority and it taking
// effect everywhere.
//
// Both invalidation methods are declared by authorization.PolicyInvalidator, so
// a caller holding the interface authorizationcfg returns can type-assert for
// that rather than for this concrete type.
//
// # What it reports
//
// Hit and miss counts come from the cache backend itself — cache/redis and
// cache/memory both emit them — so this package does not restate them. What it
// adds is the fault path, which the cache cannot report because from its side a
// stale or undecodable entry is a served result:
//
//   - authorization_cached_read_faults, entries that could not be read and
//     degraded to the inner resolver.
//   - authorization_cached_write_faults, resolutions that could not be stored.
//     A sustained rise here looks like a permanently cold cache, not a broken
//     one, which is why it is worth a counter of its own.
//
// Each resolution also carries keys.AuthorizationCacheOutcomeKey on its span, so
// a single slow request can be attributed to a miss without reading counters.
package cached

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/cache"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// serviceName names the Resolver's logger, spans, and metrics.
const serviceName = "authorization_cached"

// Cache outcomes, recorded to the span under
// keys.AuthorizationCacheOutcomeKey.
const (
	outcomeHit   = "hit"
	outcomeMiss  = "miss"
	outcomeFault = "fault"
)

// keyFormatVersion prefixes every cache key.
//
// Bump it when PermissionSet's encoding changes and entries under the old one
// are out there: bumping makes those unreachable rather than mis-decoded, at a
// cost of one round of misses. Both halves have to hold. The move to
// MarshalBinary changed the encoding and did not bump, because no deployment of
// this module exists to have written an entry worth stranding.
const keyFormatVersion = "authzv1"

// DefaultTTL is the entry lifetime when WithTTL is not supplied.
const DefaultTTL = 5 * time.Minute

var (
	_ authorization.PolicyResolver    = (*Resolver)(nil)
	_ authorization.PolicyInvalidator = (*Resolver)(nil)
)

// Resolver caches the results of an inner PolicyResolver.
type Resolver struct {
	inner authorization.PolicyResolver
	cache cache.Cache[authorization.PermissionSet]
	o11y  observability.Observer

	readFaultsCounter  metrics.Int64Counter
	writeFaultsCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this resolver actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	metricsProvider metrics.Provider
	tracerProvider  tracing.Provider

	generation atomic.Uint64
	ttl        time.Duration
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithLogger attaches a logger. Cache faults are logged; hits and misses are not.
func WithLogger(logger logging.Logger) Option {
	return func(r *Resolver) {
		r.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider, so that a resolution served
// from cache and one that fell through to the inner resolver are
// distinguishable in a trace rather than both being absent from it.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(r *Resolver) {
		r.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider.
//
// The counters here deliberately do not duplicate the hit and miss counters the
// cache backends already emit (see cache/redis and cache/memory). They count
// faults: the entries this Resolver could not read or write and silently
// degraded around. That path is invisible from the cache's own metrics, because
// from the cache's side an undecodable entry is a served result.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(r *Resolver) {
		r.metricsProvider = metricsProvider
	}
}

// WithTTL sets the entry lifetime. A non-positive value uses DefaultTTL.
func WithTTL(ttl time.Duration) Option {
	return func(r *Resolver) {
		if ttl > 0 {
			r.ttl = ttl
		}
	}
}

// NewResolver wraps inner with c.
func NewResolver(
	inner authorization.PolicyResolver,
	c cache.Cache[authorization.PermissionSet],
	opts ...Option,
) (*Resolver, error) {
	if inner == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "inner policy resolver")
	}
	if c == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "cache")
	}

	r := &Resolver{inner: inner, cache: c, ttl: DefaultTTL}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.readFaultsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_read_faults", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating cached policy read faults counter")
	}
	if r.writeFaultsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_write_faults", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating cached policy write faults counter")
	}

	return r, nil
}

// PermissionsForRoles returns the cached resolution for roles, resolving and
// storing it on a miss.
//
// A cache fault — unreachable backend, undecodable entry — is logged and
// treated as a miss. Authorization must not fail because a cache did: the inner
// resolver is still authoritative and still reachable, so degrading to it is
// both correct and the only answer that keeps requests flowing.
//
// A fault is counted and recorded to the span, but deliberately does not set
// the span's status to error: the request succeeded. Marking it would make a
// cache blip indistinguishable from a denial in a trace search, which is
// exactly backwards — the whole point of the degradation is that the caller
// never notices. Look to the fault counters and the cache outcome attribute to
// notice on their behalf.
func (r *Resolver) PermissionsForRoles(ctx context.Context, roles ...string) (*authorization.PermissionSet, error) {
	if len(roles) == 0 {
		return authorization.NewPermissionSet(), nil
	}

	ctx, op := r.o11y.Begin(ctx, observability.WithValue(keys.AuthorizationRolesKey, roles))
	defer op.End()

	key, err := r.key(roles)
	if err != nil {
		return nil, op.Error(err, "building policy cache key")
	}

	// The key embeds the generation counter and every role name, which is
	// useful when reading a fault log and noise on every span.
	op.LogOnly("cache.key", key)

	cached, err := r.cache.Get(ctx, key)
	switch {
	case err == nil && cached != nil:
		op.SpanOnly(keys.AuthorizationCacheOutcomeKey, outcomeHit)

		return cached, nil
	case err != nil && !errors.Is(err, cache.ErrNotFound):
		r.readFaultsCounter.Add(ctx, 1)
		op.SpanOnly(keys.AuthorizationCacheOutcomeKey, outcomeFault)
		op.Logger().Error("reading cached policy resolution", err)
	default:
		op.SpanOnly(keys.AuthorizationCacheOutcomeKey, outcomeMiss)
	}

	set, err := r.inner.PermissionsForRoles(ctx, roles...)
	if err != nil {
		return nil, op.Error(err, "resolving uncached permissions for roles")
	}

	if err = r.cache.Set(ctx, key, set, cache.WithExpiry(r.ttl)); err != nil {
		// The answer is already correct; failing to memoize it is not a reason
		// to fail the caller. It is a reason to count it: a write that keeps
		// failing turns every request into a miss, which reads as a cache that
		// is merely cold rather than one that is broken.
		r.writeFaultsCounter.Add(ctx, 1)
		op.Logger().Error("caching policy resolution", err)
	}

	return set, nil
}

// Roles delegates without caching. It serves admin tooling rather than the
// request path, and it is exactly the call an operator makes to confirm an edit
// landed — answering it from a cache would show them the state they were trying
// to change.
//
// It is deliberately not traced. The call adds no behavior of its own, and the
// inner resolver traces itself, so a span here would only wrap another span
// with the same duration.
func (r *Resolver) Roles(ctx context.Context) ([]authorization.Role, error) {
	return r.inner.Roles(ctx)
}

// Invalidate drops the cached resolution for an exact set of roles.
//
// Unlike a read fault, a failure here is returned rather than degraded around:
// the caller asked for stale policy to stop being served, and silently not
// doing that would leave revoked authority in place.
func (r *Resolver) Invalidate(ctx context.Context, roles ...string) error {
	if len(roles) == 0 {
		return nil
	}

	ctx, op := r.o11y.Begin(ctx, observability.WithValue(keys.AuthorizationRolesKey, roles))
	defer op.End()

	key, err := r.key(roles)
	if err != nil {
		return op.Error(err, "building policy cache key")
	}

	if err = r.cache.Delete(ctx, key); err != nil {
		return op.Error(err, "invalidating cached policy resolution")
	}

	return nil
}

// InvalidateAll makes every entry this Resolver previously wrote unreachable.
//
// It bumps a generation counter held in memory, so it takes effect immediately
// in this process and not at all in others — the stale entries elsewhere expire
// on their TTL. Call it after a policy write so that the process that made the
// change stops serving what it just replaced; do not mistake it for a
// fleet-wide flush.
func (r *Resolver) InvalidateAll() {
	r.generation.Add(1)
}

// key builds the cache key for a role set: format version, generation, then the
// sorted role names. Sorting means the same roles in a different order share an
// entry.
//
// NUL is the separator because it cannot appear in any sane role name — but a
// name containing one would let {"a\x00b"} and {"a", "b"} produce the same key,
// and a cache-entry collision here serves one principal another's permissions.
// So it is checked rather than assumed, and a violating name is an error.
func (r *Resolver) key(roles []string) (string, error) {
	sorted := slices.Clone(roles)
	slices.Sort(sorted)

	for _, name := range sorted {
		if strings.ContainsRune(name, 0) {
			return "", platformerrors.Newf("role name %q contains a NUL byte", name)
		}
	}

	var b strings.Builder
	b.WriteString(keyFormatVersion)
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(r.generation.Load(), 10))
	b.WriteByte(':')
	b.WriteString(strings.Join(sorted, "\x00"))

	return b.String(), nil
}
