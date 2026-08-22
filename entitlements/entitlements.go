package entitlements

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/authorization"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/plainname"
	"github.com/primandproper/platform-go/v13/metering"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "entitlements"

// Observability keys for this package's spans and log fields. Declared once so a
// field set on a span and the same field logged beside it cannot drift, and so
// the entitlements. prefix is applied uniformly — an un-namespaced attribute
// name collides with every other component writing to the same trace.
const (
	accountKey   = "entitlements.account"
	featureKey   = "entitlements.feature"
	planKey      = "entitlements.plan"
	kindKey      = "entitlements.kind"
	allowedKey   = "entitlements.allowed"
	reasonKey    = "entitlements.reason"
	limitKey     = "entitlements.limit"
	usedKey      = "entitlements.used"
	remainingKey = "entitlements.remaining"
	quantityKey  = "entitlements.quantity"
	staleKey     = "entitlements.stale"
	cacheHitKey  = "entitlements.cache_hit"
	fallbackKey  = "entitlements.fallback"
	flagKey      = "entitlements.flag"
	meterKey     = "entitlements.meter"
	permCountKey = "entitlements.permission_count"

	// Limit keys for the mismatch report, which is the one place two limits are
	// in hand at once and the only place the distinction between them matters.
	catalogLimitKey  = "entitlements.catalog_limit"
	enforcedLimitKey = "entitlements.enforced_limit"
)

// PermissionPrefix namespaces the authorization.Permission a feature maps to
// when Feature.Permission is not set.
//
// It is a prefix rather than the bare feature key so that an entitlement and a
// role-granted permission cannot collide in the union a session builds from
// both. "may this principal do it" and "did this account pay for it" are
// different questions with different answers, and a permission set that mixes
// them without saying which is which is a set nobody can audit.
const PermissionPrefix = "entitlement."

// Unbounded is Decision.Remaining for an answer that is not a bounded quantity:
// a boolean feature, or a quota grant with Grant.Unlimited set.
//
// It is a distinct value rather than zero because zero is a real answer — a
// quota that is spent — and a caller rendering "0 remaining" for an unlimited
// feature is the bug this constant exists to make impossible.
const Unbounded int64 = -1

var (
	// ErrNotEntitled indicates the account's plan does not include the feature.
	//
	// It is an alias for the platform sentinel rather than a new error, so that
	// errors.Is matches whichever one a caller reaches for. The canonical
	// declaration lives in the root errors package because errors/http and
	// errors/grpc must map it, and importing this package to do that would drag
	// a SQL store, a job scheduler, and a message queue into the import graph of
	// the one package every handler already depends on.
	ErrNotEntitled = platformerrors.ErrNotEntitled

	// ErrQuotaExhausted indicates the account is entitled to the feature and has
	// used all of it for the current period. It is an alias for the platform
	// sentinel, for the same reason ErrNotEntitled is.
	//
	// It is deliberately separate from ErrNotEntitled: the two have different
	// remedies. "Upgrade your plan" and "wait until the first of the month" are
	// not the same instruction, and a client told the wrong one either retries
	// forever or gives up on a request that would have succeeded.
	ErrQuotaExhausted = platformerrors.ErrQuotaExhausted

	// ErrNoPlan is what a PlanSource returns for an account it has no plan for.
	//
	// It is not a failure. An account created a moment ago, or one whose
	// subscription lapsed, genuinely has no plan, and a Checker turns it into a
	// denial with ReasonNoPlan rather than into an error — a request that
	// answers 500 because a customer has not paid is a bug report from the
	// wrong team.
	ErrNoPlan = platformerrors.New("account has no plan")

	// ErrUnknownFeature indicates a Check for a feature the Catalog does not
	// define.
	//
	// It is an error rather than a denial. A denial says "your plan does not
	// include this", which is a claim about the account; an unregistered feature
	// key is a typo in the calling code, and reporting it as a denial would have
	// the caller ship a permanently dark feature and blame the plan.
	ErrUnknownFeature = platformerrors.New("unknown entitlement feature")

	// ErrUnknownPlan indicates a PlanSource returned a plan name the Catalog does
	// not define.
	//
	// Unlike ErrNoPlan this is a wiring fault — a plan renamed in the billing
	// provider and not in the catalog, most often — so it denies with
	// ReasonUnknownPlan and is counted, rather than being quietly treated as the
	// empty plan.
	ErrUnknownPlan = platformerrors.New("unknown entitlement plan")

	// ErrDuplicateFeature indicates two registrations under one feature key.
	ErrDuplicateFeature = platformerrors.New("duplicate entitlement feature registration")

	// ErrDuplicatePlan indicates two registrations under one plan name.
	ErrDuplicatePlan = platformerrors.New("duplicate entitlement plan registration")

	// ErrDuplicateGrant indicates one plan including the same feature twice.
	ErrDuplicateGrant = platformerrors.New("duplicate entitlement grant")

	// ErrInvalidFeatureKey indicates a feature key that is empty or is not a
	// plain identifier. Keys appear in cache keys, in metric attributes, and in
	// the permission a feature maps to, so they are restricted rather than
	// escaped.
	ErrInvalidFeatureKey = platformerrors.New("invalid entitlement feature key")

	// ErrInvalidPlanName indicates a plan name that is empty or is not a plain
	// identifier. Plan names are cached against an account and reported on spans.
	ErrInvalidPlanName = platformerrors.New("invalid entitlement plan name")

	// ErrInvalidKind indicates a feature whose Kind is not one this package
	// answers.
	ErrInvalidKind = platformerrors.New("invalid entitlement feature kind")

	// ErrMeterRequired indicates a KindQuota feature that names no meter. A quota
	// feature with nothing to count is a boolean feature with extra fields.
	ErrMeterRequired = platformerrors.New("quota entitlement feature requires a meter")

	// ErrMeterNotAllowed indicates a KindBoolean feature that names a meter.
	// Nothing would ever read it, and a meter named beside a boolean feature is
	// almost always a Kind that was left at its zero value.
	ErrMeterNotAllowed = platformerrors.New("boolean entitlement feature must not name a meter")

	// ErrGrantFlagNotAllowed indicates a KindQuota feature that names a grant
	// flag.
	//
	// A grant flag opens a feature the plan excludes, and for a quota feature
	// that leaves nowhere for the limit to come from — the plan is the only thing
	// that knows it. Silently granting an unlimited one is how a support override
	// becomes an unbounded bill, so the combination is refused at registration.
	// Raising one account's limit is a plan change, and PlanSource is the seam
	// for it: it may return a plan name nobody else is on.
	ErrGrantFlagNotAllowed = platformerrors.New("quota entitlement feature must not name a grant flag")

	// ErrLimitOnBooleanFeature indicates a plan granting a quantity of something
	// that has no quantity.
	ErrLimitOnBooleanFeature = platformerrors.New("boolean entitlement grant must not carry a limit")

	// ErrNegativeLimit indicates a grant with a limit below zero. Zero is a real
	// configuration — a feature a plan names and permits none of — and negative
	// is not a synonym for unlimited; Grant.Unlimited is.
	ErrNegativeLimit = platformerrors.New("negative entitlement grant limit")

	// ErrNilCatalog indicates a nil *Catalog. It wraps errors.ErrNilInputParameter,
	// so a caller may check either.
	ErrNilCatalog = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil entitlement catalog")

	// ErrNilPlanSource indicates a nil PlanSource. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilPlanSource = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil entitlement plan source")

	// ErrNilRegistry indicates a nil *metering.Registry. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilRegistry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil metering registry")

	// ErrEnforcerRequired indicates a Catalog with at least one quota feature was
	// used to build a Checker with no metering.Enforcer.
	//
	// It fails at construction rather than at the first Check. A Checker that
	// answered boolean features and errored on quota ones would pass every test
	// a consumer wrote for the features it did answer, and fail in production on
	// the one that costs money.
	ErrEnforcerRequired = platformerrors.New("entitlement catalog has quota features but no metering enforcer")

	// ErrEmptyAccount indicates a Check for the empty account. An entitlement
	// that belongs to nobody cannot be resolved, so it is refused at the boundary
	// rather than resolving whatever plan the empty string happens to map to.
	ErrEmptyAccount = platformerrors.New("empty entitlement account")
)

// Kind says what shape a feature's answer takes.
type Kind string

const (
	// KindBoolean is a feature an account either has or does not: SSO, an export
	// button, a support tier. Its answer comes from the plan and from feature
	// flags, and never touches the database.
	KindBoolean Kind = "boolean"

	// KindQuota is a feature bounded by an amount per period: seats, API calls,
	// LLM tokens. Its answer comes from the plan for the limit and from metering
	// for the consumption, so it costs a cached read.
	KindQuota Kind = "quota"
)

// Valid reports whether k is one of this package's kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindBoolean, KindQuota:
		return true
	default:
		return false
	}
}

// Reason says how a Decision was reached.
//
// It is on the decision rather than only in a log line because the two denials
// this package can produce have different remedies, and so do the two ways an
// answer can be degraded. A caller rendering an upsell needs ReasonPlanExcludes;
// one rendering "you're out until the first" needs ReasonQuotaExhausted; an
// operator staring at a wave of ReasonUnknownPlan is looking at a catalog that
// has drifted from the billing provider, not at customers who stopped paying.
type Reason string

const (
	// ReasonPlanIncludes is a boolean feature the account's plan includes.
	ReasonPlanIncludes Reason = "plan_includes"

	// ReasonPlanExcludes is a feature the account's plan does not include.
	ReasonPlanExcludes Reason = "plan_excludes"

	// ReasonFlagGranted is a boolean feature the plan excludes and a grant flag
	// opened — a percentage rollout, or a support override.
	ReasonFlagGranted Reason = "flag_granted"

	// ReasonFlagKilled is a feature a kill flag closed, whatever the plan says.
	ReasonFlagKilled Reason = "flag_killed"

	// ReasonQuotaAvailable is a quota feature with room left in the period.
	ReasonQuotaAvailable Reason = "quota_available"

	// ReasonQuotaExhausted is a quota feature with none left. The account is
	// entitled; there is simply nothing remaining until the period rolls.
	ReasonQuotaExhausted Reason = "quota_exhausted"

	// ReasonQuotaOverage is a quota feature past its limit under a behavior that
	// permits it — metering.BehaviorWarn or metering.BehaviorAllowOverage. The
	// call is allowed and Decision.Overage carries the excess.
	ReasonQuotaOverage Reason = "quota_overage"

	// ReasonUnlimited is a quota feature the plan grants without a limit. No
	// usage was read: there is no number that would change the answer.
	ReasonUnlimited Reason = "unlimited"

	// ReasonNoPlan is an account the PlanSource has no plan for.
	ReasonNoPlan Reason = "no_plan"

	// ReasonUnknownPlan is a plan name the Catalog does not define — a catalog
	// that has drifted from whatever the PlanSource reads.
	ReasonUnknownPlan Reason = "unknown_plan"

	// ReasonPlanUnavailable is a plan that could not be resolved because the
	// PlanSource failed and no fallback plan is configured. It denies; see
	// CheckerConfig.FallbackPlan for the other answer.
	ReasonPlanUnavailable Reason = "plan_unavailable"
)

// Feature is one thing a plan may include.
//
// Features are declared in Go, not in configuration. Which features exist and
// what kind each one is are facts about the code that reads them — a quota
// feature names a meter the application registered, and a boolean one names a
// permission the application checks — so both are compiled in and neither is
// spellable in an environment variable. What plan includes what is the part that
// varies per deployment, and that is Plan.
type Feature struct {
	// Key identifies the feature — "sso", "advanced_search", "llm_tokens". It
	// appears in cache keys, in metric attributes, and in the permission the
	// feature maps to, so it must be a plain identifier: a letter or underscore
	// followed by letters, digits, or underscores.
	Key string

	// Kind says whether the feature is on/off or bounded by an amount. Required.
	Kind Kind

	// Meter names the registered metering meter a KindQuota feature counts
	// against. Required for KindQuota, forbidden for KindBoolean.
	//
	// It is a separate field rather than being assumed equal to Key because the
	// two vocabularies are owned by different people. A meter is named by
	// whoever set up billing; a feature is named by whoever writes the gate. They
	// agree until the first time one of them is renamed.
	Meter string

	// Permission is the authorization.Permission a KindBoolean feature
	// contributes to Checker.Permissions. Defaults to PermissionPrefix + Key.
	//
	// It is settable so a consumer with an existing permission vocabulary can map
	// an entitlement onto a name its handlers already check, instead of
	// rewriting every call site to learn a second one.
	Permission authorization.Permission

	// GrantFlag names a boolean feature flag that opens this feature for accounts
	// whose plan excludes it. KindBoolean only — see ErrGrantFlagNotAllowed.
	//
	// This is the rollout and the support override: a flag targeted at ten
	// percent of accounts, or at one account somebody promised something to on a
	// call. Neither should require a plan change, and neither should require a
	// deploy.
	GrantFlag string

	// KillFlag names a boolean feature flag that closes this feature for the
	// accounts it targets, whatever the plan says. It applies to both kinds.
	//
	// It beats GrantFlag, because a kill switch that a grant can beat is not a
	// kill switch.
	KillFlag string
}

// Grant is what one plan includes of one feature.
//
// Grant and Plan carry json and yaml tags; Feature deliberately does not. That
// asymmetry is the boundary between what varies per deployment and what does
// not, made visible in the types: a plan catalog is a document an operator
// edits, and a feature is a thing the code gates on.
type Grant struct {
	// Feature is the key of a registered Feature. Required.
	Feature string `json:"feature,omitempty" yaml:"feature,omitempty"`

	// Behavior says what happens at the limit, in metering's vocabulary.
	// Defaults to metering.BehaviorBlock. KindQuota only.
	//
	// The default differs from what a usage-billing meter usually wants, and
	// deliberately: this package gates, and a gate whose default is to let
	// everything through is a decoration. A plan that sells overage says so with
	// metering.BehaviorAllowOverage, which is then visible in the catalog rather
	// than implied by an absence.
	Behavior metering.QuotaBehavior `json:"behavior,omitempty" yaml:"behavior,omitempty"`

	// Limit is how much of the feature the plan includes per period, in the
	// meter's unit. KindQuota only.
	//
	// Zero is a real configuration — a plan that names a feature and permits none
	// of it, which is how a metered feature is switched off for a tier without
	// disappearing from the account's plan summary. It is not a synonym for
	// unlimited; Unlimited is.
	Limit int64 `json:"limit,omitempty" yaml:"limit,omitempty"`

	// Unlimited grants the feature without a bound. KindQuota only.
	//
	// A Check against an unlimited grant reads no usage at all: there is no
	// number that would change the answer, so paying for the read would be
	// spending latency to learn nothing. Usage is still recorded — that is the
	// Recorder's job and has nothing to do with this — so an unlimited plan still
	// produces the totals an invoice or a dashboard is built from.
	Unlimited bool `json:"unlimited,omitempty" yaml:"unlimited,omitempty"`
}

// Plan is a named bundle of grants.
//
// This is the piece the issue behind this package called configuration, and it
// is the only piece that is: which features a tier includes and how much of each
// changes when pricing changes, which is more often than a deploy and by people
// who do not ship one. See entitlements/config for reading a catalog out of the
// environment.
type Plan struct {
	// Name identifies the plan. It must be a plain identifier, and it must be
	// whatever the PlanSource returns — the two are joined by string equality and
	// by nothing else.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description is human-facing documentation, surfaced by Catalog.Plans for
	// admin tooling and pricing pages. It has no effect on any decision.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Includes are the grants this plan makes. A feature absent from the list is
	// a feature the plan excludes.
	Includes []Grant `json:"includes,omitempty" yaml:"includes,omitempty"`
}

// Decision is the answer to "may this account use this feature, and how much is
// left".
type Decision struct {
	// ResetsAt is when the period ends and Used returns to zero, for a bounded
	// quota feature. Zero for every other kind of answer. It is what a caller
	// puts in a Retry-After.
	ResetsAt time.Time

	// Feature is the feature key the decision is about.
	Feature string

	// Plan is the plan the answer came from, or empty when none could be
	// resolved. It is on the decision because the first question asked of any
	// surprising denial is "what plan do they think I'm on".
	Plan string

	// Kind is the feature's kind, so a caller can tell whether the quantity
	// fields mean anything without consulting the catalog again.
	Kind Kind

	// Reason says how the decision was reached. See Reason.
	Reason Reason

	// Permission is the authorization.Permission this feature maps to. It is
	// carried so that a handler which gates on entitlements and one which gates
	// on roles are naming the same thing.
	Permission authorization.Permission

	// Limit is the quota's limit for the period. Unbounded when the answer is not
	// a bounded quantity.
	Limit int64

	// Used is the account's total for the period, as of this answer.
	Used int64

	// Remaining is Limit minus Used, floored at zero. Unbounded when the answer
	// is not a bounded quantity — see Unbounded.
	Remaining int64

	// Overage is how far Used is past Limit, or zero when it is not. It is
	// non-zero only under a behavior that permits going past, and it is the
	// quantity an overage price is applied to.
	Overage int64

	// Allowed says whether the caller may proceed.
	Allowed bool

	// Unlimited says the plan grants this feature without a bound, so Limit,
	// Used, and Remaining carry nothing.
	Unlimited bool

	// Stale says some part of the answer was served from a cache and may be
	// behind: the account's plan by up to CheckerConfig.CacheTTL, the usage total
	// by up to the meter's staleness budget.
	//
	// It is on the decision rather than only in a metric because it is the one
	// thing a caller might want to act on. A request worth a fraction of a cent
	// proceeds on a stale allow; one that provisions something expensive can
	// decide to pay for metering's exact path instead.
	Stale bool
}

// Err reports the decision as an error, or nil when it allows.
//
// This is how a handler gates on an entitlement the same way it gates on a
// permission: return it, and errors/http turns it into a 402 with a code that
// says which of the two denials it was, while errors/grpc turns it into
// PermissionDenied or ResourceExhausted. Neither message names the feature —
// which features exist is not something to disclose to a caller who just failed
// to reach one.
//
// A nil decision reports ErrNotEntitled, so a caller that ignored an error and
// gated on the decision anyway fails closed.
func (d *Decision) Err() error {
	if d == nil {
		return ErrNotEntitled
	}

	if d.Allowed {
		return nil
	}

	if d.Reason == ReasonQuotaExhausted {
		return ErrQuotaExhausted
	}

	return ErrNotEntitled
}

// Checker answers entitlement questions.
//
// The three methods differ in what they cost and in what they can honestly
// answer, and picking between them per call site is the point.
//
// Check and CheckQuantity are the request-path question. For a boolean feature
// they are a cached plan lookup, two map lookups, and whatever the flag provider
// costs, which for every provider here is a local evaluation. For a quota
// feature they add metering's cached read.
//
// Permissions is the session-build question, and it answers only for boolean
// features. That is not an omission: a permission set is checked without I/O and
// without an error, and a quota answer that was true when the set was built can
// be false by the time it is read. Putting one in there would produce a
// permission that means "was entitled a moment ago", which every caller would
// read as "may proceed".
type Checker interface {
	// Check reports whether the account may use the feature at all — for a quota
	// feature, whether there is room for one more unit.
	//
	// One unit rather than none, because "may I consume nothing" is true at
	// exactly the moment a quota is spent, and that is the moment the question is
	// being asked.
	Check(ctx context.Context, account, feature string, opts ...CheckOption) (*Decision, error)

	// CheckQuantity is Check for a caller that knows how much it is about to
	// consume: whether the account may use quantity more of the feature this
	// period. quantity is ignored for a boolean feature.
	//
	// It is the method to reach for in front of anything whose cost is known
	// before it runs — a completion whose token count was estimated, a bulk
	// import whose row count is in hand. Asking for one unit and then consuming
	// five thousand is how a limit is exceeded by a factor nobody chose.
	CheckQuantity(ctx context.Context, account, feature string, quantity int64, opts ...CheckOption) (*Decision, error)

	// Permissions resolves the account's boolean entitlements into a permission
	// set, for a caller building a session.
	//
	// The result is meant to be OR'd with whatever the principal's roles grant:
	//
	//	authorization.NewGrants(rolePerms, entitlementPerms)
	//
	// which is the same shape authorization already uses to merge service-wide
	// and per-tenant authority. Quota features are absent from the set; see the
	// interface documentation.
	Permissions(ctx context.Context, account string, opts ...CheckOption) (*authorization.PermissionSet, error)
}

// validIdentifier reports whether a name is a plain identifier.
//
// Feature keys and plan names travel into cache keys, metric attribute values,
// and permission strings, which is the rule internal/plainname states — and the
// same rule metering applies to a meter name, since a quota feature's key and
// the meter it counts against end up in the same cache key.
func validIdentifier(name string, maxLen int) bool {
	return plainname.Valid(name, maxLen)
}
