package authorization

import (
	"encoding/json"
	"iter"
	"maps"
	"slices"
	"strconv"

	"github.com/primandproper/platform-go/v13/errors"
)

// Permission names an action a principal may be authorized to perform. It is a
// bare string so that consumers can declare their own vocabulary as ordinary
// constants:
//
//	const CreateRecipesPermission authorization.Permission = "create.recipes"
//
// A consumer with an existing Permission type adopts this one with a type
// alias (`type Permission = authorization.Permission`), which leaves every
// existing constant, map key, and switch compiling unchanged.
type Permission string

// PermissionSet is an immutable set of permissions.
//
// Every method is safe on a nil receiver, and a nil *PermissionSet grants
// nothing. That is load-bearing rather than defensive: a principal with no
// membership in some scope is represented by a nil set instead of an absent
// map entry, so "no grants here" needs no special case at any call site.
type PermissionSet struct {
	perms map[Permission]struct{}
}

// NewPermissionSet returns a set containing perms. Duplicates collapse and
// empty permissions are dropped. The set copies its input, so mutating the
// caller's slice afterwards cannot change it.
func NewPermissionSet(perms ...Permission) *PermissionSet {
	m := make(map[Permission]struct{}, len(perms))
	for _, p := range perms {
		if p != "" {
			m[p] = struct{}{}
		}
	}

	return &PermissionSet{perms: m}
}

// Has reports whether p is in the set.
func (s *PermissionSet) Has(p Permission) bool {
	if s == nil || p == "" {
		return false
	}
	_, ok := s.perms[p]

	return ok
}

// HasAll reports whether every permission in perms is in the set.
//
// HasAll with no permissions is vacuously true, which is the mathematically
// honest answer and also a hazard: a requirement that accidentally resolves to
// zero permissions would authorize everyone.
//
// Set algebra wins here and the guard belongs at the declaration site, so the
// three places a permission list is declared each answer the empty case
// themselves, and they do not answer it the same way:
//
//	PermissionSet.HasAll(), Grants.HasAll()   true   — set algebra
//	http.Enforcer.Require()                   denies — an empty list is a bug
//	grpc.RequirementsBuilder.Require()        errors — refuses to build
//
// That is deliberate, but it means "empty means allow" is only ever safe with a
// list you constructed literally. Anything derived from configuration, a
// database, or a map lookup must be checked for emptiness before it reaches
// here. Enforcement code should not call this at all — use the Enforcer for its
// transport, which already guards; see authorization/http for why HTTP cannot
// do that check at boot the way gRPC does.
func (s *PermissionSet) HasAll(perms ...Permission) bool {
	for _, p := range perms {
		if !s.Has(p) {
			return false
		}
	}

	return true
}

// HasAny reports whether any permission in perms is in the set. HasAny with no
// permissions is false: there is no witness.
func (s *PermissionSet) HasAny(perms ...Permission) bool {
	return slices.ContainsFunc(perms, s.Has)
}

// Len returns the number of permissions in the set.
func (s *PermissionSet) Len() int {
	if s == nil {
		return 0
	}

	return len(s.perms)
}

// IsEmpty reports whether the set grants nothing.
func (s *PermissionSet) IsEmpty() bool {
	return s.Len() == 0
}

// All iterates the set in sorted order. The order is deterministic so that
// encodings, golden files, and equality checks over serialized forms are
// stable across runs.
func (s *PermissionSet) All() iter.Seq[Permission] {
	return func(yield func(Permission) bool) {
		if s == nil {
			return
		}
		for _, p := range slices.Sorted(maps.Keys(s.perms)) {
			if !yield(p) {
				return
			}
		}
	}
}

// Slice returns the set's permissions in sorted order.
func (s *PermissionSet) Slice() []Permission {
	if s == nil {
		return nil
	}

	return slices.Sorted(maps.Keys(s.perms))
}

// Union returns a new set containing everything in s and in others. Nil sets
// contribute nothing.
func (s *PermissionSet) Union(others ...*PermissionSet) *PermissionSet {
	out := &PermissionSet{perms: make(map[Permission]struct{}, s.Len())}
	maps.Copy(out.perms, s.safe())
	for _, o := range others {
		maps.Copy(out.perms, o.safe())
	}

	return out
}

// IsSubsetOf reports whether every permission in s is also in other. The empty
// set is a subset of everything.
func (s *PermissionSet) IsSubsetOf(other *PermissionSet) bool {
	for p := range s.safe() {
		if !other.Has(p) {
			return false
		}
	}

	return true
}

// Equal reports whether s and other contain exactly the same permissions. A nil
// set and an empty set are equal, because both grant nothing.
func (s *PermissionSet) Equal(other *PermissionSet) bool {
	return s.Len() == other.Len() && s.IsSubsetOf(other)
}

// String is deliberately a summary rather than a listing. A set can hold
// hundreds of permissions, and this type ends up attached to logs and spans —
// dumping the whole policy into telemetry on every request would be both noisy
// and a disclosure.
func (s *PermissionSet) String() string {
	return "PermissionSet(n=" + strconv.Itoa(s.Len()) + ")"
}

// safe returns the backing map, or an empty one for a nil receiver, so range
// and copy operations need no nil check.
func (s *PermissionSet) safe() map[Permission]struct{} {
	if s == nil {
		return nil
	}

	return s.perms
}

// MarshalJSON encodes the set as a sorted array of strings.
//
// The error branch is unreachable — the argument is a []Permission, and
// encoding/json cannot fail on a slice of a string type — and stays only
// because the interface requires the return and errcheck requires the handling.
// The same is true of MarshalBinary below.
func (s *PermissionSet) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(s.Slice())
	if err != nil {
		return nil, errors.Wrap(err, "marshaling permission set")
	}

	return b, nil
}

// UnmarshalJSON decodes a JSON array of strings into the set.
func (s *PermissionSet) UnmarshalJSON(data []byte) error {
	var perms []Permission
	if err := json.Unmarshal(data, &perms); err != nil {
		return errors.Wrap(err, "unmarshaling permission set")
	}
	*s = *NewPermissionSet(perms...)

	return nil
}

// MarshalBinary encodes the set as a sorted array of permissions.
//
// PermissionSet's only field is unexported, so no encoder reflecting over the
// type can reach its contents: gob, CBOR, and anything else structural see a
// struct with nothing in it. gob refuses outright. CBOR encodes the empty map
// and decodes it back with no error, which means authorization/cached — whose
// production wiring is redis, and whose default codec is CBOR — would serve an
// empty set as an authoritative hit for the length of a TTL, with no error and
// no fault counter to notice it by.
//
// encoding.BinaryMarshaler is the hook they all honor, so the encoding is
// stated once here rather than once per codec. Deliberately not GobEncode: that
// pair only taught gob, and the default moved to a codec it said nothing to.
// Any type stored in cache.Cache whose fields are all unexported needs this
// same treatment, and should be round-tripped through cache.NewDefaultCodec in
// a test to prove it.
//
// The error branch is unreachable — the argument is a []Permission, and
// encoding/json cannot fail on a slice of a string type — and stays only
// because the interface requires the return and errcheck requires the handling.
// The same is true of MarshalJSON above.
func (s *PermissionSet) MarshalBinary() ([]byte, error) {
	b, err := json.Marshal(s.Slice())
	if err != nil {
		return nil, errors.Wrap(err, "binary-encoding permission set")
	}

	return b, nil
}

// UnmarshalBinary decodes a set encoded by MarshalBinary.
func (s *PermissionSet) UnmarshalBinary(data []byte) error {
	var perms []Permission
	if err := json.Unmarshal(data, &perms); err != nil {
		return errors.Wrap(err, "binary-decoding permission set")
	}
	*s = *NewPermissionSet(perms...)

	return nil
}
