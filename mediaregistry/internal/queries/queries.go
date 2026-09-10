package queries

import (
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// ObjectsTable is the one table this package owns, at its canonical spelling —
// what the emitted .sql names, and what the registry's own prefix rendering
// starts from.
//
// mediaregistry/migrations is where a consumer gets the name rendered at
// their prefix. This is the canonical spelling, and migrations.Tables reads the
// DDL, so the two are cross-checked against each other in this package's tests
// rather than one being derived from the other.
const ObjectsTable = "uploads_objects"

// TableNames is every table this package owns, in the order the DDL creates it.
// One entry today; the list is what [Render] feeds the querygen registry, which
// a consumer reads back to truncate a database.
var TableNames = []string{ObjectsTable}

// ScopeColumn is the tenancy dimension the table carries and every statement is
// keyed on. It is a column, not a convention: an unscoped read of this schema is
// not expressible, because there is no statement that omits it.
const ScopeColumn = "scope"

// The columns the keyed reads and lists below name. Exported because the store
// spells them too — its argument structs key on them — and two spellings of one
// column is the drift this package exists to prevent.
const (
	// ObjectKeyColumn is where the bytes live in object storage. It is
	// object_key rather than key because `key` is reserved in MySQL.
	ObjectKeyColumn = "object_key"
	// OwnerIDColumn is whoever the consumer's authorization model calls a
	// principal: the answer to "who uploaded this".
	OwnerIDColumn = "owner_id"
	// BelongsToTypeColumn and BelongsToIDColumn are what the object hangs off,
	// in the consumer's own schema. They are always bound together — see the
	// package comment.
	BelongsToTypeColumn = "belongs_to_type"
	BelongsToIDColumn   = "belongs_to_id"
)

// ObjectColumns is the table's full shape, in the order the emitted SELECTs
// project it, which is also the order the store's conversions are written in.
var ObjectColumns = []string{
	querygen.IDColumn,
	ScopeColumn,
	ObjectKeyColumn,
	"content_type",
	"size_bytes",
	OwnerIDColumn,
	BelongsToTypeColumn,
	BelongsToIDColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
}

// InsertColumns is the columns the create supplies values for: everything but
// the database-owned ones.
//
// created_at is among those the database owns, which is the whole reason the
// schema gives it a DEFAULT — see mediaregistry/migrations. A caller-supplied
// creation time is how a row ends up with one that disagrees with its id, and
// the cursor walk orders by id while the filter window compares created_at.
func InsertColumns() []string {
	return querygen.ForInsert(ObjectColumns)
}

// keyedColumns is the table's shape as a read keyed on something other than the
// row's own id sees it: every column but the id.
//
// querygen derives a single-row statement's predicates from the column list it
// is handed — the id predicate is rendered when the list has an id, exactly as
// the archived one is — so leaving the id out is how a statement says it keys on
// something else. What it does not decide is what comes back: the projection is
// a separate list, and the read below still returns the id.
func keyedColumns() []string {
	kept := make([]string, 0, len(ObjectColumns))
	for _, column := range ObjectColumns {
		if column != querygen.IDColumn {
			kept = append(kept, column)
		}
	}

	return kept
}

// options renders the table's shape as the options StandardCRUD reads.
//
// Ownership is the scope column, so every emitted statement is keyed on it. It
// is named rather than inferred, because a table whose rows are readable across
// scopes and one whose rows are not look identical from the columns.
//
// The existence check and the update are omitted rather than renamed — see the
// package comment for why the table has neither.
func options() []querygen.Option {
	return []querygen.Option{
		querygen.WithEntity("Object", "Objects"),
		querygen.WithOwnership(ScopeColumn),
		querygen.WithOmitted(querygen.ExistsQuery, querygen.UpdateQuery),
	}
}

// Render returns the canonical sqlc input for one dialect: every statement the
// registry store executes, in a stable order.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// The table is registered for existing, not for the queries below — see
	// querygen.Registry. Registering the whole list here keeps it fed by the
	// tables existing rather than by what currently produces their SQL.
	querygen.RegisterTable(TableNames...)

	rendered := g.StandardCRUD(ObjectsTable, ObjectColumns, options()...)
	rendered = append(rendered, keyedLists(g)...)
	rendered = append(rendered, keyedReads(g)...)

	return querygen.RenderFile(rendered)
}

// keyedLists is the two pages the store answers beyond the scope's own: one
// owner's objects, and everything hanging off one thing.
//
// They are list variants rather than standard queries — a keyed column and the
// standard list on top of it — and they are the same code path
// StandardCRUD's list comes from, so the filter window, the archived toggle, the
// cursor and the two counts are not merely the same ones an unkeyed list gets,
// they are the same lines.
func keyedLists(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	byOwner := g.ListQueries("ListObjectsByOwner", ObjectsTable, ObjectColumns,
		scope, querygen.Match{Column: OwnerIDColumn})

	bySubject := g.ListQueries("ListObjectsBySubject", ObjectsTable, ObjectColumns,
		scope,
		querygen.Match{Column: BelongsToTypeColumn},
		querygen.Match{Column: BelongsToIDColumn})

	return append(byOwner, bySubject...)
}

// keyedReads is the three single-row reads beyond the standard get: two that
// key on something other than the id, and one that keys on the id but reaches
// the rows the standard get is written not to see.
//
// GetObjectByKey is what a request holding a URL path runs: the caller has the
// key the bytes live at, not the row id, and the row is what says whether they
// may have them.
//
// GetObjectIDByKey is the collision check the create runs first, so a key
// already registered reports ErrObjectKeyTaken rather than a driver-specific
// constraint violation the caller would have to parse a SQLSTATE out of. It is
// rendered from no column list at all — nil, not the table's — because the
// unique index covers archived rows and a check that skipped them would clear a
// write the index then refuses.
//
// GetArchivedObject is the read the archive answers with: the row it just
// hid, on the transaction that hid it.
//
// It exists because archiving is the one write here whose result no other read
// can see. Every single-row statement over this table filters archived_at IS
// NULL — which is what makes an archived object absent from a fetch — so a store
// that archived a row and read it back through one of those would find nothing,
// and once the transaction commits the row is unreadable through this package
// entirely. That row is the only record of the key the bytes are still sitting
// at, which is the fact a consumer's retention sweep is written from.
//
// It is rendered from no column list at all, for GetObjectIDByKey's trick and
// the opposite half of its reason: querygen derives the archived predicate from
// the columns it is handed, so a read that must see archived rows is one keyed
// entirely on its matches. What takes that predicate's place is its complement —
// archived_at IS NOT NULL — so the read-back asserts the thing it was called to
// confirm, and a guard that matched nothing cannot be read back as a success.
//
// It is not a caller-facing read and is not on mediaregistry.Store. It runs on
// the transaction that did the archiving, and the row it sees is visible to
// nothing outside that transaction until it commits.
//
// There is no companion for the create. A row that was just inserted is not
// archived, so GetObject reaches it on the same transaction, and the read-back
// the create makes is that statement rather than one of its own.
func keyedReads(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}
	key := querygen.Match{Column: ObjectKeyColumn}

	return []*querygen.Query{
		g.ReadQuery("GetObjectByKey", ObjectsTable, keyedColumns(),
			querygen.Read{Projection: ObjectColumns}, key, scope),

		g.ReadQuery("GetObjectIDByKey", ObjectsTable, nil,
			querygen.Read{Projection: []string{querygen.IDColumn}}, key, scope),

		g.ReadQuery("GetArchivedObject", ObjectsTable, nil,
			querygen.Read{Projection: ObjectColumns},
			querygen.Match{Column: querygen.IDColumn}, scope,
			querygen.Match{Column: querygen.ArchivedAtColumn, Against: querygen.NoValue, Exclude: true}),
	}
}

// FileName is what the rendered corpus for a dialect is committed as.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}
