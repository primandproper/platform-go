package settings

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Hooks is what a consumer hangs off the store's writes: one method per write,
// each called inside the transaction that write ran in, once its statements have
// landed.
//
// The transaction is the point, as it is for identity.Hooks. A settings write's
// ordinary companions — an audit entry naming who defined a setting or changed
// an answer, a data change event on an outbox — are the same fact as the row, so
// a hook receives the database.Tx and writes on it, and returning an error fails
// the write: the store answers with that error and no row, and the caller's
// transaction rolls back with it.
//
// That is the whole of what a hook adds over a caller writing the companions
// beside each store call itself, and it is worth having because of what the
// caller would otherwise have to hold. ArchiveDefinition answers with an error
// alone, so a companion naming the setting it retired needs the row read first;
// an update's companion wants the row as it stood before, which is gone once the
// write has run. A consumer wrapping the Store to record those writes ends up
// reimplementing every write to call through, and reading rows the store had
// already read. A hook is handed them.
//
// What it costs is what identity.Hooks says it costs: a hook runs with a write
// transaction open, and on Postgres and MySQL with the definition's row lock
// held — exclusively after UpdateDefinition, shared after SetValue. Work that is
// slow, talks to a network, or can fail for reasons the write should survive
// belongs behind an outbox row the hook enqueues, not in the hook.
//
// An update is handed two rows, the one before it and the one after, because
// what an update means is the difference between them and that is not readable
// once the write has run. UpdateDefinition already reads the row it is about to
// rewrite, so its before row is free; SetValue's costs one keyed read on the
// transaction, which the store makes only when the hooks are not NoopHooks.
//
// Every method is "After", and none is a veto. Whether somebody may define a
// setting or change an answer is decided before the store is called; a hook
// returning an error is an abort of a decision already taken.
//
// The rows a hook is handed are the rows the write answers its caller with, and
// they are the same values: a hook that modifies one modifies what the caller
// receives. Do not.
//
// DeclareDefinitions is not a write of its own here. It reconciles a catalog
// through CreateDefinition and UpdateDefinition, so each definition it creates
// or rewrites calls those hooks, and one it finds already matching calls none.
//
// It is one interface rather than a function type per write so that a
// consumer's recording layer is one type. Embed NoopHooks and override the
// methods that matter; an operation added here later then arrives as a no-op
// rather than a compile failure.
type Hooks interface {
	// AfterCreateDefinition is called with the definition CreateDefinition
	// wrote, carrying the id it was minted under, its sorted enumeration and the
	// creation time the database stamped.
	AfterCreateDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, definition *Definition) error

	// AfterUpdateDefinition is called with the definition as it stood before
	// UpdateDefinition and as UpdateDefinition left it, enumerations included
	// and both read on the transaction — so a hook can say what changed, which
	// the row alone cannot. audit.Diff takes the pair.
	//
	// The before row is the one the update locked and checked the edit against,
	// so it costs nothing a store built with NoopHooks does not already pay.
	AfterUpdateDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, before, after *Definition) error

	// AfterArchiveDefinition is called with the definition ArchiveDefinition
	// retired, **as it stood before the archive** — ArchivedAt nil — read on the
	// transaction ahead of the statement. ArchiveDefinition answers its caller
	// with nothing, and no read on this package reaches an archived definition,
	// so this is the one place the retired row is handed to anybody. The read is
	// made only when the hooks are not NoopHooks.
	AfterArchiveDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, definition *Definition) error

	// AfterSetValue is called with the definition the answer was checked
	// against, and the subject's answer as it stood before SetValue and as
	// SetValue left it, both read on the transaction.
	//
	// before is nil when the subject had no live answer: one who never answered,
	// and one whose cleared answer this write revived. A revival is a subject
	// going from no opinion to one, which is what a nil before says; after
	// carries the creation time from when they first answered, because the write
	// converged on the row they had cleared.
	//
	// The definition is handed over because a Value names its setting only by
	// id, and the write had already read it to validate the answer.
	AfterSetValue(ctx context.Context, tx database.Tx, scope tenancy.Scope, definition *Definition, before, after *Value) error

	// AfterClearValue is called with the definition the answer was to, and the
	// answer ClearValue took back, as the clearing left it — the raw answer
	// still on it, ArchivedAt stamped — which is the same row ClearValue answers
	// its caller with.
	AfterClearValue(ctx context.Context, tx database.Tx, scope tenancy.Scope, definition *Definition, value *Value) error

	// AfterDeleteValuesForSubject is called with the subject an erasure removed
	// and how many of their answers that was. It is called when the count is
	// zero, too: an erasure that found nothing to erase still ran, and a
	// consumer recording erasures records that one.
	//
	// It is handed a count and not the rows, for the reason the write itself
	// answers with a count — the rows are the subject's own choices, and the
	// erasure exists to remove them.
	AfterDeleteValuesForSubject(ctx context.Context, tx database.Tx, scope tenancy.Scope, subject Subject, deleted int64) error
}

// NoopHooks does nothing. It is what a caller passes, by name, when it commits
// nothing alongside these writes — a seed import, a bootstrap tool, a test — and
// it is still the type to embed in a Hooks that overrides some:
//
//	type recordingHooks struct {
//		settings.NoopHooks
//
//		audit audit.Recorder
//	}
//
// Embedding it rather than implementing every method is what makes a method
// added to Hooks later additive: an embedder gains a no-op rather than a compile
// failure. A consumer implementing the interface outright — which the generated
// HooksMock invites — is the consumer the next method breaks. That can
// be the point: a consumer that records every write may prefer a new one to
// fail to compile until somebody decides what it records.
type NoopHooks struct{}

var _ Hooks = NoopHooks{}

// AfterCreateDefinition implements Hooks.
func (NoopHooks) AfterCreateDefinition(context.Context, database.Tx, tenancy.Scope, *Definition) error {
	return nil
}

// AfterUpdateDefinition implements Hooks.
func (NoopHooks) AfterUpdateDefinition(context.Context, database.Tx, tenancy.Scope, *Definition, *Definition) error {
	return nil
}

// AfterArchiveDefinition implements Hooks.
func (NoopHooks) AfterArchiveDefinition(context.Context, database.Tx, tenancy.Scope, *Definition) error {
	return nil
}

// AfterSetValue implements Hooks.
func (NoopHooks) AfterSetValue(context.Context, database.Tx, tenancy.Scope, *Definition, *Value, *Value) error {
	return nil
}

// AfterClearValue implements Hooks.
func (NoopHooks) AfterClearValue(context.Context, database.Tx, tenancy.Scope, *Definition, *Value) error {
	return nil
}

// AfterDeleteValuesForSubject implements Hooks.
func (NoopHooks) AfterDeleteValuesForSubject(context.Context, database.Tx, tenancy.Scope, Subject, int64) error {
	return nil
}
