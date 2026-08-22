package shredding

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// DefaultKeyTTL is how long an unwrapped data key stays in this process.
	//
	// It is the number a deployment promises a subject. "Erasure completes
	// within five minutes" is this constant, not a guess: until a cached key
	// expires, a replica that already holds it can still read ciphertext the
	// shred was meant to destroy.
	//
	// Five minutes is short enough to state out loud and long enough that a
	// request-per-second workload is not paying for an unwrap on every read.
	// WithBroadcaster usually collapses it to milliseconds; this is what remains
	// when the broadcast does not arrive.
	DefaultKeyTTL = 5 * time.Minute

	// DefaultMaxCachedKeys bounds how many plaintext data keys this process
	// holds at once.
	//
	// The bound is there for the same reason the TTL is. Every cached key is a
	// key a shred cannot reach yet, so an unbounded cache is an unbounded set of
	// subjects whose erasure has not finished — and, incidentally, a heap that
	// grows with the number of distinct subjects a replica has ever touched.
	DefaultMaxCachedKeys = 1024

	// dataKeyLength is the size of a per-subject data key. It matches
	// aes.KeyLength, because that is what consumes it.
	dataKeyLength = aes.KeyLength

	// wrapContext prefixes the associated data every wrap is bound to, so a
	// wrapped key cannot be replayed as some other kind of ciphertext against
	// the same root key.
	wrapContext = "shredding/subject-key/v1"
)

var _ Keys = (*KeyManager)(nil)

// KeyManager is the Keys implementation: a store of wrapped data keys, a
// wrapper that opens them, and a short-lived cache of the results. It is
// exported, and returned by NewKeys, so a caller can depend on the manager it
// built rather than on the Keys seam.
type KeyManager struct {
	store       Store
	wrapper     encryption.KeyWrapper
	clock       clock.Clock
	cache       *keyCache
	broadcaster Broadcaster
	o11y        observability.Observer

	// newCipher builds the per-subject Cipher. A field so that a test can
	// observe how often one is built without reaching into the cache, and so
	// that a deployment with a different AEAD has somewhere to put it.
	newCipher func(key []byte) (encryption.Cipher, error)
	// random is the data key source, always crypto/rand.Reader outside this
	// package's own tests.
	random io.Reader

	mintedCounter      metrics.Int64Counter
	shreddedCounter    metrics.Int64Counter
	unwrapCounter      metrics.Int64Counter
	cacheHitCounter    metrics.Int64Counter
	cacheMissCounter   metrics.Int64Counter
	broadcastCounter   metrics.Int64Counter
	broadcastErrCtr    metrics.Int64Counter
	invalidatedCounter metrics.Int64Counter
	cachedGauge        metrics.Int64Gauge

	// droppedAttrs and absentAttrs are the two readings invalidatedCounter
	// takes, built once because they are the same two values forever.
	droppedAttrs metric.MeasurementOption
	absentAttrs  metric.MeasurementOption

	// What the options wrote, kept only until the observer is built from it.
	// Read k.o11y.Logger() for the logger this Keys actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	ttl       time.Duration
	maxCached int
}

// NewKeys builds per-subject encryption over store, with wrapper protecting the
// data keys.
//
// wrapper is required and has no default. Wrapping with nothing would store data
// keys in the clear next to the ciphertext they open, which is the one
// configuration where every claim this package makes is false. Use
// cryptography/encryption/kms — gcp or aws where a KMS exists, local where
// nothing better does.
//
// Observability is optional and defaults to nothing.
func NewKeys(store Store, wrapper encryption.KeyWrapper, opts ...Option) (*KeyManager, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if wrapper == nil {
		return nil, ErrNilKeyWrapper
	}

	k := &KeyManager{
		store:     store,
		wrapper:   wrapper,
		clock:     clock.NewClock(),
		newCipher: newAESCipher,
		random:    rand.Reader,
		ttl:       DefaultKeyTTL,
		maxCached: DefaultMaxCachedKeys,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(k)
		}
	}

	k.o11y = observability.NewObserver(serviceName, k.logger, k.tracerProvider)
	k.cache = newKeyCache(k.clock, k.ttl, k.maxCached)

	if err := k.buildInstruments(); err != nil {
		return nil, err
	}

	return k, nil
}

// newAESCipher is the default newCipher. It is a named function rather than a
// closure around aes.NewCipher because that constructor returns *aes.Cipher:
// forwarding its results straight into an encryption.Cipher return would make a
// nil cipher a non-nil interface on the error path.
func newAESCipher(key []byte) (encryption.Cipher, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return c, nil
}

// buildInstruments creates every instrument at construction, so a broken meter
// fails the wiring rather than the first erasure.
func (k *KeyManager) buildInstruments() error {
	mp := metrics.EnsureMetricsProvider(k.metricsProvider)

	var err error

	if k.mintedCounter, err = mp.NewInt64Counter(serviceName + "_keys_minted"); err != nil {
		return platformerrors.Wrap(err, "creating shredding keys minted counter")
	}

	if k.shreddedCounter, err = mp.NewInt64Counter(serviceName + "_keys_shredded"); err != nil {
		return platformerrors.Wrap(err, "creating shredding keys shredded counter")
	}

	// The KMS line item. Cost at scale is call volume, not key count, and this
	// is the call.
	if k.unwrapCounter, err = mp.NewInt64Counter(serviceName + "_key_unwraps"); err != nil {
		return platformerrors.Wrap(err, "creating shredding key unwrap counter")
	}

	if k.cacheHitCounter, err = mp.NewInt64Counter(serviceName + "_cache_hits"); err != nil {
		return platformerrors.Wrap(err, "creating shredding cache hit counter")
	}

	if k.cacheMissCounter, err = mp.NewInt64Counter(serviceName + "_cache_misses"); err != nil {
		return platformerrors.Wrap(err, "creating shredding cache miss counter")
	}

	// The publishing half of the fleet-wide invalidation, and the number the
	// subscribing half's received counter is meant to be read against. A
	// deployment that broadcasts and never receives has wired the publisher and
	// forgotten the consumer, which no single counter can show.
	if k.broadcastCounter, err = mp.NewInt64Counter(serviceName + "_invalidations_broadcast"); err != nil {
		return platformerrors.Wrap(err, "creating shredding invalidations broadcast counter")
	}

	// Worth alerting on. A broadcast that is failing means erasure has quietly
	// gone back to completing on the TTL across the fleet, and nothing else says
	// so.
	if k.broadcastErrCtr, err = mp.NewInt64Counter(serviceName + "_invalidations_broadcast_failures"); err != nil {
		return platformerrors.Wrap(err, "creating shredding broadcast failure counter")
	}

	if k.invalidatedCounter, err = mp.NewInt64Counter(serviceName + "_invalidations_applied"); err != nil {
		return platformerrors.Wrap(err, "creating shredding invalidations applied counter")
	}

	if k.cachedGauge, err = mp.NewInt64Gauge(serviceName + "_cached_keys"); err != nil {
		return platformerrors.Wrap(err, "creating shredding cached keys gauge")
	}

	k.droppedAttrs = metric.WithAttributes(attribute.Bool(droppedKey, true))
	k.absentAttrs = metric.WithAttributes(attribute.Bool(droppedKey, false))

	return nil
}

func (k *KeyManager) Encrypt(ctx context.Context, subject Subject, plaintext, associatedData []byte) ([]byte, error) {
	ctx, op := k.o11y.Begin(ctx,
		observability.WithValue(subjectIDKey, subject.ID),
		observability.WithValue(subjectTypeKey, subject.Type),
	)
	defer op.End()

	cipher, err := k.cipherFor(ctx, op, subject, true)
	if err != nil {
		return nil, op.Error(err, "encrypting for shredding subject")
	}

	sealed, err := cipher.Seal(ctx, plaintext, associatedData)
	if err != nil {
		return nil, op.Error(err, "encrypting for shredding subject")
	}

	return sealed, nil
}

func (k *KeyManager) Decrypt(ctx context.Context, subject Subject, ciphertext, associatedData []byte) ([]byte, error) {
	ctx, op := k.o11y.Begin(ctx,
		observability.WithValue(subjectIDKey, subject.ID),
		observability.WithValue(subjectTypeKey, subject.Type),
	)
	defer op.End()

	// mint is false, and that asymmetry is the whole state machine. Reading is
	// not a reason to bring a key into existence: a subject with no key has no
	// ciphertext, so a read that reaches here is asking about something the keys
	// table cannot explain, and minting would answer it with a key that opens
	// nothing.
	cipher, err := k.cipherFor(ctx, op, subject, false)
	if err != nil {
		return nil, op.Error(err, "decrypting for shredding subject")
	}

	opened, err := cipher.Open(ctx, ciphertext, associatedData)
	if err != nil {
		return nil, op.Error(err, "decrypting for shredding subject")
	}

	return opened, nil
}

func (k *KeyManager) Shred(ctx context.Context, subject Subject) (Receipt, error) {
	ctx, op := k.o11y.Begin(ctx,
		observability.WithValue(subjectIDKey, subject.ID),
		observability.WithValue(subjectTypeKey, subject.Type),
	)
	defer op.End()

	if err := subject.validate(); err != nil {
		return Receipt{}, op.Error(err, "shredding subject key")
	}

	receipt, err := k.store.Shred(ctx, subject, k.clock.Now().UTC())
	if err != nil {
		return Receipt{}, op.Error(err, "shredding subject key")
	}

	// Locally first and unconditionally. This replica's cached copy is the one
	// this process can definitely reach, and it is also the one most likely to
	// be warm — the request that erased the subject probably read them first.
	k.cache.drop(subject)

	k.broadcast(ctx, op, subject)

	op.Set(destroyedKey, receipt.Destroyed).Set(shreddedAtKey, receipt.ShreddedAt)

	if receipt.Destroyed {
		k.shreddedCounter.Add(ctx, 1)
	}

	k.cachedGauge.Record(ctx, int64(k.cache.len()))

	return receipt, nil
}

func (k *KeyManager) Invalidate(ctx context.Context, subject Subject) {
	ctx, op := k.o11y.Begin(ctx,
		observability.WithValue(subjectIDKey, subject.ID),
		observability.WithValue(subjectTypeKey, subject.Type),
	)
	defer op.End()

	dropped := k.cache.drop(subject)

	// Whether the drop found anything is the whole point of recording this. An
	// invalidation that took a live key is a subject whose erasure finished on
	// the broadcast rather than on the TTL; one that found nothing arrived after
	// the key had expired, or at a replica that never read the subject. Only
	// this side of the bus can tell those apart, and a fleet where the first
	// number is always zero has a broadcast that is not arriving in time to
	// matter.
	attrs := k.absentAttrs
	if dropped {
		attrs = k.droppedAttrs
	}

	op.Set(droppedKey, dropped)
	k.invalidatedCounter.Add(ctx, 1, attrs)
	k.cachedGauge.Record(ctx, int64(k.cache.len()))
}

// broadcast tells the other replicas to drop the key, and does not fail the
// shred if it cannot.
//
// The destruction has already happened; the broadcast only decides whether the
// other replicas notice now or at their TTL. Returning an error here would fail
// an erasure request that succeeded, and the retry would re-run a destruction
// that is already done — trading a stated five-minute bound for a request that
// reports failure. The failure is counted and logged instead, which is what
// makes a bus that has silently stopped delivering visible.
func (k *KeyManager) broadcast(ctx context.Context, op observability.Operation, subject Subject) {
	if k.broadcaster == nil {
		return
	}

	// Counted as an attempt rather than as a success, so that the failure
	// counter is a subset of this one and the ratio between them is the
	// bus's error rate rather than something that has to be reconstructed.
	k.broadcastCounter.Add(ctx, 1)

	if err := k.broadcaster.Broadcast(ctx, subject); err != nil {
		k.broadcastErrCtr.Add(ctx, 1)
		op.Acknowledge(err, "broadcasting shredding invalidation")
	}
}

// cipherFor resolves the subject's Cipher, minting a data key when asked to and
// when the subject has none.
func (k *KeyManager) cipherFor(
	ctx context.Context,
	op observability.Operation,
	subject Subject,
	mint bool,
) (encryption.Cipher, error) {
	if err := subject.validate(); err != nil {
		return nil, err
	}

	if cipher, ok := k.cache.get(subject); ok {
		k.cacheHitCounter.Add(ctx, 1)
		op.Set(cacheHitKey, true)

		return cipher, nil
	}

	k.cacheMissCounter.Add(ctx, 1)
	op.Set(cacheHitKey, false)

	record, err := k.store.Load(ctx, subject)
	switch {
	case err == nil:
		return k.openRecord(ctx, op, record)
	case !errors.Is(err, ErrNoKey):
		return nil, err
	case !mint:
		return nil, err
	}

	return k.mint(ctx, op, subject)
}

// openRecord unwraps a live record into a usable Cipher.
func (k *KeyManager) openRecord(ctx context.Context, op observability.Operation, record *Record) (encryption.Cipher, error) {
	if record.Shredded() {
		op.Set(shreddedAtKey, *record.ShreddedAt)

		return nil, ErrSubjectShredded
	}

	if len(record.Wrapped) == 0 {
		return nil, ErrKeyMaterialMissing
	}

	k.unwrapCounter.Add(ctx, 1)

	material, err := k.wrapper.Unwrap(ctx, record.Wrapped, wrapAAD(record.Subject))
	if err != nil {
		return nil, platformerrors.Wrap(err, "unwrapping subject data key")
	}

	cipher, err := k.newCipher(material)
	if err != nil {
		return nil, platformerrors.Wrap(err, "building subject cipher")
	}

	k.cache.put(record.Subject, cipher)
	k.cachedGauge.Record(ctx, int64(k.cache.len()))

	return cipher, nil
}

// mint generates a data key for a subject that has none, and yields to whoever
// got there first.
//
// The losing side of that race throws its key away rather than keeping it: two
// live keys for one subject means a shred destroys one of them and leaves the
// ciphertext under the other perfectly readable, which is the failure this
// package exists to prevent and would be invisible until somebody audited an
// erasure.
func (k *KeyManager) mint(ctx context.Context, op observability.Operation, subject Subject) (encryption.Cipher, error) {
	material := make([]byte, dataKeyLength)
	if _, err := io.ReadFull(k.random, material); err != nil {
		return nil, platformerrors.Wrap(err, "generating subject data key")
	}

	wrapped, err := k.wrapper.Wrap(ctx, material, wrapAAD(subject))
	if err != nil {
		return nil, platformerrors.Wrap(err, "wrapping subject data key")
	}

	inserted, err := k.store.Insert(ctx, &Record{
		Subject:   subject,
		Wrapped:   wrapped,
		CreatedAt: k.clock.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}

	if !inserted {
		record, loadErr := k.store.Load(ctx, subject)
		if loadErr != nil {
			return nil, loadErr
		}

		return k.openRecord(ctx, op, record)
	}

	op.Set(mintedKey, true)
	k.mintedCounter.Add(ctx, 1)

	cipher, err := k.newCipher(material)
	if err != nil {
		return nil, platformerrors.Wrap(err, "building subject cipher")
	}

	k.cache.put(subject, cipher)
	k.cachedGauge.Record(ctx, int64(k.cache.len()))

	return cipher, nil
}

// wrapAAD binds a wrapped key to the subject it belongs to.
//
// Without it, a wrapped key lifted from one row into another unwraps perfectly
// well, and the subject whose row it was moved into reads somebody else's data.
// The version prefix keeps a wrapped key from being replayed as some other kind
// of ciphertext against the same root key.
func wrapAAD(subject Subject) []byte {
	aad := make([]byte, 0, len(wrapContext)+len(subject.Type)+len(subject.ID)+2)
	aad = append(aad, wrapContext...)
	aad = append(aad, 0)
	aad = append(aad, subject.Type...)
	aad = append(aad, 0)
	aad = append(aad, subject.ID...)

	return aad
}
