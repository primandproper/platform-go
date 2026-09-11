package comments

import (
	"context"
	"slices"
	"strings"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// TargetType names one kind of thing an application's users comment on. It is
// the value stored in target_type, the key a [Targets] catalog is keyed by, and
// half of what a [Target] is.
//
// It is a defined type rather than a string so that an application's target
// types are declarable in a form both a reader and a type checker recognize:
//
//	const Recipe comments.TargetType = "recipe"
//
// The type is what makes the set of them discoverable. A catalog has to list
// every kind of thing an application accepts comments on — a missing entry
// refuses the write — and keeping that list by hand beside the constants that
// are its source of truth is what makes it drift. Derived instead, the question
// "which constants are target types" has to be answerable, and answering it by
// matching on the constant's name means a convention nothing enforces. Declared
// type is a fact the compiler already holds and no one can spell wrong.
//
// It is deliberately not an alias. An alias is indistinguishable from string to
// a type checker, which would leave the set exactly as undiscoverable as it was.
//
// Nothing here constrains the format beyond refusing the empty string and
// surrounding space. Dots, colons, and underscores are all fine; the catalog is
// the authority on which values exist, and this package has no opinion beyond
// that.
type TargetType string

// String renders the target type as it is stored.
//
// It exists for the observability seams that take an any and switch on its type:
// a defined string type is neither string nor fmt.Stringer to that switch and
// falls through to a reflective default, which records the same text by a slower
// path that nothing would flag if it stopped matching. Spelling the conversion
// at those call sites keeps what is recorded a decision rather than a fallback.
func (t TargetType) String() string { return string(t) }

// TargetExistsFunc reports whether a target is there to be commented on.
//
// It is the optional half of the catalog, and it is the only thing that can
// answer the question this package cannot: the row a comment is about lives in a
// table the consumer owns, in a schema this store has never seen, so there is no
// foreign key to lean on and no join to make. A definition that supplies one
// turns "a comment about a recipe that does not exist" into a refused write; a
// definition that does not leaves that comment writable, and the package
// documentation owns what happens to it afterwards.
//
// It is called on the create path only, before the row is written, and it is
// given the scope the comment is being filed under — so a check that reads the
// consumer's own table reads it as the tenant, not across tenants.
//
// An error is not "absent". A hook that cannot reach its table fails the write
// rather than deciding the target is gone, because those two answers lead to
// opposite actions and only one of them is recoverable by trying again.
type TargetExistsFunc func(ctx context.Context, scope tenancy.Scope, targetID string) (bool, error)

// TargetDefinition describes one kind of thing an application accepts comments
// on.
//
// Description is what a moderation console shows beside the type, and the reason
// this is a struct rather than a set — a bare set would push every consumer into
// maintaining that text somewhere else, out of step with the types themselves.
type TargetDefinition struct {
	// Exists optionally checks that a target of this type is there before a
	// comment is written against it. Nil is no check, which is the honest
	// default: a consumer that cannot cheaply answer the question should not be
	// made to answer it badly.
	Exists TargetExistsFunc

	// Description is human-facing prose naming what this type is.
	Description string
}

// Targets is the set of things an application accepts comments on, keyed by
// target type. It is supplied at construction rather than stored, because what a
// target type means is an application opinion and this package has none.
//
// Writing a comment against a type outside the catalog is refused. That matters
// because a target type is a string underneath, string literals are typo-prone,
// and a comment written under "recipies" is a comment that is stored, is
// counted, and appears in no view — an absence somebody has to notice.
//
// Reading is not gated, and the asymmetry is deliberate. See [Store] for the
// argument: the catalog exists to stop a comment being written where nothing
// will ever list it, and that failure is at the write. A read of a type the
// catalog does not hold answers with the rows that are there — which is nothing
// at all unless the type was withdrawn, and if it was withdrawn those rows are
// exactly what the operator withdrawing it needs to reach.
type Targets map[TargetType]TargetDefinition

// Known reports whether targetType is in the catalog.
func (t Targets) Known(targetType TargetType) bool {
	_, ok := t[targetType]

	return ok
}

// TargetTypes returns the catalog's target types, sorted, for rendering a
// moderation console or an API response.
func (t Targets) TargetTypes() []TargetType {
	types := make([]TargetType, 0, len(t))
	for targetType := range t {
		types = append(types, targetType)
	}

	slices.Sort(types)

	return types
}

// Target is what a comment is about: a kind of thing, and one of them.
//
// It is one value in this package's API and two columns in its table, and both
// halves of that are deliberate. One value, because a target type without an id
// is not a target and every method that takes one takes both — passing them
// separately is how a call site ends up pairing one comment's type with
// another's id. Two columns, because a key like "recipes:1234" scopes by
// construction and cannot be indexed, filtered or enumerated as the two facts it
// is: "every comment about recipes" is a question the two-column shape answers
// and the composite one does not.
type Target struct {
	// Type is the kind of thing, as the catalog spells it.
	Type TargetType `json:"type"`
	// ID is which one, as the application spells it.
	ID string `json:"id"`
}

// Validate reports whether the target is well-formed: both halves present, and
// neither of them whitespace.
//
// It says nothing about whether the type is one the catalog holds or whether the
// thing exists — those are the store's checks, because only the store has the
// catalog. This is the shape check, and it is exported because a handler
// rejecting a malformed target before it reaches a store is a handler answering
// 400 instead of 500.
func (t Target) Validate() error {
	if strings.TrimSpace(t.Type.String()) == "" {
		return ErrEmptyTargetType
	}

	if strings.TrimSpace(t.ID) == "" {
		return ErrEmptyTargetID
	}

	return nil
}

// Zero reports whether the target names nothing at all, which is what a reply
// that adopts its parent's target looks like on the way in.
func (t Target) Zero() bool { return t.Type == "" && t.ID == "" }
