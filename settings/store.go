package settings

import (
	"context"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/tenancy"
)

// Store is the whole of what this package persists: the catalog and the
// answers stored against it.
//
// It is two interfaces because they have two callers. A DefinitionStore is
// reached by whatever administers a deployment — a migration, an admin console,
// a seeding job — and a ValueStore is reached on the request path, by the
// handler saving somebody's notification preference and by the code that reads
// it back. Splitting them is what lets a component depend on the half it uses;
// Store is here for the wiring that provides both.
//
// # Every method runs on the caller's executor
//
// A write takes a database.Tx and a read takes the wider
// database.SQLQueryExecutor. Nothing here opens a transaction of its own, and
// nothing here reaches for a connection of its own — see the settings package
// documentation for what that buys and what it costs.
//
// The asymmetry is deliberate. A Tx satisfies SQLQueryExecutor, so one read
// serves a caller holding Client.Reader() and a caller inside a transaction
// alike, and the second sees that transaction's own uncommitted writes. That is
// what [ValueStore.Resolve] needed most: a service that writes an override and
// resolves it to return the new effective value in one response reads its own
// write, where a read narrowed to the reader would answer with the value the
// subject had before.
//
// The scope is an argument on every method, including the two writes that take a
// whole [Definition] — the argument is what the statement binds, and the
// entity's own Scope field is overwritten with it rather than read from. The
// rejected alternative was entity-carried scope for those two, which would make
// "which tenant is this write for" answerable only by reading a struct the
// caller assembled somewhere else; comments.Store settled it for the module and
// this store follows.
//
// # Thirteen of these are on the wire and one is not
//
// settings/grpc serves both halves of this interface: the catalog, which is an
// operator's, and the answers stored against it, which are a person's own. It
// is the same split this interface already makes, and it is why that surface
// has two sets of grants rather than one.
//
// [ValueStore.DeleteValuesForSubject] is the one method that stays off it, and
// the reason is on the method itself: it is erasure, called from inside the
// transaction that removes the rest of a person. A write whose whole property
// is that it commits with its caller's other writes is not an RPC.
type Store interface {
	DefinitionStore
	ValueStore
}

// DefinitionStore is the catalog: what settings exist, what kind of value each
// holds, and what each falls back to.
//
// Every method takes a tenancy.Scope, and a deployment with one catalog passes
// tenancy.Global() to all of them. There is deliberately no unscoped variant of
// any of these — see the tenancy package, and the settings package
// documentation on why a definition and the values against it share a scope.
type DefinitionStore interface {
	// CreateDefinition adds a setting to the catalog inside the caller's
	// transaction, and returns it as stored — with the id it was minted under
	// and the creation time the database assigned. A nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// It refuses a name already defined in this scope with
	// ErrDefinitionNameTaken, a default the setting would not admit, and an
	// enumeration holding an empty or repeated value.
	//
	// The transaction is the caller's because a row in a consumer's schema is
	// rarely written alone. An audit entry naming who defined the setting and a
	// data change event on an outbox somebody fans out are the ordinary
	// companions, and a companion is worth what its atomicity with the row is
	// worth. Written after a transaction of this store's own had committed, they
	// would be a window in which the definition exists and nothing downstream
	// has been told — narrow, one-directional, and not something a consumer
	// could close from outside this package.
	//
	// Every statement runs on tx: the name collision check, the definition, its
	// enumeration, and the read-back of the creation time. A value set against
	// the definition later in the same transaction, through
	// [ValueStore.SetValue], finds it.
	CreateDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, definition *Definition) (*Definition, error)

	// GetDefinition reads one live definition by id, on the caller's executor. A
	// nil q is an error wrapping ErrNilExecutor.
	GetDefinition(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, definitionID string) (*Definition, error)

	// GetDefinitionByName reads one live definition by the name application
	// code spells. It is the read every value-side call begins with.
	GetDefinitionByName(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, name string) (*Definition, error)

	// ListDefinitions pages the scope's catalog.
	ListDefinitions(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Definition], error)

	// UpdateDefinition rewrites a definition, enumeration included, inside the
	// caller's transaction. A nil tx is an error wrapping ErrNilExecutor.
	//
	// It refuses an edit that some stored value no longer satisfies —
	// ErrStrandedValues, naming the subject and the value — which is the rule
	// this store exists to own. An administrator narrowing an enumeration or
	// changing a kind clears or migrates the offending values first, and the
	// refusal names them one at a time so that there is always something to do
	// next.
	//
	// The stranded-value walk runs on tx, which is what lets that clearing
	// happen in the same transaction as the edit. A value cleared through
	// [ValueStore.ClearValue] earlier in the transaction is no longer live and
	// does not strand the edit, so an administrator narrows an enumeration and
	// clears what it excludes with neither landing without the other. It is the
	// same rule, applied to the values as the transaction sees them rather than
	// as the last commit left them.
	UpdateDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, definition *Definition) error

	// ArchiveDefinition retires a setting inside the caller's transaction. A nil
	// tx is an error wrapping ErrNilExecutor.
	//
	// The values stored against it are left alone and the name stays claimed:
	// archiving is not erasure, and freeing the name would let a second
	// definition inherit rows written for the first. A catalog that genuinely
	// wants the name back deletes the definition, which takes its values with
	// it through the schema's cascade.
	ArchiveDefinition(ctx context.Context, tx database.Tx, scope tenancy.Scope, definitionID string) error
}

// ValueStore is the request path: what one subject answered, and what a setting
// resolves to for them.
//
// Every method takes the setting's name rather than a definition id, because the
// name is what application code holds — and the definition read that name costs
// is the read that validates the write anyway, so nothing is saved by making a
// caller do it first.
type ValueStore interface {
	// SetValue stores a subject's answer inside the caller's transaction,
	// replacing whatever they answered before and reviving an answer they had
	// cleared. A nil tx is an error wrapping ErrNilExecutor.
	//
	// raw is checked against the definition: of its kind, and in its
	// enumeration where there is one. A value the setting does not admit is
	// ErrMalformedValue or ErrNotEnumerated rather than a row nothing can read
	// back.
	//
	// It is the write a preference change makes when the change is also an audit
	// entry and a data change event: the three land together or not at all. See
	// [DefinitionStore.CreateDefinition] for the argument in full.
	//
	// The definition read that validates the write runs on tx, so a definition
	// created through [DefinitionStore.CreateDefinition] earlier in the same
	// transaction is one a value can be set against.
	SetValue(ctx context.Context, tx database.Tx, scope tenancy.Scope, subject Subject, name, raw string) (*Value, error)

	// GetValue reads the answer a subject stored, or ErrValueNotFound when they
	// have not answered. It is the raw row; Resolve is what applies the
	// default.
	GetValue(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subject Subject, name string) (*Value, error)

	// ClearValue takes a subject's answer back inside the caller's transaction,
	// leaving them on the definition's default. A nil tx is an error wrapping
	// ErrNilExecutor.
	//
	// See [DefinitionStore.UpdateDefinition] for what clearing a value in the
	// same transaction as an edit to its definition buys.
	ClearValue(ctx context.Context, tx database.Tx, scope tenancy.Scope, subject Subject, name string) error

	// DeleteValuesForSubject destroys everything one subject answered within
	// the scope — cleared answers included — and reports how many rows that
	// was.
	//
	// It is what an erasure is built on, and it is the one delete here because
	// clearing is not one: ClearValue archives the row, and the row still says
	// what the subject chose. A consumer whose subject access request has to
	// remove a person's stored preferences calls this from the transaction that
	// removes the person. A schema whose values all belong to one subject type
	// can cascade from that table's delete instead, and one with two cannot,
	// because a mixed subject_id column cannot reference two tables.
	//
	// Zero is not an error: a subject who never answered is a subject with
	// nothing here to erase.
	//
	// It is the one method here that settings/grpc does not serve, and this
	// paragraph is why. The transaction is not an implementation detail of the
	// call: a subject access request removes a person from a dozen tables at
	// once, and their stored preferences going with the rest or not at all is
	// the whole of what an erasure means. Over a wire the write lands in a
	// transaction of its own, on the far side of a network, at a moment the
	// caller does not choose — so what a failure leaves behind is a person
	// erased from one table and present in the others, which no amount of
	// retrying repairs after the fact. It is the same fact audit.Recorder
	// states about an audit entry, and the reason that method takes a Tx too.
	//
	// A process that erases people from somewhere else is describing an RPC of
	// its own — the one whose handler owns the transaction this write belongs
	// in — with this call inside it.
	DeleteValuesForSubject(ctx context.Context, tx database.Tx, scope tenancy.Scope, subject Subject) (int64, error)

	// ListValuesForSubject pages everything one subject has answered.
	ListValuesForSubject(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subject Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Value], error)

	// ListValuesForDefinition pages everyone who has answered one setting. It is
	// the administrative read behind "who has overridden this", and the walk
	// UpdateDefinition runs before it changes a kind or an enumeration.
	ListValuesForDefinition(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, name string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Value], error)

	// Resolve answers one setting for one subject: their value, else the
	// definition's default, else neither.
	//
	// The third case is a resolution rather than an error — the setting exists
	// and has not been answered — and reading it as a typed value reports
	// ErrSettingUnset. A setting that does not exist at all is
	// ErrDefinitionNotFound.
	//
	// It runs on the caller's executor, which is the whole reason the reads here
	// take one. A service that sets a value and resolves it to return the new
	// effective value in the same response passes the Tx it wrote through and
	// reads its own write; passing Client.Reader() from inside that transaction
	// would answer with what the subject had before it.
	Resolve(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subject Subject, name string) (*Resolution, error)

	// ResolveAll answers every live setting in the scope for one subject, sorted
	// by name.
	//
	// It is the read a settings page makes, and it is one pass over the
	// catalog and one over the subject's answers rather than one resolution
	// per setting. Settings the subject has not answered are in the result, at
	// their default or as [SourceUnset]: a page rendering "your preferences"
	// wants the ones nobody has touched too.
	//
	// Both passes run on the caller's executor, so a transaction that has just
	// written a definition and an answer to it resolves both.
	ResolveAll(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subject Subject) ([]*Resolution, error)
}
