/*
Package shreddingcfg assembles per-subject data keys from environment
configuration: the Store the keys live in, the Keys surface that uses them, and
the Broadcaster that tells the rest of the fleet when one is destroyed.

The root key is not configured here. It is an encryption.KeyWrapper — a KMS
client, or the local wrapper where no KMS exists — and which one a deployment
uses is Go wiring rather than a string in the environment. It is passed
explicitly to NewKeys.

Nor is the database. NewStore takes a database.Client, and which client that is
carries the whole weight of this feature: the keys table needs a shorter backup
retention than the data it protects, or a restore hands back the keys a shred
destroyed. Handing this the same client as everything else is supported and is
usually the wrong answer. See the shredding package documentation.
*/
package shreddingcfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a shredding Store, Keys, and Broadcaster.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the subject key table. It must match the prefix the
	// migrations were rendered with. Defaults to
	// shredding.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// InvalidationTopic is the topic a shred is announced on, for the
	// deployments that build a Broadcaster. Defaults to
	// shredding.DefaultInvalidationTopic.
	InvalidationTopic string `env:"INVALIDATION_TOPIC" json:"invalidationTopic,omitempty" yaml:"invalidationTopic,omitempty"`

	// KeyTTL bounds how long an unwrapped data key stays in a process, and
	// therefore how long after a shred a replica can still read what that key
	// protected. Defaults to shredding.DefaultKeyTTL.
	//
	// This is the number a deployment promises a subject when it says erasure
	// completes within N minutes. Treat a change to it as a change to that
	// promise rather than as cache tuning.
	KeyTTL time.Duration `env:"KEY_TTL" json:"keyTTL,omitempty" yaml:"keyTTL,omitempty"`

	// MaxCachedKeys bounds how many plaintext data keys a process holds at
	// once. Defaults to shredding.DefaultMaxCachedKeys.
	MaxCachedKeys int `env:"MAX_CACHED_KEYS" json:"maxCachedKeys,omitempty" yaml:"maxCachedKeys,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in what was left unset.
func (cfg *Config) EnsureDefaults() {
	if cfg == nil {
		return
	}

	if cfg.TablePrefix == "" {
		cfg.TablePrefix = shredding.DefaultTablePrefix
	}

	if cfg.InvalidationTopic == "" {
		cfg.InvalidationTopic = shredding.DefaultInvalidationTopic
	}

	if cfg.KeyTTL == 0 {
		cfg.KeyTTL = shredding.DefaultKeyTTL
	}

	if cfg.MaxCachedKeys == 0 {
		cfg.MaxCachedKeys = shredding.DefaultMaxCachedKeys
	}
}

// ValidateWithContext validates the config.
//
// KeyTTL has no upper bound here, because there is no number this package can
// declare too long without knowing what a deployment has told its subjects. It
// is negative values that are rejected — those would disable the cache while
// reading as though they configured it.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.KeyTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.MaxCachedKeys, validation.Min(0)),
		validation.Field(&cfg.InvalidationTopic, validation.Required),
	)
}

// prepare nil-checks, defaults, and validates in that order: an unset field
// with a documented default is not a validation failure.
func (cfg *Config) prepare(ctx context.Context) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return errors.Wrap(err, "validating shredding config")
	}

	return nil
}

// NewStore builds the store the wrapped data keys live in.
//
// client is the one decision in this package that cannot be undone later.
// Pointing it at the database holding the data these keys protect means a
// restore of that database's snapshot resurrects keys that were shredded since,
// and everything they opened with them.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *shredding.SQLStore into a
// non-nil shredding.Store on the error path, and a caller testing the result against
// nil would find a store that panics on first use.
func NewStore(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (shredding.Store, error) {
	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	storeOpts := []shredding.SQLStoreOption{
		shredding.WithTablePrefix(cfg.TablePrefix),
		shredding.WithStoreLogger(o.logger),
		shredding.WithStoreTracerProvider(o.tracerProvider),
		shredding.WithStoreMetricsProvider(o.metricsProvider),
	}

	store, storeErr := shredding.NewSQLStore(client, append(storeOpts, o.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

// NewKeys builds the per-subject encryption surface.
//
// wrapper is the root key, and it is required: see shredding.NewKeys. A
// deployment with a KMS should pass cryptography/encryption/kms/gcp or /aws; one
// without should pass /local and know what it gave up.
//
// The Broadcaster is not wired here. It needs a publisher, which is a dependency
// this constructor has no way to invent, so a deployment that wants one builds
// it with NewBroadcaster and passes it through WithKeysOptions — or lets the DI
// registration do it.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *shredding.KeyManager into a
// non-nil shredding.Keys on the error path, and a caller testing the result against
// nil would find a Keys that panics on first use.
func NewKeys(
	ctx context.Context,
	cfg *Config,
	store shredding.Store,
	wrapper encryption.KeyWrapper,
	opts ...Option,
) (shredding.Keys, error) {
	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	keysOpts := []shredding.Option{
		shredding.WithKeyTTL(cfg.KeyTTL),
		shredding.WithMaxCachedKeys(cfg.MaxCachedKeys),
		shredding.WithLogger(o.logger),
		shredding.WithTracerProvider(o.tracerProvider),
		shredding.WithMetricsProvider(o.metricsProvider),
	}

	k, keysErr := shredding.NewKeys(store, wrapper, append(keysOpts, o.keys...)...)
	if keysErr != nil {
		return nil, keysErr
	}

	return k, nil
}

// NewBroadcaster builds the shred announcement over the configured topic.
//
// The subscribing half is NewInvalidationConsumer, over the same topic. A
// deployment that publishes and never subscribes has a counter that says
// invalidations are being sent and nothing acting on them, which is the worst of
// the two configurations.
//
// It takes no Option, alone among this package's constructors. A Broadcaster is
// a publish call and a topic name; it has nothing to log, trace, or count that
// the publisher underneath it — or the Keys that counts its calls — does not
// already.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *shredding.QueueBroadcaster into a
// non-nil shredding.Broadcaster on the error path, and a caller testing the result against
// nil would find a broadcaster that panics on first use.
func NewBroadcaster(
	ctx context.Context,
	cfg *Config,
	provider messagequeue.PublisherProvider,
) (shredding.Broadcaster, error) {
	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	if provider == nil {
		return nil, errors.Wrap(errors.ErrNilInputParameter, "nil publisher provider")
	}

	publisher, err := provider.NewPublisher(ctx, cfg.InvalidationTopic)
	if err != nil {
		return nil, errors.Wrapf(err, "creating shredding invalidation publisher for topic %q", cfg.InvalidationTopic)
	}

	b, broadcasterErr := shredding.NewQueueBroadcaster(publisher)
	if broadcasterErr != nil {
		return nil, broadcasterErr
	}

	return b, nil
}

// NewInvalidationConsumer builds the subscribing half: the consumer that hears
// another replica's shred and drops this one's cached copy of the key.
//
// invalidator is normally the shredding.Keys this process encrypts through.
//
// It returns the consumer without running it. Consuming needs a goroutine and a
// channel to report errors on, and a constructor that started one would own a
// goroutine its caller cannot see:
//
//	consumer, err := shreddingcfg.NewInvalidationConsumer(ctx, cfg, consumers, keys, shreddingcfg.WithPillars(pillars))
//	// ...
//	go consumer.Consume(ctx, errs)
//
// The observability options matter more here than anywhere else in this package,
// because this is the end of the broadcast with nothing else watching it. See
// shredding.NewInvalidationHandler for what the instruments say.
func NewInvalidationConsumer(
	ctx context.Context,
	cfg *Config,
	provider messagequeue.ConsumerProvider,
	invalidator shredding.Invalidator,
	opts ...Option,
) (messagequeue.Consumer, error) {
	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	if provider == nil {
		return nil, errors.Wrap(errors.ErrNilInputParameter, "nil consumer provider")
	}

	o := newOptions(opts)

	handler, err := shredding.NewInvalidationHandler(invalidator,
		shredding.WithInvalidationLogger(o.logger),
		shredding.WithInvalidationTracerProvider(o.tracerProvider),
		shredding.WithInvalidationMetricsProvider(o.metricsProvider),
	)
	if err != nil {
		return nil, err
	}

	consumer, err := provider.NewConsumer(ctx, cfg.InvalidationTopic, handler)
	if err != nil {
		return nil, errors.Wrapf(err, "creating shredding invalidation consumer for topic %q", cfg.InvalidationTopic)
	}

	return consumer, nil
}
