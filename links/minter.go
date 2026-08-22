package links

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	hashingsha256 "github.com/primandproper/platform-go/v13/cryptography/hashing/sha256"
	"github.com/primandproper/platform-go/v13/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/random"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Observability keys. Package-specific ones are namespaced; nothing here maps
// onto an observability/keys constant.
//
// There is deliberately no key for a token. A span exporter and a log sink are
// both durable storage owned by somebody other than this process, and a token
// written to either is a live credential sitting outside the one place this
// package is careful about. idKey carries the digest instead, which identifies
// the same link and redeems nothing.
const (
	idKey        = "links.id"
	actionKey    = "links.action"
	subjectKey   = "links.subject"
	expiresAtKey = "links.expires_at"
	stateKey     = "links.state"
	outcomeKey   = "outcome"
)

// Outcomes reported on links_redemptions. Every call to Redeem that resolves
// lands in exactly one of these.
const (
	outcomeRedeemed       = "redeemed"
	outcomeNotFound       = "not_found"
	outcomeAlreadyUsed    = "already_redeemed"
	outcomeExpired        = "expired"
	outcomeRevoked        = "revoked"
	outcomeInvalidToken   = "invalid_token"
	outcomeStoreUnhealthy = "store_error"
)

// Minter mints, inspects, redeems, and revokes action links.
//
// It is a concrete type rather than an interface: there is one implementation,
// and the seams worth swapping — the store, the locker, the hasher, the
// randomness — are already interfaces with their own mocks.
type Minter struct {
	store     cache.Cache[Record]
	locker    distributedlock.ScopedLocker
	hasher    hashing.Hasher
	generator random.Generator
	clock     clock.Clock
	o11y      observability.Observer

	actions map[Action]ActionPolicy

	mintCounter       metrics.Int64Counter
	redemptionCounter metrics.Int64Counter
	revocationCounter metrics.Int64Counter
	storeErrorCounter metrics.Int64Counter
	staleCounter      metrics.Int64Counter
	latencyHist       metrics.Float64Histogram

	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	keyPrefix      string
	retention      time.Duration
	tokenBytes     int
	maxTokenLength int
}

// NewMinter builds a Minter over a record store and a locker.
//
// The locker is required and has no default. Single use is enforced by reading
// a record and then writing it back consumed, and without mutual exclusion two
// requests carrying the same token both read "active" and both proceed — which
// is the entire failure this package exists to prevent, arriving silently and
// only under concurrency.
//
// At least one action must be registered; see WithAction.
func NewMinter(
	store cache.Cache[Record],
	locker distributedlock.ScopedLocker,
	opts ...Option,
) (*Minter, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if locker == nil {
		return nil, ErrNilLocker
	}

	o := &minterOptions{
		clock:          clock.NewClock(),
		hasher:         hashingsha256.NewSHA256Hasher(),
		generator:      random.NewGenerator(),
		retention:      DefaultRetention,
		tokenBytes:     DefaultTokenBytes,
		maxTokenLength: DefaultMaxTokenLength,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	if len(o.actions) == 0 {
		return nil, ErrNoActions
	}

	for action, policy := range o.actions {
		if err := policy.validate(action, o.allowInsecure); err != nil {
			return nil, err
		}
	}

	m := &Minter{
		store:           store,
		locker:          locker,
		hasher:          o.hasher,
		generator:       o.generator,
		clock:           o.clock,
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
		actions:         maps.Clone(o.actions),
		keyPrefix:       DefaultKeyPrefix,
		retention:       o.retention,
		tokenBytes:      o.tokenBytes,
		maxTokenLength:  o.maxTokenLength,
	}

	if o.keyPrefix != nil {
		m.keyPrefix = *o.keyPrefix
	}

	m.o11y = observability.NewObserver(serviceName, o.logger, m.tracerProvider)

	mp := metrics.EnsureMetricsProvider(m.metricsProvider)

	var err error
	if m.mintCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_minted", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating minted counter")
	}
	if m.redemptionCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_redemptions", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating redemptions counter")
	}
	if m.revocationCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_revocations", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating revocations counter")
	}
	if m.storeErrorCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_store_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating store errors counter")
	}
	if m.staleCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_stale_records", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating stale records counter")
	}
	if m.latencyHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	return m, nil
}

// Actions returns the actions this Minter can mint, for a caller that wants to
// assert its wiring or render a list of the flows a deployment supports.
func (m *Minter) Actions() []Action {
	actions := make([]Action, 0, len(m.actions))
	for action := range m.actions {
		actions = append(actions, action)
	}

	return actions
}

// Mint issues a single-use link for an action and a subject.
//
// The returned Link carries the URL to deliver and the ID to record. The token
// exists in exactly two places afterwards: in that URL, and in whatever the
// caller delivers it with. It is not in the store — the store holds its digest
// — and it is not recoverable, so a link that fails to deliver is reminted
// rather than looked up.
func (m *Minter) Mint(
	ctx context.Context,
	action Action,
	subject Subject,
	opts ...MintOption,
) (*Link, error) {
	ctx, op := m.o11y.Begin(ctx, observability.WithValues(map[string]any{
		actionKey:  string(action),
		subjectKey: string(subject),
	}))
	defer op.End()
	defer m.observeLatency(ctx, m.clock.Now())

	policy, ok := m.actions[action]
	if !ok {
		return nil, op.Error(platformerrors.Wrapf(ErrUnknownAction, "action %q", action), "resolving action policy")
	}

	if subject == "" {
		return nil, op.Error(ErrEmptySubject, "checking action link subject")
	}

	o := newMintOptions(opts)

	ttl := policy.TTL
	if o.ttl != nil {
		ttl = *o.ttl
	}

	token, err := m.generator.GenerateBase64EncodedString(ctx, m.tokenBytes)
	if err != nil {
		return nil, op.Error(err, "generating action link token")
	}

	now := m.clock.Now().UTC()
	expiresAt := now.Add(ttl)

	id := m.idFor(Token(token))
	op.Set(idKey, string(id)).Set(expiresAtKey, expiresAt)

	// The store outlives the link by the retention window on purpose. Record
	// expiry is what decides redeemability; the extra window is only so that a
	// late click is answered with ErrLinkExpired rather than with silence.
	if err = m.store.Set(ctx, m.storeKey(id), &Record{
		CreatedAt: now,
		ExpiresAt: expiresAt,
		Metadata:  o.metadata,
		Action:    action,
		Subject:   subject,
		Version:   recordVersion,
		State:     StateActive,
	}, cache.WithExpiry(ttl+m.retention)); err != nil {
		m.storeErrorCounter.Add(ctx, 1)

		return nil, op.Error(
			platformerrors.Wrap(platformerrors.Wrap(ErrStoreUnavailable, err.Error()), "storing action link"),
			"minting action link",
		)
	}

	m.mintCounter.Add(ctx, 1, metric.WithAttributes(attribute.String(actionKey, string(action))))

	return &Link{
		ExpiresAt: expiresAt,
		URL:       policy.expand(Token(token)),
		Token:     Token(token),
		ID:        id,
		Action:    action,
		Subject:   subject,
	}, nil
}

// Inspect reports what a token would redeem as, without consuming it.
//
// It is what a GET handler calls. A password-reset link lands on a form, and
// consuming the token to render that form burns it before the user has typed
// anything — which is also what happens when a mail scanner fetches the URL on
// its way to the inbox. Render from Inspect, consume from Redeem on the POST.
//
// Its answer is advisory and nothing may be granted on it alone. It takes no
// lock and makes no write, so between it and the Redeem that follows the link
// can be redeemed by someone else, revoked, or expire. Redeem re-checks
// everything Inspect checked; that check is the one that counts.
func (m *Minter) Inspect(ctx context.Context, token Token) (*Claims, error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()
	defer m.observeLatency(ctx, m.clock.Now())

	id, err := m.identify(token)
	if err != nil {
		return nil, op.Error(err, "checking action link token")
	}

	op.Set(idKey, string(id))

	record, found, err := m.load(ctx, op, m.storeKey(id))
	if err != nil {
		return nil, op.Error(err, "loading action link")
	}
	if !found {
		return nil, op.Error(ErrLinkNotFound, "inspecting action link")
	}

	if err = m.usable(record); err != nil {
		return nil, op.Error(err, "inspecting action link")
	}

	return record.claims(id), nil
}

// Redeem consumes a token and returns what it was bound to.
//
// A second call with the same token reports ErrLinkAlreadyRedeemed, and so does
// a concurrent one: the read and the consuming write happen under a lock on the
// link, so two simultaneous redemptions of one token cannot both see it active.
//
// It fails closed on the store, without a policy knob. Idempotency offers one
// because a duplicate charge can be cheaper than an outage; nothing comparable
// is true here. A link this package cannot mark as consumed must not be
// honored, because "honored anyway" means an account was handed to whoever
// asked, twice.
func (m *Minter) Redeem(ctx context.Context, token Token) (*Claims, error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()
	defer m.observeLatency(ctx, m.clock.Now())

	id, err := m.identify(token)
	if err != nil {
		m.countRedemption(ctx, "", outcomeInvalidToken)

		return nil, op.Error(err, "checking action link token")
	}

	op.Set(idKey, string(id))

	res, err := m.resolve(ctx, op, id, StateRedeemed)
	switch {
	case err != nil:
		m.countRedemption(ctx, "", outcomeStoreUnhealthy)

		return nil, op.Error(err, "redeeming action link")
	case res.err != nil:
		m.countRedemption(ctx, res.action, outcomeFor(res.err))

		return nil, op.Error(res.err, "redeeming action link")
	}

	op.Set(actionKey, string(res.action)).Set(subjectKey, string(res.claims.Subject))
	m.countRedemption(ctx, res.action, outcomeRedeemed)

	return res.claims, nil
}

// Revoke withdraws a link by its ID, so that a token still sitting in somebody's
// mailbox stops working.
//
// It takes an ID rather than a token because the server never has the token: it
// stored a digest, which is the point. The ID is what Mint returned and what the
// audit entry for that mint recorded, so revoking a link months later needs
// nothing secret to have been kept in the meantime — and "revoke every
// outstanding reset link for this account" is a query against that log followed
// by a call to this per result.
//
// Revoking an already-revoked link succeeds: the outcome asked for is the
// outcome already in place. Revoking a redeemed one reports
// ErrLinkAlreadyRedeemed, because there the caller asked to prevent something
// that has already happened, and an operator revoking after a suspected
// compromise needs to be told they were too late.
func (m *Minter) Revoke(ctx context.Context, id ID) error {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()
	defer m.observeLatency(ctx, m.clock.Now())

	if id == "" {
		return op.Error(ErrInvalidID, "checking action link ID")
	}

	op.Set(idKey, string(id))

	res, err := m.resolve(ctx, op, id, StateRevoked)
	switch {
	case err != nil:
		return op.Error(err, "revoking action link")
	case stderrors.Is(res.err, ErrLinkRevoked), stderrors.Is(res.err, ErrLinkExpired):
		// Already unusable in the way revocation was asking for. Reporting a
		// failure would push callers into ignoring the error from this method
		// entirely, which is where the redeemed case below would be lost too.
		return nil
	case res.err != nil:
		return op.Error(res.err, "revoking action link")
	}

	m.revocationCounter.Add(ctx, 1, metric.WithAttributes(attribute.String(actionKey, string(res.action))))

	return nil
}

// resolution is what resolve determined about a link.
//
// The link's own answer is a field rather than resolve's error return because
// the two are answered differently and must not be confused. A returned error
// means the store could not be read or written and nothing changed; err here
// means the store answered and the link is absent, spent, expired, or revoked —
// a fact about the link, not a failure of the call. action is known whenever a
// record was found, including on every one of those refusals, which is what
// keeps a metric labeled by action from going blank exactly when somebody is
// asking why one flow's links keep failing.
type resolution struct {
	claims *Claims
	err    error
	action Action
}

// resolve moves an active link into a terminal state under its lock, returning
// what it was bound to.
//
// The write is inside the lock rather than after it. A check that passes and a
// write that lands separately is exactly the interleaving that lets two
// redemptions of one token both succeed.
func (m *Minter) resolve(
	ctx context.Context,
	op observability.Operation,
	id ID,
	to State,
) (*resolution, error) {
	storeKey := m.storeKey(id)
	res := &resolution{}

	lockErr := m.locker.WithLock(ctx, m.lockKey(id), func(ctx context.Context) error {
		record, found, loadErr := m.load(ctx, op, storeKey)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			res.err = ErrLinkNotFound

			return nil
		}

		res.action = record.Action

		// Whatever the link says about itself is carried on res rather than
		// returned. This callback's error return belongs to the store, and
		// spending it on "already redeemed" would report the single-use
		// guarantee working as an outage.
		if res.err = m.usable(record); res.err == nil {
			// Copied rather than mutated in place: the memory cache provider
			// hands back the live pointer it holds, so writing through record
			// would edit the stored link before the write that is supposed to
			// commit the edit.
			resolved := *record
			resolved.State = to
			resolved.ResolvedAt = m.clock.Now().UTC()

			if setErr := m.store.Set(ctx, storeKey, &resolved, cache.WithExpiry(m.retention)); setErr != nil {
				return setErr
			}

			op.Set(stateKey, uint8(to))
			res.claims = record.claims(id)
		}

		return nil
	})
	if lockErr != nil {
		m.storeErrorCounter.Add(ctx, 1)

		return nil, platformerrors.Wrap(
			platformerrors.Wrap(ErrStoreUnavailable, lockErr.Error()),
			"consuming action link",
		)
	}

	return res, nil
}

// usable reports why a record cannot be acted on, or nil.
//
// Expiry is decided here against this package's clock rather than left to the
// store's own eviction. The store's expiry is set past the link's on purpose,
// and a cache that evicts late — or not at all, as the memory provider does for
// an entry nothing reads — must not be able to keep a credential alive past the
// moment it was supposed to die.
func (m *Minter) usable(record *Record) error {
	switch record.State {
	case StateRedeemed:
		return ErrLinkAlreadyRedeemed
	case StateRevoked:
		return ErrLinkRevoked
	case StateActive:
		if !m.clock.Now().UTC().Before(record.ExpiresAt) {
			return ErrLinkExpired
		}

		return nil
	default:
		// A state this binary does not know is treated the same as a shape it
		// cannot read: refuse. The alternative is honoring a link whose meaning
		// was written by something else.
		return ErrLinkNotFound
	}
}

// load reads a record, reporting whether one usable to this binary was found.
//
// A record written by a different shape of this package reads as absent. That
// invalidates outstanding links across a shape change, which is the safe
// direction — the unsafe one is decoding a credential's record with the wrong
// field meanings.
//
// Store failures are returned, never swallowed. A miss and an outage answer the
// same question with opposite consequences here, and cache.ErrNotFound is the
// only one of the two that means the link is not there.
func (m *Minter) load(
	ctx context.Context,
	op observability.Operation,
	storeKey string,
) (record *Record, found bool, err error) {
	record, err = m.store.Get(ctx, storeKey)
	switch {
	case err == nil:
	case stderrors.Is(err, cache.ErrNotFound):
		return nil, false, nil
	default:
		m.storeErrorCounter.Add(ctx, 1)

		return nil, false, platformerrors.Wrap(
			platformerrors.Wrap(ErrStoreUnavailable, err.Error()),
			"reading action link record",
		)
	}

	if record == nil {
		return nil, false, nil
	}

	if record.Version != recordVersion {
		m.staleCounter.Add(ctx, 1)
		op.Logger().
			WithValue("links.record_version", record.Version).
			Debug("ignoring action link record written by a different record version")

		return nil, false, nil
	}

	return record, true, nil
}

// identify turns a caller-supplied token into the ID it would be stored under.
//
// The length bound is the only shape this checks. Everything else is handled by
// the digest: whatever bytes arrive, the store is addressed by a hex digest of
// them, so a hostile token never reaches the store, the locker, or a log line
// as itself.
func (m *Minter) identify(token Token) (ID, error) {
	if token == "" {
		return "", platformerrors.Wrap(ErrInvalidToken, "empty token")
	}
	if m.maxTokenLength > 0 && len(token) > m.maxTokenLength {
		return "", platformerrors.Wrap(ErrInvalidToken, "token exceeds the maximum length")
	}

	return m.idFor(token), nil
}

// idFor digests a token into the handle it is stored and spoken about under.
func (m *Minter) idFor(token Token) ID {
	return ID(hashing.HexString(m.hasher, string(token)))
}

// outcomeFor names the redemption outcome a link's own answer corresponds to.
func outcomeFor(err error) string {
	switch {
	case stderrors.Is(err, ErrLinkAlreadyRedeemed):
		return outcomeAlreadyUsed
	case stderrors.Is(err, ErrLinkExpired):
		return outcomeExpired
	case stderrors.Is(err, ErrLinkRevoked):
		return outcomeRevoked
	default:
		return outcomeNotFound
	}
}

// countRedemption records one resolved redemption.
//
// The action is a label because the registry bounds it; the subject never is,
// because nothing bounds that. A per-user metric label is how a metrics backend
// is turned into an expensive and unqueryable copy of the audit log.
func (m *Minter) countRedemption(ctx context.Context, action Action, outcome string) {
	m.redemptionCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String(actionKey, string(action)),
		attribute.String(outcomeKey, outcome),
	))
}

// observeLatency records how long an operation took. It is deferred with the
// start time evaluated at the call, so each public method is one line.
func (m *Minter) observeLatency(ctx context.Context, start time.Time) {
	m.latencyHist.Record(ctx, float64(m.clock.Since(start).Milliseconds()))
}

// storeKey namespaces a link's ID for the record store.
func (m *Minter) storeKey(id ID) string {
	return m.keyPrefix + string(id)
}

// lockKey namespaces a link's ID for the locker. It is deliberately distinct
// from the store key: the two live in different systems, and a shared spelling
// invites the assumption that one can be derived from the other.
func (m *Minter) lockKey(id ID) string {
	return m.keyPrefix + "lock:" + string(id)
}
