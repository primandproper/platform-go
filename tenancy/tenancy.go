package tenancy

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

var (
	// ErrNoScope indicates a scope that names nobody — the zero Scope, reaching a
	// query or a validation. It wraps errors.ErrEmptyInputParameter, so a caller
	// may check either.
	//
	// It is not the error for "this tenant has no rows", which is an empty result,
	// nor for "this tenant may not read them", which is
	// errors.ErrPermissionDenied. It means the call never said whose data it
	// wanted.
	ErrNoScope = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no tenancy scope provided")

	// ErrUnscannableScope indicates a scope column that held something a scope
	// cannot be read from — a NULL, or a type no driver should produce for TEXT.
	ErrUnscannableScope = platformerrors.New("cannot scan tenancy scope")
)

// Scope names whose data a row is: an account, an organization, a workspace, or
// nobody.
//
// It is a struct with unexported fields rather than a named string so that the
// zero value names nobody and cannot be mistaken for a scope that does. Two
// scopes are equal when they name the same owner, so Scope is usable as a map
// key and comparable with ==.
//
// Build one with Of or Global. See the package documentation for why the empty
// identifier is not a third constructor.
type Scope struct {
	// owner is the identifier as stored. Empty for Global.
	owner string
	// known separates Global — a deliberate absence of an owner — from the zero
	// value, which is the absence of a decision. Without it the two would be one
	// value, and every forgotten scope would silently read the global one.
	known bool
}

var (
	_ driver.Valuer    = Scope{}
	_ sql.Scanner      = (*Scope)(nil)
	_ json.Marshaler   = Scope{}
	_ json.Unmarshaler = (*Scope)(nil)
)

// Of returns the scope owned by ownerID.
//
// An empty ownerID yields the zero Scope, which no query accepts. That is
// deliberate: Of takes an identifier the caller is holding, and an empty one
// means the caller's own lookup came back empty — a bug that should surface as
// ErrNoScope rather than as a silent read of the global scope. Call Global when
// the absence of an owner is the intent.
func Of(ownerID string) Scope {
	if ownerID == "" {
		return Scope{}
	}

	return Scope{owner: ownerID, known: true}
}

// Global returns the scope of data belonging to no tenant.
//
// It matches only itself. A global row is not visible to a tenant scope and a
// tenant's rows are not visible here, so an application whose events are global
// passes Global everywhere and gets the behavior it had before the dimension
// existed — because Global's stored identifier is the empty string, which is
// what a scope column defaults to.
func Global() Scope {
	return Scope{known: true}
}

// FromOwner reconstructs the scope stored as ownerID, mapping the empty
// identifier back to Global.
//
// It is the inverse of Owner, for a Store implementation whose backing is not a
// SQL driver — Scan covers the ones that are. Unlike Of it accepts the empty
// identifier, because here it arrived from a column that was written rather than
// from a caller who may have lost it.
func FromOwner(ownerID string) Scope {
	return Scope{owner: ownerID, known: true}
}

// Validate reports whether the scope names anything, returning ErrNoScope when
// it does not.
//
// It is what a component's entry points call, so that "the caller forgot the
// scope" is one error with one message wherever it is caught.
func (s Scope) Validate() error {
	if !s.known {
		return ErrNoScope
	}

	return nil
}

// IsGlobal reports whether this is the scope belonging to no tenant. The zero
// Scope is not global — it is undecided.
func (s Scope) IsGlobal() bool {
	return s.known && s.owner == ""
}

// Owner returns the identifier this scope names, which is what goes in a scope
// column: the owner's ID, or the empty string for Global.
//
// The zero Scope also returns the empty string, so Owner alone cannot tell
// Global from an unset scope. Validate first, or bind the Scope itself and let
// Value refuse.
func (s Scope) Owner() string {
	return s.owner
}

// String renders the scope for a log field or a span attribute.
//
// It is prose, not a column value — the global scope's identifier is the empty
// string, and "<global>" is only how it reads. Owner and Value are what a query
// binds. The angle brackets are there so that an owner whose ID happens to be
// "global" is still distinguishable in a log.
func (s Scope) String() string {
	switch {
	case !s.known:
		return "<unset>"
	case s.owner == "":
		return "<global>"
	default:
		return s.owner
	}
}

// Value renders the scope as a bound query parameter, implementing
// driver.Valuer.
//
// An unset scope is an error rather than an empty string. That is the whole
// reason a store binds the Scope rather than a string it derived: a predicate
// that lost its scope fails at the driver instead of reading the global scope's
// rows, and no store implementation has to remember to check.
func (s Scope) Value() (driver.Value, error) {
	if !s.known {
		return nil, ErrNoScope
	}

	return s.owner, nil
}

// Scan reads a scope from a scope column, implementing sql.Scanner. The empty
// identifier is Global, since that is how Global is stored.
//
// A NULL is refused rather than read as Global: the column this package documents
// is NOT NULL, so a NULL means the schema is not the one the queries were written
// against, and guessing which scope it meant is how one tenant's rows become
// another's.
func (s *Scope) Scan(src any) error {
	switch value := src.(type) {
	case string:
		*s = FromOwner(value)

		return nil
	case []byte:
		*s = FromOwner(string(value))

		return nil
	case nil:
		return platformerrors.Wrap(ErrUnscannableScope, "scope column is NULL")
	default:
		return platformerrors.Wrapf(ErrUnscannableScope, "scope column holds %T", src)
	}
}

// jsonNull is what an unset scope marshals to and unmarshals from.
var jsonNull = []byte("null")

// MarshalJSON renders the scope as its owner identifier, so a scope on the wire
// reads the same as the scope columns and the audit log's: "" for Global, the
// owner's ID otherwise.
//
// An unset scope is null rather than "", because "" is Global's spelling and
// conflating the two is what makes a client that omitted the field look like one
// that asked for the global scope.
func (s Scope) MarshalJSON() ([]byte, error) {
	if !s.known {
		return jsonNull, nil
	}

	return json.Marshal(s.owner)
}

// UnmarshalJSON reads the rendering MarshalJSON produces: null (or an absent
// field) is the unset scope, "" is Global, and anything else is that owner.
//
// The empty string is accepted here where Of refuses it, and the asymmetry is
// deliberate. Of takes an identifier the caller looked up, so an empty one is a
// failed lookup; a JSON document instead distinguishes the field being absent
// from its being present and empty, and the second of those is a client naming
// the global scope in the only spelling it has.
func (s *Scope) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), jsonNull) {
		*s = Scope{}

		return nil
	}

	var owner string
	if err := json.Unmarshal(data, &owner); err != nil {
		return platformerrors.Wrap(err, "unmarshaling tenancy scope")
	}

	*s = FromOwner(owner)

	return nil
}
