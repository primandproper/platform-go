package queries

import (
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The tables this package owns, at their canonical spelling — what the emitted
// .sql names, and what the settings store's own prefix rendering starts from.
const (
	DefinitionsTable       = "settings_definitions"
	DefinitionOptionsTable = "settings_definition_options"
	ValuesTable            = "settings_values"
)

// TableNames is every table settings owns, in the order the DDL creates them.
//
// Three rather than the one declared below that gets a standard set: the
// options table carries no columns worth a filtered list and the values table
// keys every statement on its natural key, but a table nothing generates a
// standard query for is still a table with rows in it. That is the distinction
// the querygen registry is built around, and this is the list [Render] feeds it.
//
// settings/migrations is where a consumer gets these names rendered at their
// prefix. This list is the canonical spelling, and migrations.Tables reads the
// DDL, so the two are cross-checked against each other in this package's tests
// rather than one being derived from the other.
var TableNames = []string{
	DefinitionsTable,
	DefinitionOptionsTable,
	ValuesTable,
}

// ScopeColumn is the tenancy dimension every table with rows of its own carries
// and every statement over one is keyed on. It is a column, not a convention: an
// unscoped read of this schema is not expressible, because there is no statement
// that omits it.
//
// The options table is the exception and cannot be otherwise: an option is
// reached only through the definition whose id it names, and that definition is
// scoped. The same reasoning identity's role tables carry.
const ScopeColumn = "scope"

// The definition columns the keyed reads and the store both name. Exported
// because two spellings of one column is the drift this package exists to
// prevent.
const (
	DefinitionNameColumn         = "name"
	DefinitionKindColumn         = "kind"
	DefinitionDefaultValueColumn = "default_value"
	DefinitionAdminOnlyColumn    = "admin_only"
	DefinitionDescriptionColumn  = "description"
)

// The columns of the options table: the definition an option belongs to, and
// the value it admits. Together they are the row and its primary key.
const (
	OptionDefinitionColumn = "definition_id"
	OptionValueColumn      = "value"
)

// The four columns a stored value is addressed by, and the one it carries.
//
// Together the four are the row's natural key — one value per subject per
// definition — which is why every single-row statement over that table names
// all four rather than the id the table also carries, and why the upsert
// converges on them: the schema's UNIQUE, the upsert's ON CONFLICT and the
// store's own reads name the same four columns, and a conflict target that
// disagreed with the key would insert a second row where it meant to revive the
// first.
const (
	ValueDefinitionColumn  = "definition_id"
	ValueSubjectTypeColumn = "subject_type"
	ValueSubjectIDColumn   = "subject_id"
	ValueColumn            = "value"
)

// exceptDefinitionIDArg is the argument the name collision check excludes a row
// through: the id of the definition being updated, absent when there is not one
// yet.
//
// It is the one argument in this schema whose absence is meaningful rather than
// a caller forgetting to bind it — see nameCollisionCheck.
const exceptDefinitionIDArg = "except_definition_id"

// Definitions is the catalog: what a setting is called, what kind of value it
// holds, what it falls back to, and who may write it.
//
// Every assignable column is updatable, which is unusual in this module and is
// the point of the store's guard rather than an oversight. A definition's kind
// and its enumeration decide how every value already stored against it is read,
// so an edit that invalidates one of those values is refused by the store after
// a walk of them — see settings.SQLStore's UpdateDefinition. The alternative,
// freezing the columns, is the same refusal with no way to say yes.
var Definitions = Table{
	Name:     DefinitionsTable,
	Singular: "Definition",
	Plural:   "Definitions",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		DefinitionNameColumn,
		DefinitionDescriptionColumn,
		DefinitionKindColumn,
		DefinitionDefaultValueColumn,
		DefinitionAdminOnlyColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{DefinitionDefaultValueColumn},
	Updatable: []string{
		DefinitionNameColumn,
		DefinitionDescriptionColumn,
		DefinitionKindColumn,
		DefinitionDefaultValueColumn,
		DefinitionAdminOnlyColumn,
	},
	Omitted: []querygen.StandardQuery{querygen.ExistsQuery},
}

// Values is what a subject answered.
//
// It gets no standard queries: the create is an upsert, and every single-row
// statement keys on the (scope, subject, definition) quadruple rather than on
// the id the table carries. Its two paged reads are keyed list variants, and
// both are below.
//
// Updatable is the one column a converging write may carry over. The definition
// and the subject are the key, and the scope is immutable here as everywhere
// else in this schema, so what an upsert onto an existing row changes is the
// answer and nothing about whose answer it is.
var Values = Table{
	Name:     ValuesTable,
	Singular: "Value",
	Plural:   "Values",
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		ValueDefinitionColumn,
		ValueSubjectTypeColumn,
		ValueSubjectIDColumn,
		ValueColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Updatable: []string{ValueColumn},
}

// OptionColumns is the whole of an option row, which is also its primary key.
//
// It is a bare column list rather than a [Table] for the reason identity's role
// tables are: no id, no scope, no convention triple, and no standard query of
// any kind. Every field a Table carries would decide something for a table that
// has a caller reading it on its own, and this one has none — an option is read
// through its definition, filtered by nothing, and archived by the definition
// being archived.
var OptionColumns = []string{OptionDefinitionColumn, OptionValueColumn}

// Emitted is the tables the canonical .sql covers with the standard set, in the
// order they appear in it.
//
// One of the three. Values is deliberately absent and still contributes five
// statements; the options table contributes three. The list is what gets a set,
// not what gets a statement.
var Emitted = []*Table{&Definitions}

// Render returns the canonical sqlc input for d: the definition table's
// standard queries and every keyed statement the store runs beside them, in one
// file's worth of text.
//
// It is what settings/internal/queriesgen writes to the .sql beside this file
// and what CI regenerates to check the committed copy still matches. That .sql
// is sqlc-gen-unison's input, so what the store executes is this text exactly:
// the generated settingsdb package carries it per dialect, with the consumer's
// table prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// Every table settings owns, not the one the loop below emits for.
	// StandardCRUD registers what it emits, which leaves the options and the
	// values out — and those are tables with rows in them, so a consumer
	// reading the registry back to truncate a database would miss two of three.
	querygen.RegisterTable(TableNames...)

	var rendered []*querygen.Query
	for _, table := range Emitted {
		rendered = append(rendered, g.StandardCRUD(table.Name, table.Columns, table.Options()...)...)
	}

	rendered = append(rendered, createdAtReads(g)...)
	rendered = append(rendered, keyedDefinitionReads(g)...)
	rendered = append(rendered, nameCollisionCheck(g))
	rendered = append(rendered, optionWrites(g)...)
	rendered = append(rendered, valueReads(g)...)
	rendered = append(rendered, valueWrites(g)...)
	rendered = append(rendered, archivedValueReadBack(g), valueErasure(g))

	return querygen.RenderFile(rendered)
}

// createdAtReads is the read-back of the one column an emitted table's create
// does not carry: the creation time the database assigned it.
//
// created_at is database-owned — it is not in any create's column list, and the
// schema gives it a DEFAULT — so the value the caller handed over still holds
// the zero time when the INSERT returns, and the store reads it back inside the
// same transaction.
//
// It keys on the id alone. The scope is absent because this is not a read a
// caller reaches: it is the create's read-back of the row it has just written,
// by the id it minted for it, and the row is not visible to anything else until
// the transaction commits. The column list is the id and nothing else, which is
// also what leaves the archived predicate off a row that cannot be archived yet.
func createdAtReads(g *querygen.Generator) []*querygen.Query {
	rendered := make([]*querygen.Query, 0, len(Emitted))

	for _, table := range Emitted {
		rendered = append(rendered, g.ReadQuery(
			"Get"+table.Singular+"CreatedAt", table.Name,
			[]string{querygen.IDColumn},
			querygen.Read{Projection: []string{querygen.CreatedAtColumn}},
		))
	}

	return rendered
}

// keyedDefinitionReads is the read a caller actually makes: a definition by the
// name application code spells, rather than by an id it would have to have
// looked up first.
//
// It is the read every value-side method begins with, because a value is
// interpreted against its definition — the kind it parses as, the enumeration it
// has to be in, the default it falls back to — so there is no write here that
// can skip it.
func keyedDefinitionReads(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.ReadQuery("GetDefinitionByName", DefinitionsTable, Definitions.KeyedColumns(),
			querygen.Read{Projection: Definitions.Columns},
			querygen.Match{Column: DefinitionNameColumn},
			querygen.Match{Column: ScopeColumn}),
	}
}

// nameCollisionCheck is the read CreateDefinition and UpdateDefinition run
// before writing, so a taken name reports ErrDefinitionNameTaken rather than a
// driver's constraint violation.
//
// Two things about it are not the rest of this file's shape.
//
// It does not filter on archived_at, and that is the schema's requirement rather
// than an omission — the same trick archivedValueReadBack uses for the other
// half of the same reason. The unique index covers archived rows — archiving a
// definition does not destroy the values stored under its name, so freeing the
// name would let a second definition claim rows written for the first — and a
// check that skipped archived rows would report the name free and hand the write
// to the index, which is the driver error this read exists to prevent. The
// column list is empty for exactly that reason: querygen renders the archived
// predicate from the column list, so a read that must see archived rows is a
// read rendered from no columns at all, keyed entirely on its matches.
//
// And the row being updated is excluded through an argument the caller may
// leave unset, which is what lets one statement serve both callers. A rename
// must not collide with itself; a create has no id to exclude yet. Under the
// presence-conditional comparand the absent argument coalesces to the empty
// string and excludes an id no row has, so the statement a create runs is the
// statement a rename runs, checked once.
func nameCollisionCheck(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery("GetDefinitionIDByName", DefinitionsTable, nil,
		querygen.Read{Projection: []string{querygen.IDColumn}},
		querygen.Match{Column: DefinitionNameColumn},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{
			Column:  querygen.IDColumn,
			Against: querygen.OptionalArgument,
			Arg:     exceptDefinitionIDArg,
			Exclude: true,
		})
}

// optionWrites is the three statements the enumeration needs: the clear that
// empties a definition's option set, the insert that writes one back, and the
// batched read that hydrates a page of definitions with theirs.
//
// The first two are how an enumeration is replaced wholesale rather than
// diffed. Diffing means reading the current set first and computing two
// statements from it, which is three round trips to express "these are the legal
// values now" and a read-modify-write besides.
//
// The insert is one statement per option rather than one multi-row INSERT per
// call. The multi-row form is assembled from the caller's cardinality, so it has
// no static text — nothing for sqlc to check and nothing for querygen to emit —
// and what replaces it costs a round trip per option, inside the transaction the
// definition's own write already opened, at the cardinalities an enumeration
// actually has.
//
// The read is batched for the reason every N+1 read here is: a page of thirty
// definitions whose options are fetched inside the loop that converts rows is
// thirty round trips returning a handful each. It is ordered by the option
// value, which is what makes an enumeration come back the same way twice — a
// set has no order, and the one a caller can rely on is better than whichever
// the planner found convenient.
//
// None of the three is scoped, and none can be: the options table carries no
// scope column, because an option is reached only through the definition whose
// id it names. The id these bind is a value the store read through a scoped
// statement.
func optionWrites(g *querygen.Generator) []*querygen.Query {
	definition := querygen.Match{Column: OptionDefinitionColumn}

	return []*querygen.Query{
		g.DeleteQuery("DeleteDefinitionOptions", DefinitionOptionsTable, OptionColumns, definition),
		g.InsertQuery("InsertDefinitionOption", DefinitionOptionsTable, OptionColumns, nil),
		g.SetReadQuery("ListDefinitionOptionsByDefinitionIDs", DefinitionOptionsTable, OptionColumns,
			querygen.Read{Order: OptionValueColumn},
			querygen.SetKey{Column: OptionDefinitionColumn}),
	}
}

// valueReads is the three reads over the table that has no standard queries at
// all: one subject's answer to one definition, the page of a subject's answers,
// and the page of the answers stored against one definition.
//
// The single-row read keys on the (scope, subject, definition) quadruple rather
// than on the id the table carries, which is why it is rendered from
// Values.KeyedColumns() while projecting Values.Columns.
//
// The definition-keyed page is what makes an edit to a definition checkable. It
// is a caller-facing read — "who has overridden this setting" — and it is also
// the walk UpdateDefinition runs before it changes a kind or an enumeration, so
// that an edit invalidating a stored value is refused rather than stranding it.
// A page rather than one read of everything, because the number of subjects that
// have answered a setting is the number of subjects.
func valueReads(g *querygen.Generator) []*querygen.Query {
	var (
		scope       = querygen.Match{Column: ScopeColumn}
		subjectType = querygen.Match{Column: ValueSubjectTypeColumn}
		subjectID   = querygen.Match{Column: ValueSubjectIDColumn}
		definition  = querygen.Match{Column: ValueDefinitionColumn}
	)

	rendered := []*querygen.Query{
		g.ReadQuery("GetValue", ValuesTable, Values.KeyedColumns(),
			querygen.Read{Projection: Values.Columns},
			scope, subjectType, subjectID, definition),
	}

	rendered = append(rendered,
		g.ListQueries("ListValuesForSubject", ValuesTable, Values.Columns, scope, subjectType, subjectID)...)

	return append(rendered,
		g.ListQueries("ListValuesForDefinition", ValuesTable, Values.Columns, scope, definition)...)
}

// valueWrites is the pair that sets an answer and takes it back.
//
// The write has to converge rather than insert, because the quadruple is unique
// across live and archived rows alike. A plain INSERT fails when a subject sets
// a value they once cleared; a DELETE followed by an INSERT loses the creation
// time, which is the record of when the subject first answered. So the conflict
// branch assigns the value and clears archived_at, which is what makes setting a
// cleared value a revival rather than a second row.
//
// The conflict target is the quadruple rather than the quadruple plus anything
// else, and it is not free to be otherwise: Postgres matches ON CONFLICT against
// a unique index the table actually has, and this schema's is on those four
// columns.
//
// Clearing is an archive rather than a delete. What a subject answered is worth
// keeping — it is what a later "restore my settings" restores, and what an audit
// of a preference change reads — and the row is the thing the unique key is
// about, so archiving it leaves the key claimed and the next write converging on
// the same row.
func valueWrites(g *querygen.Generator) []*querygen.Query {
	var (
		scope       = querygen.Match{Column: ScopeColumn}
		subjectType = querygen.Match{Column: ValueSubjectTypeColumn}
		subjectID   = querygen.Match{Column: ValueSubjectIDColumn}
		definition  = querygen.Match{Column: ValueDefinitionColumn}
	)

	return []*querygen.Query{
		g.UpsertQuery("UpsertValue", ValuesTable,
			Values.Columns,
			Values.InsertColumns(),
			Values.UpdateColumns(),
			Values.Nullable,
			scope, subjectType, subjectID, definition),

		g.ArchiveQuery("ArchiveValue", ValuesTable, Values.KeyedColumns(),
			scope, subjectType, subjectID, definition),
	}
}

// archivedValueReadBack is the read ClearValue answers with: the value it took
// back, on the transaction that took it.
//
// It exists because clearing is the one write over these tables whose result no
// other read here can see. Every single-row statement filters archived_at IS
// NULL — which is what makes a cleared answer resolve to the default rather
// than to itself — so a store that archived a value and read it back through
// one of those would find nothing, and once the transaction commits the answer
// the subject gave is unreadable through this package entirely. The stamp is
// the part that cannot be reconstructed either way: archived_at is
// CURRENT_TIMESTAMP on the server, so a row assembled from what the caller
// passed in says a clearing has not happened.
//
// There is deliberately no companion for ArchiveDefinition. A retired
// definition is still the row its id names and GetDefinition still reaches it
// before the archive runs, so a second statement here would have bought a
// caller nothing it could not read for itself — and charged one to every
// caller that only wanted the setting retired. The asymmetry is settings.Store's
// and is argued there.
//
// It is rendered from no column list at all, for nameCollisionCheck's reason:
// querygen derives the archived predicate from the columns it is handed, so a
// read that must see archived rows is one keyed entirely on its matches. What
// takes that predicate's place is its complement — archived_at IS NOT NULL —
// which makes the read-back assert the thing it was called to confirm. A row
// the clearing did not move is not a row it answers with, so a guard that
// matched nothing cannot be read back as a success.
//
// It is not a caller-facing read and is not on settings.Store. It runs on the
// transaction that did the archiving, by the key that transaction just wrote
// under, and the row is visible to nothing outside it until it commits.
func archivedValueReadBack(g *querygen.Generator) *querygen.Query {
	return g.ReadQuery("GetArchivedValue", ValuesTable, nil,
		querygen.Read{Projection: Values.Columns},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: ValueSubjectTypeColumn},
		querygen.Match{Column: ValueSubjectIDColumn},
		querygen.Match{Column: ValueDefinitionColumn},
		querygen.Match{Column: querygen.ArchivedAtColumn, Against: querygen.NoValue, Exclude: true})
}

// valueErasure is the one hard delete over the values table: everything one
// subject answered within a scope, archived rows included.
//
// It exists because clearing is an archive. ClearValue leaves the row, and the
// row still says what the subject chose — which is what an audit of a preference
// change reads, and is also what a subject access request has to remove. A
// consumer whose erasure has to take a person's stored preferences with them has
// nothing else in this schema to call: an archive does not remove a value, and a
// foreign key from subject_id onto the consumer's own table is available only to
// a deployment with exactly one subject type, since a mixed column cannot
// reference two tables.
//
// The key is the scope and the subject, and not the definition: an erasure is
// about a person, not about a setting. The column list goes over without the id
// — querygen renders the id predicate from the list it is handed — and the
// archived predicate is one DeleteQuery never renders, which is what lets this
// reach the rows ClearValue archived: an erasure that skipped them would leave
// exactly the rows it exists for.
func valueErasure(g *querygen.Generator) *querygen.Query {
	return g.DeleteQuery("DeleteValuesForSubject", ValuesTable,
		Values.ColumnsExcept(querygen.IDColumn),
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: ValueSubjectTypeColumn},
		querygen.Match{Column: ValueSubjectIDColumn})
}

// FileName is the file one dialect's rendered queries are committed to.
//
// The _generated suffix is in the path rather than only in the header comment,
// because a path is what a reviewer sees in a diff, what CI's glob selects, and
// what a reader scanning this directory reads first — and these are the files
// whose answer to "this line is wrong" is to edit something else.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}
