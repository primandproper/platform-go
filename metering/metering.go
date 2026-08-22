package metering

import (
	"context"
	"math"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/plainname"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "metering"

// Observability keys for this package's spans and log fields. Declared once so a
// field set on a span and the same field logged beside it cannot drift, and so
// the metering. prefix is applied uniformly — an un-namespaced attribute name
// collides with every other component writing to the same trace.
//
// Dimensions are deliberately absent. They are application-chosen keys and
// values — model names, endpoints, regions, and whatever else a consumer decides
// to break usage down by — and a span exporter is not the place to discover that
// somebody started dimensioning by email address.
const (
	subjectKey     = "metering.subject"
	meterKey       = "metering.meter"
	quantityKey    = "metering.quantity"
	usedKey        = "metering.used"
	limitKey       = "metering.limit"
	overageKey     = "metering.overage"
	allowedKey     = "metering.allowed"
	behaviorKey    = "metering.behavior"
	periodStartKey = "metering.period_start"
	periodEndKey   = "metering.period_end"
	aggregationKey = "metering.aggregation"
	acceptedKey    = "metering.accepted"
	duplicateKey   = "metering.duplicates"
	batchSizeKey   = "metering.batch_size"
	staleKey       = "metering.stale"
	cacheHitKey    = "metering.cache_hit"
	productKey     = "metering.product"
	entitledKey    = "metering.entitled"

	// Store-layer keys. The database client traces the statement, but with the
	// SQL text suppressed by default — so without these a trace shows an
	// anonymous query span and no indication of whose usage it was about.
	storeOpKey      = "metering.store_operation"
	rowsAffectedKey = "metering.rows_affected"
	resultCountKey  = "metering.result_count"
	reapedKey       = "metering.reaped"
	sequenceKey     = "metering.flush_sequence"
	deltaKey        = "metering.flush_delta"
	flushedKey      = "metering.flushed"
	attemptsKey     = "metering.attempts"
	terminalKey     = "metering.terminal"
)

var (
	// ErrNilStore indicates a nil Store. It wraps errors.ErrNilInputParameter, so
	// a caller may check either.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil metering store")

	// ErrNilRegistry indicates a nil *Registry. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilRegistry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil metering registry")

	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client")

	// ErrNilExecutor indicates a Store method that runs in the caller's
	// transaction was called without one.
	ErrNilExecutor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil query executor")

	// ErrUnknownMeter indicates usage or a quota naming a meter that was never
	// registered.
	//
	// It is an error rather than an implicit registration. A meter's aggregation
	// decides how its numbers combine, and a meter conjured from its first usage
	// record would have to guess — so a typo in a meter name would silently open
	// a second, unbilled account of the same thing.
	ErrUnknownMeter = platformerrors.New("unknown meter")

	// ErrDuplicateMeter indicates two registrations under one meter name.
	ErrDuplicateMeter = platformerrors.New("duplicate meter registration")

	// ErrDuplicateQuota indicates two quotas registered for one meter.
	ErrDuplicateQuota = platformerrors.New("duplicate quota registration")

	// ErrInvalidMeterName indicates a meter name that is empty or is not a plain
	// identifier. Names appear in metric attributes and in provider-side
	// idempotency keys, so they are restricted rather than escaped.
	ErrInvalidMeterName = platformerrors.New("invalid meter name")

	// ErrUnsupportedAggregation indicates an aggregation this package does not
	// implement — see Aggregation.Supported.
	ErrUnsupportedAggregation = platformerrors.New("unsupported meter aggregation")

	// ErrPeriodMismatch indicates a quota whose Period differs from its meter's.
	//
	// A quota over a window the meter does not bucket by cannot be answered
	// without summing across buckets, which is a table scan on the read path this
	// package exists to keep cheap. The two are required to agree, at
	// registration, rather than producing a quietly expensive Check.
	ErrPeriodMismatch = platformerrors.New("quota period does not match its meter's period")

	// ErrUnknownPeriod indicates a Period outside the set this package resolves.
	ErrUnknownPeriod = platformerrors.New("unknown metering period")

	// ErrNoBillingPeriodResolver indicates PeriodBillingPeriod was used without a
	// PeriodResolver that can answer it.
	//
	// The library refuses rather than guessing. When a subject's billing period
	// starts is a fact held by the billing provider — anniversary dates, proration,
	// trials, plan changes mid-month — and a calendar month assumed in its place
	// would bill the right total against the wrong invoice.
	ErrNoBillingPeriodResolver = platformerrors.New("no metering billing period resolver configured")

	// ErrEmptySubject indicates usage with no subject. Usage that belongs to
	// nobody cannot be invoiced and cannot be enforced, so it is refused at the
	// boundary rather than accumulating under the empty string.
	ErrEmptySubject = platformerrors.New("empty metering subject")

	// ErrEmptyIdempotencyKey indicates usage with no idempotency key.
	//
	// It is required, not optional. Every ingest path this package has — an HTTP
	// handler behind a client that retries, a queue consumer that redelivers — can
	// present the same usage twice, and usage that can be double-counted is usage
	// that produces wrong invoices. See the package documentation on choosing one.
	ErrEmptyIdempotencyKey = platformerrors.New("empty metering idempotency key")

	// ErrIdempotencyKeyTooLong indicates usage whose idempotency key exceeds
	// MaxIdempotencyKeyLength.
	//
	// Distinct from ErrEmptyIdempotencyKey because the two are different bugs in
	// the caller and have different fixes: one forgot to send a key, the other
	// is deriving one from something too long to store — a request body, a URL —
	// and needs to hash it instead. A caller matching the empty sentinel to tell
	// its own client "you must supply an idempotency key" would say that to a
	// client that supplied one.
	ErrIdempotencyKeyTooLong = platformerrors.New("metering idempotency key too long")

	// ErrNegativeQuantity indicates usage with a quantity below zero.
	//
	// Negative usage is a credit, and credits are a billing concept the provider
	// owns: a refund has a reason, a tax treatment, and an audit trail that none
	// of this package's aggregates can carry. Correcting an over-count is done by
	// issuing a credit at the provider, not by metering a negative number.
	ErrNegativeQuantity = platformerrors.New("negative metering quantity")

	// ErrNoQuota indicates an Enforcer call for a meter with no registered quota.
	//
	// Unmetered is not the same as unlimited, and this package will not pretend
	// otherwise. A caller that wants "allow everything on this meter" registers a
	// quota saying so — BehaviorAllowOverage, or a limit nobody reaches — and the
	// decision is then visible in the registry instead of implied by an absence.
	ErrNoQuota = platformerrors.New("no quota registered for meter")

	// ErrNilEntitlementReader indicates a PlanLimitSource built without an
	// EntitlementReader. It wraps errors.ErrNilInputParameter, so a caller may
	// check either.
	ErrNilEntitlementReader = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil metering entitlement reader")

	// ErrInvalidPlanLimits indicates a PlanLimits entry that cannot be served: a
	// behavior that is not one of this package's, or a limit below zero.
	//
	// It is refused at construction rather than at the first request. A limits
	// table is wiring, and the failure it produces on the request path — an
	// endpoint that refuses a customer, or a limit nothing ever applies — is one
	// nobody looks for until a bill is wrong.
	ErrInvalidPlanLimits = platformerrors.New("invalid metering plan limits")

	// ErrNilProviderMapper indicates a Flusher built without a ProviderMapper. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilProviderMapper = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil metering provider mapper")

	// ErrNilUsageReporter indicates a Flusher built without a
	// capitalism.UsageReporter. It wraps errors.ErrNilInputParameter, so a caller
	// may check either.
	ErrNilUsageReporter = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil metering usage reporter")

	// ErrNoProviderRef indicates a ProviderMapper that has no provider-side handle
	// for a subject and meter. It is not a failure: a subject on a plan that does
	// not bill for this meter is the ordinary case, and the flusher treats it as
	// "nothing to post" rather than an error to retry forever.
	ErrNoProviderRef = platformerrors.New("no provider reference for subject and meter")
)

// Aggregation says how a meter's usage records combine into the number that gets
// enforced and invoiced.
type Aggregation string

const (
	// AggregationSum adds every record in the period. The additive default:
	// API requests, LLM tokens, emails sent.
	AggregationSum Aggregation = "sum"

	// AggregationMax keeps the largest record in the period. What "peak
	// concurrent connections" or "high-water storage" means, and the reason
	// storage cannot simply be summed — a gigabyte held all month is one
	// gigabyte, not thirty.
	AggregationMax Aggregation = "max"

	// AggregationLast keeps the most recent record in the period, ordered by
	// Usage.OccurredAt. What a gauge means: seats provisioned, bytes stored right
	// now. A record that arrives out of order does not displace a newer one.
	AggregationLast Aggregation = "last"

	// AggregationUniqueCount counts distinct values within the period — monthly
	// active users, distinct seats.
	//
	// It is named but not implemented. Every other aggregation folds a record
	// into a single integer as it arrives; this one has to remember which values
	// it has already seen, which is a set or a HyperLogLog per period rather than
	// a column. Registering a meter with it is refused at registration rather
	// than silently treated as a sum, which is the answer that would look right
	// on a dashboard and be wrong on an invoice.
	AggregationUniqueCount Aggregation = "unique_count"
)

// Valid reports whether a is one of this package's named aggregations.
func (a Aggregation) Valid() bool {
	switch a {
	case AggregationSum, AggregationMax, AggregationLast, AggregationUniqueCount:
		return true
	default:
		return false
	}
}

// Supported reports whether a is an aggregation this package implements. See
// AggregationUniqueCount for the one that is named and is not.
func (a Aggregation) Supported() bool {
	switch a {
	case AggregationSum, AggregationMax, AggregationLast:
		return true
	case AggregationUniqueCount:
		return false
	default:
		return false
	}
}

// Fold applies the aggregation to a running total and an arriving record.
//
// It is the in-process counterpart of what the store's UPDATE does in SQL, used
// by the enforcer to answer "what would this consume take the total to" without a
// round trip. The two must agree; the store tests assert that they do.
//
// newer says whether the arriving record is at or after the one the total was
// last folded from, which is the only thing AggregationLast needs to know.
func (a Aggregation) Fold(total, quantity int64, newer bool) int64 {
	switch a {
	case AggregationMax:
		return max(total, quantity)
	case AggregationLast:
		if !newer {
			return total
		}

		return quantity
	case AggregationSum:
		return total + quantity
	case AggregationUniqueCount:
		return total
	default:
		return total
	}
}

// Meter is one thing being counted.
type Meter struct {
	// Name identifies the meter — "api_requests", "storage_bytes",
	// "llm_tokens". It appears in metric attributes and in the idempotency keys
	// sent to the billing provider, so it must be a plain identifier and must be
	// stable: renaming a meter starts a new, empty count, and the old one stops
	// being flushed.
	Name string

	// Unit is what one unit of Quantity is — "requests", "bytes", "tokens". It is
	// documentation and telemetry, never arithmetic: this package does no unit
	// conversion, and a meter that changes its unit mid-period silently changes
	// what its total means.
	Unit string

	// Aggregation says how records combine. Required.
	Aggregation Aggregation

	// Period is the window usage accumulates in, and the bucket the durable
	// total is keyed by. Required.
	Period Period

	// Staleness bounds how out of date Enforcer.Check may be for this meter.
	// Zero takes the enforcer's configured default.
	//
	// It is per-meter because the right answer differs by what the meter guards.
	// A quota on a $0.0001 API call can tolerate a minute of staleness — the
	// worst case is a few calls over the line. A quota on something expensive
	// wants seconds, or wants Consume instead.
	Staleness time.Duration
}

// validate reports whether the meter can be registered.
func (m Meter) validate() error {
	if !validMeterName(m.Name) {
		return platformerrors.Wrapf(ErrInvalidMeterName, "meter %q", m.Name)
	}

	if !m.Aggregation.Valid() {
		return platformerrors.Wrapf(ErrUnsupportedAggregation, "meter %q aggregation %q", m.Name, m.Aggregation)
	}

	if !m.Aggregation.Supported() {
		return platformerrors.Wrapf(ErrUnsupportedAggregation, "meter %q aggregation %q", m.Name, m.Aggregation)
	}

	if !m.Period.Valid() {
		return platformerrors.Wrapf(ErrUnknownPeriod, "meter %q period %q", m.Name, m.Period)
	}

	return nil
}

// validMeterName reports whether a name is a plain identifier.
//
// A meter name travels into a provider-side idempotency key and into metric
// attribute values, which is the rule internal/plainname states.
func validMeterName(name string) bool {
	return plainname.Valid(name, MaxMeterNameLength)
}

// Usage is one record of something having been consumed.
type Usage struct {
	// OccurredAt is when the usage happened, which decides the period it lands
	// in. Zero means "now", read from the recorder's clock.
	//
	// It is the event's time and not the ingest time on purpose: a queue that
	// redelivers an hour later must still file the usage in the period it
	// happened in, or the last hour of a billing period would leak into the next
	// one every month.
	OccurredAt time.Time

	// Dimensions break usage down — model, region, endpoint. They are stored
	// against the event for later analysis and are deliberately not part of
	// enforcement or of the aggregate key.
	//
	// Dimensioned quotas are a different data structure and a much larger one:
	// the number of totals to keep becomes the product of every dimension's
	// cardinality, and a dimension whose values come from user input has no
	// bound. A caller that needs to enforce per-model limits registers a meter
	// per model, where the cardinality is a decision somebody made on purpose.
	Dimensions map[string]string

	// IdempotencyKey dedupes at the ingest boundary. Required. A retried HTTP
	// request or a redelivered queue message carrying the same key is counted
	// once; see the package documentation on choosing one.
	IdempotencyKey string

	// Subject is the account or tenant being billed. Required.
	Subject string

	// Meter names the registered meter this record belongs to. Required.
	Meter string

	// Quantity is how much was consumed, in the meter's Unit. It must not be
	// negative — see ErrNegativeQuantity.
	Quantity int64
}

// validate reports whether the usage record can be ingested. It does not check
// the meter's existence, which is the registry's job.
func (u *Usage) validate() error {
	if u.Subject == "" {
		return ErrEmptySubject
	}

	if u.Meter == "" {
		return platformerrors.Wrap(ErrInvalidMeterName, "empty meter name")
	}

	if u.IdempotencyKey == "" {
		return platformerrors.Wrapf(ErrEmptyIdempotencyKey, "meter %q subject %q", u.Meter, u.Subject)
	}

	if len(u.IdempotencyKey) > MaxIdempotencyKeyLength {
		return platformerrors.Wrapf(ErrIdempotencyKeyTooLong, "idempotency key exceeds %d bytes", MaxIdempotencyKeyLength)
	}

	if u.Quantity < 0 {
		return platformerrors.Wrapf(ErrNegativeQuantity, "meter %q quantity %d", u.Meter, u.Quantity)
	}

	return nil
}

// QuotaBehavior says what happens when a subject reaches a quota's limit.
type QuotaBehavior string

const (
	// BehaviorBlock refuses usage past the limit: Decision.Allowed goes false and
	// the caller is expected to turn that into a 429 or a 402.
	BehaviorBlock QuotaBehavior = "block"

	// BehaviorWarn allows and records usage past the limit, reporting the overage
	// so a caller can surface it. What a plan uses during a grace period, and
	// during the fortnight after somebody upgrades their limits and before the
	// billing system catches up.
	BehaviorWarn QuotaBehavior = "warn"

	// BehaviorAllowOverage allows and records usage past the limit and says
	// nothing is wrong: Decision.Allowed stays true and Decision.Overage carries
	// the excess, which is the number the overage line on the invoice is computed
	// from.
	//
	// It is a first-class behavior rather than an error path because it is how
	// most usage billing actually works. A limit is where the price changes, not
	// where the service stops.
	BehaviorAllowOverage QuotaBehavior = "allow_overage"
)

// Valid reports whether b is one of this package's behaviors.
func (b QuotaBehavior) Valid() bool {
	switch b {
	case BehaviorBlock, BehaviorWarn, BehaviorAllowOverage:
		return true
	default:
		return false
	}
}

// records reports whether usage is written through even though it is over the
// limit. Only BehaviorBlock refuses; the other two count what happened, because
// usage a customer had is usage a customer had.
func (b QuotaBehavior) records() bool {
	return b != BehaviorBlock
}

// Quota is a limit on a meter.
//
// It carries no notion of a plan. Which plan a subject is on, what that plan
// entitles them to, and when it changes are exactly the questions a billing
// provider's product catalog already answers, and modeling them here would be
// duplicating that catalog in a second place that can disagree with it. An
// application maps its plans to quotas and supplies them — statically through the
// Registry, or per subject through a QuotaSource.
type Quota struct {
	// Meter names the registered meter this limits. Required.
	Meter string

	// Behavior says what happens at the limit. Required.
	Behavior QuotaBehavior

	// Period must equal the meter's period. See ErrPeriodMismatch.
	Period Period

	// Limit is the quantity allowed per period, in the meter's Unit. A limit of
	// zero means no usage is allowed, which is a real configuration — a feature
	// switched off for a plan tier — and not a synonym for unlimited.
	Limit int64
}

// Unlimited is the Quota.Limit for a meter nothing constrains, paired with
// BehaviorAllowOverage.
//
// It is a spelling rather than a sentinel: nothing in this package special-cases
// it, and the enforcer does the same arithmetic over it that it does over any
// other number. That is deliberate. A value meaning "do not enforce" is a value
// every enforcement path has to remember to check, and the day one of them
// forgets is the day a customer is refused for exceeding infinity.
//
// It is named because the alternatives a caller reaches for are all wrong in
// ways that are quiet. It is not the absence of a quota, which this package
// reports as ErrNoQuota — unmetered and unlimited are different facts, and
// reading one as the other is how a meter nobody has configured becomes a meter
// nobody is charged for. It is not zero, which means no usage is allowed and is
// a real configuration for a feature switched off on a tier. And it is not a
// large round number somebody picked, which is a limit a customer can eventually
// reach.
const Unlimited int64 = math.MaxInt64

// validate reports whether the quota can be registered against the given meter.
func (q Quota) validate(m Meter) error {
	if !q.Behavior.Valid() {
		return platformerrors.Newf("invalid quota behavior %q for meter %q", q.Behavior, q.Meter)
	}

	if q.Limit < 0 {
		return platformerrors.Newf("negative quota limit %d for meter %q", q.Limit, q.Meter)
	}

	if q.Period != m.Period {
		return platformerrors.Wrapf(ErrPeriodMismatch, "meter %q has period %q, quota has %q", m.Name, m.Period, q.Period)
	}

	return nil
}

// Decision is the answer to "may this subject consume this much".
type Decision struct {
	// ResetsAt is when the period ends and Used returns to zero. It is what a
	// caller puts in a Retry-After or an X-RateLimit-Reset header.
	ResetsAt time.Time

	// Meter is the meter the decision is about.
	Meter string

	// Behavior is the quota behavior that produced this decision, so a caller can
	// tell an allowed-because-under-limit from an allowed-because-overage-is-fine
	// without consulting the registry again.
	Behavior QuotaBehavior

	// Used is the total for the period, including this call's quantity when the
	// decision came from Consume and the usage was recorded. It is the number to
	// show a customer.
	Used int64

	// Limit is the quota's limit for the period.
	Limit int64

	// Overage is how far Used is past Limit, or zero when it is not. It is the
	// quantity an overage price is applied to.
	Overage int64

	// Allowed says whether the caller may proceed.
	Allowed bool

	// Stale says the decision was served from cache and may be behind the durable
	// total by up to the meter's staleness budget. Always false for Consume.
	//
	// It is on the decision rather than only in a metric because it is the one
	// thing a caller might want to act on: a request worth a fraction of a cent
	// proceeds on a stale allow, and one worth a dollar can decide to pay for a
	// Consume instead.
	Stale bool

	// Duplicate says the usage was already counted under this idempotency key, so
	// this call recorded nothing. The decision still reports the true current
	// total, which is what a retried request should see.
	Duplicate bool
}

// Recorder ingests usage.
//
// It is the write path and says nothing about limits. A deployment that meters
// for billing and enforces nothing uses only this; enforcement is Enforcer, and
// the two are separate interfaces because most call sites want exactly one of
// them.
type Recorder interface {
	// Record ingests usage records, at most once per idempotency key.
	//
	// It is variadic and batched because the ingest path for anything worth
	// metering is high-volume: an LLM proxy records tokens on every completion,
	// and a round trip per record would make metering the cost of the thing being
	// metered.
	//
	// A batch is not atomic across records. Each record is independently deduped
	// and folded, so a batch containing one already-seen record still records the
	// rest — which is the behavior a redelivered queue message needs, because the
	// redelivery generally overlaps rather than repeats.
	Record(ctx context.Context, u ...Usage) error
}

// Enforcer answers whether a subject may consume, and optionally records that
// they did.
//
// The two methods differ in what they cost and what they promise, and picking
// between them per call site is the point:
//
// Check reads a cached total with a bounded staleness budget and writes nothing.
// It is what belongs in front of a cheap operation, where the cost of being a
// little bit wrong is a few requests over the line and the cost of being right is
// a synchronous durable read on every request.
//
// Consume takes the durable path: it locks the subject's total for the period,
// decides against the true number, and records the usage in the same transaction.
// It is exact, and it costs a write. It is what belongs in front of anything
// whose overage is worth more than the write.
//
// Gating a cheap read on a durable write is how metering becomes the latency
// bottleneck of the system it was added to measure, which is why this is two
// methods and not one.
type Enforcer interface {
	// Check reports whether quantity may be consumed, without recording it. The
	// returned decision may be stale by up to the meter's staleness budget; see
	// Decision.Stale.
	Check(ctx context.Context, subject, meter string, quantity int64) (*Decision, error)

	// Consume checks and records atomically against the durable total.
	//
	// It generates an idempotency key of its own, because there is no natural one
	// in this signature. That makes a retried Consume count twice: the caller
	// that retries is the caller that knows the two attempts were one operation,
	// so it is the caller that has to say so — with ConsumeUsage.
	Consume(ctx context.Context, subject, meter string, quantity int64) (*Decision, error)

	// ConsumeUsage is Consume over a full Usage record, so a caller can supply
	// its own idempotency key, an event time, and dimensions. It is the method to
	// reach for on any path that can be retried.
	ConsumeUsage(ctx context.Context, u Usage) (*Decision, error)
}
