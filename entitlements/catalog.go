package entitlements

import (
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v13/authorization"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/metering"
)

const (
	// MaxFeatureKeyLength bounds a feature key. Keys are joined with an account
	// ID into cache keys and rendered into metric attribute values, and a key
	// long enough to matter is a key somebody has already made a mistake with.
	MaxFeatureKeyLength = 64

	// MaxPlanNameLength bounds a plan name.
	MaxPlanNameLength = 64
)

// Catalog is the set of features an application gates on and the plans that
// include them.
//
// A Catalog is built during startup and read concurrently thereafter. It is not
// safe to register into one a Checker is already running against, and nothing
// here pretends otherwise: registration is a wiring-time activity, and a mutex
// would only make an ordering bug quieter.
//
// Registration is ordered — features first, then the plans that grant them —
// because a grant naming an unregistered feature is a typo in one of the two
// names, and accepting it would produce an entitlement nothing ever asks for.
type Catalog struct {
	features map[string]Feature
	plans    map[string]Plan
	grants   map[string]map[string]Grant
}

// NewCatalog builds an empty Catalog.
func NewCatalog() *Catalog {
	return &Catalog{
		features: map[string]Feature{},
		plans:    map[string]Plan{},
		grants:   map[string]map[string]Grant{},
	}
}

// RegisterFeature adds a feature.
//
// Re-registering a key is an error rather than a replacement. A feature's kind
// decides what every grant of it means, so a silent overwrite would reinterpret
// the plans registered before it — the same catalog, read as different
// entitlements, with nothing to say when the reading changed.
//
//nolint:gocritic // hugeParam: taken by value so the catalog stores a copy a caller cannot mutate afterwards
func (c *Catalog) RegisterFeature(f Feature) error {
	if err := f.validate(); err != nil {
		return err
	}

	if _, exists := c.features[f.Key]; exists {
		return platformerrors.Wrapf(ErrDuplicateFeature, "feature %q", f.Key)
	}

	f.Permission = f.permission()

	c.features[f.Key] = f

	return nil
}

// RegisterPlan adds a plan over already-registered features.
func (c *Catalog) RegisterPlan(p Plan) error {
	if !validIdentifier(p.Name, MaxPlanNameLength) {
		return platformerrors.Wrapf(ErrInvalidPlanName, "plan %q", p.Name)
	}

	if _, exists := c.plans[p.Name]; exists {
		return platformerrors.Wrapf(ErrDuplicatePlan, "plan %q", p.Name)
	}

	grants := make(map[string]Grant, len(p.Includes))
	for i := range p.Includes {
		g := p.Includes[i]

		f, ok := c.features[g.Feature]
		if !ok {
			return platformerrors.Wrapf(ErrUnknownFeature, "plan %q grants feature %q", p.Name, g.Feature)
		}

		if _, dupe := grants[g.Feature]; dupe {
			return platformerrors.Wrapf(ErrDuplicateGrant, "plan %q grants feature %q twice", p.Name, g.Feature)
		}

		if err := g.validate(&f); err != nil {
			return platformerrors.Wrapf(err, "plan %q", p.Name)
		}

		if f.Kind == KindQuota && g.Behavior == "" {
			g.Behavior = metering.BehaviorBlock
		}

		grants[g.Feature] = g
	}

	c.plans[p.Name] = p
	c.grants[p.Name] = grants

	return nil
}

// Feature returns the feature registered under key.
func (c *Catalog) Feature(key string) (Feature, bool) {
	f, ok := c.features[key]

	return f, ok
}

// Plan returns the plan registered under name.
func (c *Catalog) Plan(name string) (Plan, bool) {
	p, ok := c.plans[name]

	return p, ok
}

// GrantFor returns what a plan includes of a feature. The second return is false
// when the plan excludes the feature, or when the plan is not registered — the
// two are the same answer to a caller deciding whether to allow, and
// distinguishing them is Plan's job.
func (c *Catalog) GrantFor(plan, feature string) (Grant, bool) {
	g, ok := c.grants[plan][feature]

	return g, ok
}

// FeatureKeys returns the registered feature keys, sorted.
func (c *Catalog) FeatureKeys() []string {
	return slices.Sorted(maps.Keys(c.features))
}

// PlanNames returns the registered plan names, sorted.
func (c *Catalog) PlanNames() []string {
	return slices.Sorted(maps.Keys(c.plans))
}

// Features returns every registered feature, sorted by key, for introspection
// and admin tooling.
func (c *Catalog) Features() []Feature {
	out := make([]Feature, 0, len(c.features))
	for _, key := range c.FeatureKeys() {
		out = append(out, c.features[key])
	}

	return out
}

// Plans returns every registered plan, sorted by name, for the pricing page and
// the admin console that need to render what each tier includes.
func (c *Catalog) Plans() []Plan {
	out := make([]Plan, 0, len(c.plans))
	for _, name := range c.PlanNames() {
		out = append(out, c.plans[name])
	}

	return out
}

// HasQuotaFeatures reports whether any registered feature is KindQuota, which is
// what decides whether a Checker needs a metering.Enforcer.
func (c *Catalog) HasQuotaFeatures() bool {
	for key := range c.features {
		if c.features[key].Kind == KindQuota {
			return true
		}
	}

	return false
}

// ValidateMeters reports whether every quota feature names a meter the registry
// has.
//
// It is a separate method rather than something RegisterFeature does, because
// the two are built by different code at different times: the catalog is a
// pricing decision and the registry is a billing one, and requiring them in a
// particular order at startup would put a dependency between two things that
// genuinely have none. NewQuotaSource runs it, which is where the two meet.
//
// A quota feature whose meter is unregistered would present as a Check that
// errors for one feature and works for the rest — the failure mode worth paying
// a startup check to avoid.
func (c *Catalog) ValidateMeters(registry *metering.Registry) error {
	if registry == nil {
		return ErrNilRegistry
	}

	for _, key := range c.FeatureKeys() {
		f := c.features[key]
		if f.Kind != KindQuota {
			continue
		}

		if _, ok := registry.Meter(f.Meter); !ok {
			return platformerrors.Wrapf(metering.ErrUnknownMeter, "feature %q names meter %q", f.Key, f.Meter)
		}
	}

	return nil
}

// validate reports whether the feature can be registered.
func (f *Feature) validate() error {
	if !validIdentifier(f.Key, MaxFeatureKeyLength) {
		return platformerrors.Wrapf(ErrInvalidFeatureKey, "feature %q", f.Key)
	}

	if !f.Kind.Valid() {
		return platformerrors.Wrapf(ErrInvalidKind, "feature %q kind %q", f.Key, f.Kind)
	}

	switch f.Kind {
	case KindQuota:
		if f.Meter == "" {
			return platformerrors.Wrapf(ErrMeterRequired, "feature %q", f.Key)
		}

		if f.GrantFlag != "" {
			return platformerrors.Wrapf(ErrGrantFlagNotAllowed, "feature %q", f.Key)
		}
	case KindBoolean:
		if f.Meter != "" {
			return platformerrors.Wrapf(ErrMeterNotAllowed, "feature %q names meter %q", f.Key, f.Meter)
		}
	}

	return nil
}

// permission returns the permission this feature maps to, defaulted.
//
// The default is applied here as well as at registration so that a Feature
// obtained any other way — a literal in a test, a value copied out of Features
// — answers the same thing a registered one does.
func (f *Feature) permission() authorization.Permission {
	if f.Permission != "" {
		return f.Permission
	}

	return authorization.Permission(PermissionPrefix + f.Key)
}

// validate reports whether the grant can be registered against a feature.
func (g *Grant) validate(f *Feature) error {
	if f.Kind == KindBoolean {
		if g.Limit != 0 || g.Unlimited || g.Behavior != "" {
			return platformerrors.Wrapf(ErrLimitOnBooleanFeature, "feature %q", g.Feature)
		}

		return nil
	}

	if g.Limit < 0 {
		return platformerrors.Wrapf(ErrNegativeLimit, "feature %q limit %d", g.Feature, g.Limit)
	}

	if g.Behavior != "" && !g.Behavior.Valid() {
		return platformerrors.Newf("invalid quota behavior %q for feature %q", g.Behavior, g.Feature)
	}

	return nil
}
