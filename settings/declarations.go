package settings

import (
	"context"
	"errors"
	"slices"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Declaration is what a deployment says a setting is, held by the binary rather
// than by the database.
//
// It is [Definition] without the four fields the store owns — the id, the two
// stamps and the scope — and it is a separate type for that reason rather than
// as a convenience. A catalog compiled into a process names settings; it does
// not name rows, and it cannot know the id of a row it may not have written
// yet. Passing a Definition instead would mean four fields a caller could fill
// in and [DeclareDefinitions] would have to either ignore or refuse, and both
// of those are worse than a type that has nowhere to put them.
//
// Everything on it is reconciled. A declaration is the whole of what the
// setting is, so a field left at its zero value is a declaration that the
// setting has no description, no default and no enumeration — not a field
// [DeclareDefinitions] leaves alone.
type Declaration struct {
	_ struct{} `json:"-"`

	// Default is what a subject who has not answered falls back to, or nil for
	// a setting with no default at all. See [Definition.Default] for why the
	// distinction needs a pointer.
	Default *string `json:"default"`

	// Name is what application code asks for, unique within the scope. It is
	// the handle the reconcile keys on: a declaration is matched to the row it
	// describes by name, because the name is the only thing both the binary and
	// the database hold.
	Name string `json:"name"`

	// Description is prose for whoever administers the setting.
	Description string `json:"description"`

	// Kind is how a stored value is parsed.
	Kind Kind `json:"kind"`

	// Enumeration is the values this setting admits, or empty for a setting
	// that admits any value of its kind.
	Enumeration []string `json:"enumeration"`

	// AdminOnly marks a setting only an administrator may write — its value,
	// not this declaration. See [Definition.AdminOnly]: it is recorded here and
	// enforced by whoever holds the store, which on the wire is settings/grpc.
	AdminOnly bool `json:"adminOnly"`
}

// Outcome says what a declaration did to the catalog.
type Outcome string

const (
	// OutcomeCreated is a setting the scope did not define, now defined.
	OutcomeCreated Outcome = "created"
	// OutcomeUpdated is a setting the scope defined differently, now edited to
	// match the declaration.
	OutcomeUpdated Outcome = "updated"
	// OutcomeUnchanged is a setting the scope already defines exactly as
	// declared. Nothing was written for it.
	OutcomeUnchanged Outcome = "unchanged"
)

// String renders the outcome as it is reported.
func (o Outcome) String() string { return string(o) }

// Declared is one declaration's result: the definition the scope now holds, and
// what reaching it cost.
//
// The outcome is the one fact here that no later read can recover. The
// definition is a row anybody can fetch afterwards, but whether this boot
// created it, edited it or found it already right is gone the moment the
// transaction commits — and it is exactly what the companion write cares about.
// Every write in this package takes the caller's transaction so that an audit
// entry can land beside it, and a caller that could not tell the three apart
// would have to record an entry for every declaration, including the ones that
// wrote nothing.
type Declared struct {
	_ struct{} `json:"-"`

	// Definition is the setting as the scope now defines it: the row that was
	// created, the row as the edit left it, or the row that already agreed.
	// Never nil when [DeclareDefinitions] returns no error.
	Definition *Definition `json:"definition"`

	// Outcome is which of the three this was.
	Outcome Outcome `json:"outcome"`
}

// DeclareDefinitions reconciles a scope's catalog against what the caller
// declares, inside the caller's transaction, and reports what each declaration
// did.
//
// It is the call a composition root makes on every boot. Looping
// [DefinitionStore.CreateDefinition] over a catalog works exactly once: the
// second boot reaches a name the first one claimed and stops on
// [ErrDefinitionNameTaken], so what a consumer writes instead is a read, a
// comparison and a branch per setting — which is this function, and which is
// worth owning here because the comparison is the part that is easy to get
// wrong. metering.Registry.RegisterMeter is the same shape for a catalog that
// lives in memory; this is the shape for one that lives in a table.
//
// Each declaration is read by name on the transaction and takes one of three
// paths. A name the scope does not define is created. A name it defines
// differently is edited to match, keeping the id the row already has, so every
// value stored against it keeps answering. A name it already defines exactly as
// declared writes nothing at all — which is what makes a boot that changed
// nothing a read per setting and no writes, rather than a rewrite of every
// enumeration and a stranded-value walk over every subject who has answered.
//
// It is create-or-reconcile over the settings named, and deliberately not a
// sync: a setting the declaration omits is left exactly as it is. Retiring one
// is [DefinitionStore.ArchiveDefinition], said out loud, by somebody who meant
// it — a reconcile that archived the difference would let a binary rolled back
// to last week's catalog retire a setting shipped this week, and take the
// values with it.
//
// The refusals are the store's own, and they are why this is worth running on
// every boot rather than only on the first. An edit that narrows an enumeration
// or changes a kind under values subjects have already chosen is
// [ErrStrandedValues], naming the subject and the value — so a deployment whose
// new catalog would break stored answers fails to boot rather than serving rows
// that exist, resolve and fail to parse. A declaration naming a setting
// somebody archived is [ErrDefinitionNameTaken]: an archived name stays taken,
// and reviving the definition silently would undo a retirement nobody asked to
// undo.
//
// The whole catalog is checked before any of it is written — every declaration
// validated, and the set checked for a name declared twice — because a catalog
// with a typo in its ninth entry should not have written its first eight. The
// transaction is the caller's for the reason every write here takes one: the
// catalog lands with whatever the boot writes beside it, or none of it does. A
// consumer with nothing to join opens one with Client.WithTransaction and
// passes the Tx it is handed.
//
// It takes the [DefinitionStore] interface rather than hanging off [SQLStore]
// because it composes three of that interface's methods and needs nothing else:
// it works against the SQL store, against a mock, and against any other
// backing, and settings.Store keeps the fourteen methods settings/grpc's roster
// has ruled on.
func DeclareDefinitions(
	ctx context.Context,
	store DefinitionStore,
	tx database.Tx,
	scope tenancy.Scope,
	declarations []Declaration,
) ([]*Declared, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if tx == nil {
		return nil, ErrNilExecutor
	}

	if err := scope.Validate(); err != nil {
		return nil, platformerrors.Wrap(err, "declaring the settings catalog")
	}

	if err := checkCatalog(declarations); err != nil {
		return nil, err
	}

	declared := make([]*Declared, 0, len(declarations))

	for i := range declarations {
		one, err := declare(ctx, store, tx, scope, &declarations[i])
		if err != nil {
			return nil, platformerrors.Wrapf(err, "declaring setting %q", declarations[i].Name)
		}

		declared = append(declared, one)
	}

	return declared, nil
}

// checkCatalog holds the whole catalog to the rules a single definition is held
// to, and to the one rule only a set has.
//
// It runs before the first write for the reason the exported function's
// documentation gives: the alternative is a catalog that wrote its first eight
// settings and then reported that its ninth is malformed, which is a
// half-declared catalog rolled back by whatever the caller does with the error
// rather than by anything here.
//
// The duplicate is a refusal rather than a convergence. Each declaration is read
// on the caller's transaction, so a name declared twice would reconcile the
// second against what the first just wrote and report "updated" for a setting
// nobody edited — and where the two disagree, the last one silently wins. Both
// readings are a typo in somebody's catalog, and a typo is worth a refusal.
func checkCatalog(declarations []Declaration) error {
	seen := make(map[string]struct{}, len(declarations))

	for i := range declarations {
		declaration := &declarations[i]

		if err := declaration.definition().validate(); err != nil {
			return platformerrors.Wrapf(err, "declaring setting %q", declaration.Name)
		}

		if _, duplicate := seen[declaration.Name]; duplicate {
			return platformerrors.Wrapf(ErrDuplicateDeclaration, "setting %q", declaration.Name)
		}

		seen[declaration.Name] = struct{}{}
	}

	return nil
}

// declare reconciles one declaration against the scope's catalog.
//
// The read runs on the caller's transaction rather than on a reader, which is
// what lets a catalog be declared beside the rest of a boot's writes and what
// makes the reads see anything this transaction has already written.
func declare(
	ctx context.Context,
	store DefinitionStore,
	tx database.Tx,
	scope tenancy.Scope,
	declaration *Declaration,
) (*Declared, error) {
	existing, err := store.GetDefinitionByName(ctx, tx, scope, declaration.Name)

	switch {
	case err == nil:
		if declaration.matches(existing) {
			return &Declared{Definition: existing, Outcome: OutcomeUnchanged}, nil
		}

		updated, updateErr := store.UpdateDefinition(ctx, tx, scope, declaration.onto(existing))
		if updateErr != nil {
			return nil, updateErr
		}

		return &Declared{Definition: updated, Outcome: OutcomeUpdated}, nil

	case errors.Is(err, ErrDefinitionNotFound):
		created, createErr := store.CreateDefinition(ctx, tx, scope, declaration.definition())
		if createErr != nil {
			return nil, createErr
		}

		return &Declared{Definition: created, Outcome: OutcomeCreated}, nil

	default:
		return nil, err
	}
}

// definition renders the declaration as the definition a create writes: no id,
// because the store mints one, and no stamps, because they are the database's.
func (d *Declaration) definition() *Definition {
	return &Definition{
		Name:        d.Name,
		Description: d.Description,
		Kind:        d.Kind,
		Default:     d.Default,
		Enumeration: sortedEnumeration(d.Enumeration),
		AdminOnly:   d.AdminOnly,
	}
}

// onto renders the declaration as the edit to an existing row: everything the
// declaration says, over everything the store owns.
//
// The id is the existing row's, which is the whole of what reconciling buys. A
// definition rewritten under the id it already had is one every stored value
// still points at; one written under a new id would leave every answer a subject
// has given attached to a definition nothing reads.
func (d *Declaration) onto(existing *Definition) *Definition {
	updated := *existing
	updated.Description = d.Description
	updated.Kind = d.Kind
	updated.Default = d.Default
	updated.Enumeration = sortedEnumeration(d.Enumeration)
	updated.AdminOnly = d.AdminOnly

	return &updated
}

// matches reports whether the scope already defines the setting exactly as
// declared, which is the test that decides whether a boot writes anything.
//
// It compares every field a declaration carries and nothing else: the id, the
// stamps and the scope are the store's, and a definition that differs from the
// declaration only in those is a definition the declaration agrees with.
//
// The enumerations compare as sorted slices because both are sorted — the
// declaration's by sortedEnumeration, the row's by the read that hydrated it,
// which orders by the option. Comparing them as sets would be the same answer
// spelled twice; comparing them unsorted would rewrite an enumeration on every
// boot that spelled one in a different order.
func (d *Declaration) matches(existing *Definition) bool {
	if existing == nil {
		return false
	}

	return existing.Description == d.Description &&
		existing.Kind == d.Kind &&
		existing.AdminOnly == d.AdminOnly &&
		sameDefault(existing.Default, d.Default) &&
		slices.Equal(existing.Enumeration, sortedEnumeration(d.Enumeration))
}

// sameDefault compares two defaults, where having none and having "" are
// different answers — see [Definition.Default].
func sameDefault(stored, declared *string) bool {
	if stored == nil || declared == nil {
		return stored == nil && declared == nil
	}

	return *stored == *declared
}
