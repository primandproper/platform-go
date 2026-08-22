package metering

import (
	"context"
	"slices"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// MaxMeterNameLength bounds a meter name. Meter names travel into
// provider-side idempotency keys, and Stripe publishes a 255-byte limit on
// those; leaving room for the subject and the period is what keeps a long meter
// name from silently truncating a key into a collision with another one.
const MaxMeterNameLength = 64

// Registry is the set of meters an application counts and the quotas it enforces
// on them.
//
// A Registry is built during startup and read concurrently thereafter. It is not
// safe to register into one an Enforcer is already running against, and nothing
// here pretends otherwise: registration is a wiring-time activity, and a mutex
// would only make an ordering bug quieter.
type Registry struct {
	meters map[string]Meter
	quotas map[string]Quota
}

// NewRegistry builds an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		meters: map[string]Meter{},
		quotas: map[string]Quota{},
	}
}

// RegisterMeter adds a meter.
//
// Re-registering a name is an error rather than a replacement. The aggregation
// and period of a meter decide what every stored total for it means, so a silent
// overwrite would reinterpret history — the same rows, read as a different
// quantity, with nothing to say when the reading changed.
func (r *Registry) RegisterMeter(m Meter) error {
	if err := m.validate(); err != nil {
		return err
	}

	if _, exists := r.meters[m.Name]; exists {
		return platformerrors.Wrapf(ErrDuplicateMeter, "meter %q", m.Name)
	}

	r.meters[m.Name] = m

	return nil
}

// RegisterQuota adds a quota over an already-registered meter.
//
// The meter must exist first. A quota over an unknown meter is a typo in one of
// the two names, and accepting it would produce a limit that is never consulted
// because nothing ever records against the meter it names.
func (r *Registry) RegisterQuota(q Quota) error {
	m, ok := r.meters[q.Meter]
	if !ok {
		return platformerrors.Wrapf(ErrUnknownMeter, "quota for meter %q", q.Meter)
	}

	if err := q.validate(m); err != nil {
		return err
	}

	if _, exists := r.quotas[q.Meter]; exists {
		return platformerrors.Wrapf(ErrDuplicateQuota, "meter %q", q.Meter)
	}

	r.quotas[q.Meter] = q

	return nil
}

// Meter returns the meter registered under name.
func (r *Registry) Meter(name string) (Meter, bool) {
	m, ok := r.meters[name]

	return m, ok
}

// Quota returns the quota registered for a meter.
func (r *Registry) Quota(meter string) (Quota, bool) {
	q, ok := r.quotas[meter]

	return q, ok
}

// MeterNames returns the registered meter names, sorted.
func (r *Registry) MeterNames() []string {
	return sortedKeys(r.meters)
}

// QuotaMeters returns the meter names that have a quota, sorted.
func (r *Registry) QuotaMeters() []string {
	return sortedKeys(r.quotas)
}

// QuotaSource supplies a subject's quota for a meter at request time, for
// applications whose limits are per subject rather than per deployment.
//
// This is the seam plans live behind. A subject on the "pro" plan and one on
// "free" have different limits for the same meter, and that mapping is where
// every application's opinions are: trials, grandfathered pricing, the limit
// somebody bumped by hand for one customer in 2023. A library that modeled plans
// would be duplicating the billing provider's product catalog in a second place
// that can disagree with it, so it models none of them and asks instead.
//
// Returning ErrNoQuota means the subject has no limit on this meter, which the
// Enforcer reports rather than treating as unlimited — see ErrNoQuota.
type QuotaSource interface {
	QuotaFor(ctx context.Context, subject, meter string) (Quota, error)
}

// QuotaSourceFunc adapts a function to QuotaSource.
type QuotaSourceFunc func(ctx context.Context, subject, meter string) (Quota, error)

// QuotaFor implements QuotaSource.
func (f QuotaSourceFunc) QuotaFor(ctx context.Context, subject, meter string) (Quota, error) {
	return f(ctx, subject, meter)
}

var _ QuotaSource = (*RegistryQuotaSource)(nil)

// RegistryQuotaSource serves the Registry's static quotas to every subject. It
// is exported, and returned by NewRegistryQuotaSource, so a caller can depend on
// the source it built rather than on the QuotaSource seam.
type RegistryQuotaSource struct {
	registry *Registry
}

// NewRegistryQuotaSource returns the QuotaSource an Enforcer uses when no other
// is configured: every subject gets the quota registered for the meter.
//
// It is the right answer for a deployment with one set of limits — an internal
// service protecting a shared dependency — and the wrong one the moment two
// customers are supposed to be able to buy different amounts.
func NewRegistryQuotaSource(registry *Registry) *RegistryQuotaSource {
	return &RegistryQuotaSource{registry: registry}
}

// QuotaFor implements QuotaSource.
func (s *RegistryQuotaSource) QuotaFor(_ context.Context, _, meter string) (Quota, error) {
	if s.registry == nil {
		return Quota{}, ErrNilRegistry
	}

	q, ok := s.registry.Quota(meter)
	if !ok {
		return Quota{}, platformerrors.Wrapf(ErrNoQuota, "meter %q", meter)
	}

	return q, nil
}

// ProviderRef locates the provider-side object a subject's usage on a meter is
// reported against.
type ProviderRef struct {
	// CustomerID is the provider-side customer the usage is billed to — for
	// Stripe, the `cus_…` the meter's customer mapping resolves against.
	CustomerID string

	// MeterName is the provider-side meter the usage counts against — for Stripe,
	// the `event_name` of the billing meter.
	//
	// It is supplied rather than taken to be this package's own meter name. The
	// provider's meters are configured wherever pricing is owned, and the two
	// names drift apart the first time a plan is renamed. A mapper that wants
	// them to agree can return the meter it was passed.
	MeterName string
}

// ProviderMapper maps a subject and meter onto the provider-side handles usage is
// posted against.
//
// The mapping is application knowledge and cannot be anything else: it is the
// join between an internal account ID and a Stripe customer, and it changes
// whenever somebody upgrades a plan. A library that guessed it would post one
// customer's usage onto another's invoice, which is the single most expensive
// mistake available in this package.
//
// Returning an error wrapping ErrNoProviderRef means this subject does not bill
// for this meter — a free plan, an internal account, a meter kept for analytics
// only. The Flusher treats it as nothing to post rather than as a failure, and
// marks the usage flushed so it does not retry forever.
type ProviderMapper interface {
	ProviderRefFor(ctx context.Context, subject, meter string) (ProviderRef, error)
}

// ProviderMapperFunc adapts a function to ProviderMapper.
type ProviderMapperFunc func(ctx context.Context, subject, meter string) (ProviderRef, error)

// ProviderRefFor implements ProviderMapper.
func (f ProviderMapperFunc) ProviderRefFor(ctx context.Context, subject, meter string) (ProviderRef, error) {
	return f(ctx, subject, meter)
}

// sortedKeys projects a map's keys in sorted order.
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}
