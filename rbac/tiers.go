package rbac

import (
	"slices"

	"github.com/primandproper/primitives-go/v2/authorization"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// Tier is a kind of principal a grant is meant for.
//
// Tiers are ordered and cumulative: a TenantAdmin is also a Member, and an
// Operator is also a TenantAdmin. A permission is therefore placed in the
// lowest tier that should hold it, and once — which is what lets a surface's
// placement be checked against the methods it declares.
type Tier int

const (
	// TierMember holds grants that act on the caller's own data within a
	// tenant, or on nobody's: a catalog every signed-in caller reads.
	TierMember Tier = iota + 1
	// TierTenantAdmin holds grants that act on one tenant's data, including
	// other members' of it.
	TierTenantAdmin
	// TierOperator holds grants that act on every tenant's data, on the
	// directory, or on what the deployment itself offers — a catalog it sells
	// from, the settings it defines, the waitlists it runs, the clients that
	// speak for it.
	TierOperator
)

// String names the tier as the field of [Tiers] that holds it.
func (t Tier) String() string {
	switch t {
	case TierMember:
		return "Member"
	case TierTenantAdmin:
		return "TenantAdmin"
	case TierOperator:
		return "Operator"
	default:
		return "unknown tier"
	}
}

// Tiers sorts a surface's permissions by the kind of principal that should
// hold them.
//
// Every gRPC surface in this module exports one beside its Permissions map,
// and that is where the sort is decided — by the package that knows which of
// its grants is an oracle and which is a member reading their own row. A
// deployment composes its policy from them with [MergeTiers] and
// [PolicyFromTiers]; the role names, and how many roles there are, are its
// own.
//
// Where a surface could not tell which of two tiers a grant belonged in, it
// chose the higher. The two mistakes are not symmetric: a grant placed too high
// refuses somebody, who notices, and one placed too low hands somebody a read
// nobody meant them to have, which nobody notices.
type Tiers struct {
	// Operator grants act on every tenant's data, on the directory, or on what
	// the deployment itself offers.
	Operator []authorization.Permission `json:"operator,omitempty" yaml:"operator,omitempty"`
	// TenantAdmin grants act on one tenant's data, including other members'.
	TenantAdmin []authorization.Permission `json:"tenantAdmin,omitempty" yaml:"tenantAdmin,omitempty"`
	// Member grants act on the caller's own data within a tenant.
	Member []authorization.Permission `json:"member,omitempty" yaml:"member,omitempty"`
	// Narrowings are methods a lower tier may be let reach although the
	// permission in front of them is a higher tier's, because the surface asks
	// an authorizer inside the handler that confines the call to the caller.
	//
	// They are not grants, and [PolicyFromTiers] does not grant them: the
	// permission on the method also gates the methods beside it, so granting
	// it to the lower tier would hand over those too. A deployment that wants
	// one re-declares the method under a permission of its own with
	// authorization/grpc's RequirementsBuilder.Override, and grants that.
	Narrowings []Narrowing `json:"narrowings,omitempty" yaml:"narrowings,omitempty"`
}

// Narrowing is one method a lower tier than its permission's may safely
// reach, and the authorizer that makes it safe.
type Narrowing struct {
	// Method is the gRPC full method name, as the surface's Permissions map
	// keys it.
	Method string `json:"method" yaml:"method"`
	// Authorizer names the seam the surface asks inside the handler, as Go
	// spells it — "SignupAuthorizer.AuthorizeSubjectRead". A deployment that
	// narrows the method is relying on what that seam answers.
	Authorizer string `json:"authorizer" yaml:"authorizer"`
	// Tier is the tier the method may be narrowed to. It is below the tier of
	// the permission the method requires; a narrowing to the same tier or a
	// higher one would say nothing.
	Tier Tier `json:"tier" yaml:"tier"`
}

// RoleNames are a deployment's names for the three tiers' roles.
type RoleNames struct {
	Operator    string `json:"operator"    yaml:"operator"`
	TenantAdmin string `json:"tenantAdmin" yaml:"tenantAdmin"`
	Member      string `json:"member"      yaml:"member"`
}

// placements maps each permission to every tier it is placed in, once per
// placement, so a permission listed twice in one tier is visible as two.
func (t *Tiers) placements() map[authorization.Permission][]Tier {
	out := map[authorization.Permission][]Tier{}
	place := func(tier Tier, perms []authorization.Permission) {
		for _, p := range perms {
			out[p] = append(out[p], tier)
		}
	}

	place(TierOperator, t.Operator)
	place(TierTenantAdmin, t.TenantAdmin)
	place(TierMember, t.Member)

	return out
}

// Validate reports whether every permission is placed in one tier only.
//
// A permission listed twice within one tier is tolerated here, because it
// grants nothing twice; [Tiers.Partitions], which a surface's own test runs, is
// stricter.
//
//nolint:gocritic // hugeParam: a value receiver so it is callable on a surface's Tiers(), whose result is not addressable
func (t Tiers) Validate() error {
	var errs []error

	placements := t.placements()
	for _, p := range sortedKeys(placements) {
		if tiers := distinct(placements[p]); len(tiers) > 1 {
			errs = append(errs, platformerrors.Wrapf(ErrPermissionInTwoTiers, "%q is placed in %v", p, tiers))
		}
	}

	return platformerrors.Join(errs...)
}

// Partitions reports whether t sorts exactly the permissions a surface checks:
// every permission required by a method in required, or consulted inside a
// handler, is placed in exactly one tier, and every placed permission is one of
// those.
//
// required is the surface's Permissions map. consulted are the permissions it
// asks of a caller's grants inside a handler rather than on a method, such as
// an operator override — they are a principal's to hold like any other, and
// are tiered like any other.
//
// It also checks every narrowing: that its method is one required declares,
// so a public method — which holds no permission — cannot be narrowed into a
// grant, and that its tier is below its method's.
//
// It reports every problem it finds rather than the first, so one failing
// test names the whole of a stale partition.
//
//nolint:gocritic // hugeParam: a value receiver so it is callable on a surface's Tiers(), whose result is not addressable
func (t Tiers) Partitions(required map[string][]authorization.Permission, consulted ...authorization.Permission) error {
	var errs []error

	checked := map[authorization.Permission]bool{}
	for _, perms := range required {
		for _, p := range perms {
			checked[p] = true
		}
	}

	for _, p := range consulted {
		checked[p] = true
	}

	placements := t.placements()

	for _, p := range sortedKeys(checked) {
		switch tiers := placements[p]; len(tiers) {
		case 0:
			errs = append(errs, platformerrors.Wrapf(ErrUntieredPermission, "%q", p))
		case 1:
		default:
			errs = append(errs, platformerrors.Wrapf(ErrPermissionInTwoTiers, "%q is placed in %v", p, tiers))
		}
	}

	for _, p := range sortedKeys(placements) {
		if !checked[p] {
			errs = append(errs, platformerrors.Wrapf(ErrUncheckedPermission, "%q", p))
		}
	}

	seen := map[string]bool{}
	for i := range t.Narrowings {
		n := &t.Narrowings[i]
		if seen[n.Method] {
			errs = append(errs, platformerrors.Wrapf(ErrInvalidNarrowing, "%s is narrowed twice", n.Method))
			continue
		}

		seen[n.Method] = true

		perms, ok := required[n.Method]
		if !ok {
			errs = append(errs, platformerrors.Wrapf(ErrInvalidNarrowing, "%s requires no permission", n.Method))
			continue
		}

		if n.Authorizer == "" {
			errs = append(errs, platformerrors.Wrapf(ErrInvalidNarrowing, "%s names no authorizer", n.Method))
		}

		for _, p := range perms {
			if tiers := placements[p]; len(tiers) == 1 && n.Tier >= tiers[0] {
				errs = append(errs, platformerrors.Wrapf(ErrInvalidNarrowing,
					"%s is narrowed to %v, which is not below %q's %v", n.Method, n.Tier, p, tiers[0]))
			}
		}
	}

	return platformerrors.Join(errs...)
}

// MergeTiers combines several surfaces' tiers into one, refusing a permission
// placed in two tiers.
//
// A permission placed in the same tier by two surfaces is kept once, and so is
// a narrowing two surfaces both state; one method narrowed two different ways
// is refused. The order is the arguments', so the merge of an unchanged set of
// surfaces is the same value from one release to the next.
func MergeTiers(tiers ...Tiers) (Tiers, error) {
	var (
		out        Tiers
		narrowings = map[string]Narrowing{}
		errs       []error
	)

	for i := range tiers {
		t := &tiers[i]
		out.Operator = append(out.Operator, t.Operator...)
		out.TenantAdmin = append(out.TenantAdmin, t.TenantAdmin...)
		out.Member = append(out.Member, t.Member...)

		for j := range t.Narrowings {
			n := t.Narrowings[j]
			if prior, ok := narrowings[n.Method]; ok {
				if prior != n {
					errs = append(errs, platformerrors.Wrapf(ErrInvalidNarrowing, "%s is narrowed two different ways", n.Method))
				}

				continue
			}

			narrowings[n.Method] = n
			out.Narrowings = append(out.Narrowings, n)
		}
	}

	out.Operator = dedupe(out.Operator)
	out.TenantAdmin = dedupe(out.TenantAdmin)
	out.Member = dedupe(out.Member)

	if err := out.Validate(); err != nil {
		errs = append(errs, err)
	}

	if err := platformerrors.Join(errs...); err != nil {
		return Tiers{}, err
	}

	return out, nil
}

// PolicyFromTiers is the policy that grants each tier's permissions to the
// role a deployment names for it, with the tiers' order as inheritance: the
// tenant admin role inherits the member role, and the operator role inherits
// the tenant admin role.
//
// The names are required, and a policy with an empty one fails
// [Policy.Validate] rather than being repaired here — how a deployment with
// fewer than three roles folds the tiers together is its decision, made by
// composing the [Tiers] before handing them over. Narrowings are not granted;
// see [Tiers.Narrowings].
//
//nolint:gocritic // hugeParam: by value to match the value a surface's Tiers() and MergeTiers return
func PolicyFromTiers(t Tiers, roles RoleNames) Policy {
	return Policy{Roles: []authorization.Role{
		{
			Name:        roles.Member,
			Permissions: slices.Clone(t.Member),
		},
		{
			Name:        roles.TenantAdmin,
			Permissions: slices.Clone(t.TenantAdmin),
			Inherits:    []string{roles.Member},
		},
		{
			Name:        roles.Operator,
			Permissions: slices.Clone(t.Operator),
			Inherits:    []string{roles.TenantAdmin},
		},
	}}
}

func dedupe(perms []authorization.Permission) []authorization.Permission {
	seen := make(map[authorization.Permission]bool, len(perms))
	out := make([]authorization.Permission, 0, len(perms))

	for _, p := range perms {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

func distinct(tiers []Tier) []Tier {
	out := slices.Clone(tiers)
	slices.Sort(out)

	return slices.Compact(out)
}

func sortedKeys[V any](m map[authorization.Permission]V) []authorization.Permission {
	out := make([]authorization.Permission, 0, len(m))
	for p := range m {
		out = append(out, p)
	}

	slices.Sort(out)

	return out
}
