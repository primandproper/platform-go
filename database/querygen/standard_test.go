package querygen

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recovered runs fn and returns whatever it panicked with as an error, or nil.
func recovered(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			var ok bool
			if err, ok = r.(error); !ok {
				err = platformerrors.Newf("panicked with a non-error: %v", r)
			}
		}
	}()

	fn()

	return nil
}

// named returns the query with the given annotation name, failing the test when
// there is none.
func named(tb testing.TB, queries []*Query, name string) *Query {
	tb.Helper()

	for _, query := range queries {
		if query.Annotation.Name == name {
			return query
		}
	}

	tb.Fatalf("no query named %q in %s", name, strings.Join(queryNames(queries), ", "))

	return nil
}

func queryNames(queries []*Query) []string {
	names := make([]string, 0, len(queries))
	for _, query := range queries {
		names = append(names, query.Annotation.Name)
	}

	return names
}

func TestStandardCRUD(T *testing.T) {
	T.Parallel()

	T.Run("emits the whole set for a conventional table", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("valid_instruments", columnsFor(LastIndexedAtColumn),
			WithEntity("ValidInstrument", "ValidInstruments"))

		test.Eq(t, []string{
			"CreateValidInstrument",
			"GetValidInstrument",
			"CheckValidInstrumentExistence",
			"ListValidInstruments",
			"UpdateValidInstrument",
			"ArchiveValidInstrument",
			"ScanValidInstrumentIDsForReindex",
			"MarkValidInstrumentsAsIndexed",
		}, queryNames(queries))
	})

	T.Run("annotates each query with the type its result shape needs", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(LastIndexedAtColumn))

		test.EqOp(t, ExecType, named(t, queries, "CreateThings").Annotation.Type)
		test.EqOp(t, OneType, named(t, queries, "GetThings").Annotation.Type)
		test.EqOp(t, OneType, named(t, queries, "CheckThingsExistence").Annotation.Type)
		test.EqOp(t, ManyType, named(t, queries, "ListThings").Annotation.Type)
		test.EqOp(t, ExecRowsType, named(t, queries, "UpdateThings").Annotation.Type)
		test.EqOp(t, ExecRowsType, named(t, queries, "ArchiveThings").Annotation.Type)
		test.EqOp(t, ManyType, named(t, queries, "ScanThingsIDsForReindex").Annotation.Type)
		test.EqOp(t, ExecRowsType, named(t, queries, "MarkThingsAsIndexed").Annotation.Type)
	})

	T.Run("the default names cannot collide with each other", func(t *testing.T) {
		t.Parallel()

		// Singular and plural both default to the table name, so the single-row
		// read and the list would share a name if the list were spelled Get.
		queries := pg().StandardCRUD("things", columnsFor(LastIndexedAtColumn))

		seen := map[string]struct{}{}
		for _, name := range queryNames(queries) {
			_, repeated := seen[name]
			test.False(t, repeated, test.Sprintf("duplicate name %q", name))
			seen[name] = struct{}{}
		}
	})

	T.Run("insert takes a value for every column the database does not own", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(LastIndexedAtColumn))

		want := `INSERT INTO things (
	id,
	name
) VALUES (
	sqlc.arg(id),
	sqlc.arg(name)
);`

		test.EqOp(t, want, named(t, queries, "CreateThings").Content)
	})

	T.Run("update assigns the mutable columns and stamps last_updated_at", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor())

		want := `UPDATE things SET
	name = sqlc.arg(name),
	last_updated_at = CURRENT_TIMESTAMP
WHERE archived_at IS NULL
	AND id = sqlc.arg(id);`

		test.EqOp(t, want, named(t, queries, "UpdateThings").Content)
	})

	T.Run("archive is a soft delete that refuses to run twice", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor())

		// The archived_at IS NULL is what makes the :execrows count meaningful:
		// re-archiving an archived row affects nothing and the caller learns so.
		want := `UPDATE things SET
	archived_at = CURRENT_TIMESTAMP
WHERE archived_at IS NULL
	AND id = sqlc.arg(id);`

		test.EqOp(t, want, named(t, queries, "ArchiveThings").Content)
	})

	T.Run("get reads one unarchived row by id", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", []string{IDColumn, "name", ArchivedAtColumn})

		want := `SELECT
	things.id,
	things.name,
	things.archived_at
FROM things
WHERE things.archived_at IS NULL
	AND things.id = sqlc.arg(id);`

		test.EqOp(t, want, named(t, queries, "GetThings").Content)
	})

	T.Run("exists asks the same question without reading the row", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", []string{IDColumn, "name", ArchivedAtColumn})

		want := `SELECT EXISTS (
	SELECT things.id
	FROM things
	WHERE things.archived_at IS NULL
		AND things.id = sqlc.arg(id)
);`

		test.EqOp(t, want, named(t, queries, "CheckThingsExistence").Content)
	})

	T.Run("list carries both counts and the keyset walk", func(t *testing.T) {
		t.Parallel()

		content := named(t, pg().StandardCRUD("things", columnsFor()), "ListThings").Content

		test.StrContains(t, content, ") AS filtered_count")
		test.StrContains(t, content, ") AS total_count")
		test.StrContains(t, content, "ORDER BY things.id ASC")
		test.StrContains(t, content, "LIMIT COALESCE(sqlc.narg(result_limit), 50)")
	})
}

func TestStandardCRUD_columnsDecide(T *testing.T) {
	T.Parallel()

	T.Run("no archived_at, no archive", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", []string{IDColumn, "name", CreatedAtColumn})

		test.SliceNotContains(t, queryNames(queries), "ArchiveThings")
		test.StrNotContains(t, named(t, queries, "GetThings").Content, ArchivedAtColumn)
		test.StrNotContains(t, named(t, queries, "ListThings").Content, IncludeArchivedArg)
	})

	T.Run("no last_indexed_at, no reindex scan and no stamp", func(t *testing.T) {
		t.Parallel()

		names := queryNames(pg().StandardCRUD("things", columnsFor()))

		test.SliceNotContains(t, names, "ScanThingsIDsForReindex")
		test.SliceNotContains(t, names, "MarkThingsAsIndexed")
	})

	T.Run("indexed but not archivable gets the stamp without the reindex scan", func(t *testing.T) {
		t.Parallel()

		// The scan filters on archived_at, so emitting it would name a column
		// the table does not have. The stamp names only the two columns it
		// assigns and keys on, so it is emitted regardless — the column would
		// otherwise be one nothing can write.
		names := queryNames(pg().StandardCRUD("things", []string{IDColumn, "name", LastIndexedAtColumn}))

		test.SliceNotContains(t, names, "ScanThingsIDsForReindex")
		test.SliceContains(t, names, "MarkThingsAsIndexed")
	})

	T.Run("the stamp is the write the column's owner rule names", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(LastIndexedAtColumn))

		want := `UPDATE things SET
	last_indexed_at = CURRENT_TIMESTAMP
WHERE id = ANY(sqlc.arg(ids)::text[]);`

		test.EqOp(t, want, named(t, queries, "MarkThingsAsIndexed").Content)

		// The column is database-owned, so neither write a caller drives may
		// name it — which is what leaves the stamp as its only writer.
		test.StrNotContains(t, named(t, queries, "CreateThings").Content, LastIndexedAtColumn)
		test.StrNotContains(t, named(t, queries, "UpdateThings").Content, LastIndexedAtColumn)
	})

	T.Run("WithOmitted drops the stamp from a table that has the column", func(t *testing.T) {
		t.Parallel()

		// The escape hatch for a consumer that maintains last_indexed_at some
		// other way, or is not ready to. The reindex scan is unaffected, so the
		// two halves can be adopted separately.
		names := queryNames(pg().StandardCRUD("things", columnsFor(LastIndexedAtColumn),
			WithOmitted(MarkAsIndexedQuery)))

		test.SliceNotContains(t, names, "MarkThingsAsIndexed")
		test.SliceContains(t, names, "ScanThingsIDsForReindex")
	})

	T.Run("WithQueryName renames the stamp onto an existing spelling", func(t *testing.T) {
		t.Parallel()

		// The migration path for a consumer whose generated code already calls
		// the write something else.
		queries := pg().StandardCRUD("things", columnsFor(LastIndexedAtColumn),
			WithQueryName(MarkAsIndexedQuery, "UpdateThingsLastIndexedAt"))

		test.SliceNotContains(t, queryNames(queries), "MarkThingsAsIndexed")
		test.StrContains(t, named(t, queries, "UpdateThingsLastIndexedAt").Content, LastIndexedAtColumn)
	})

	T.Run("the stamp carries no owner predicate", func(t *testing.T) {
		t.Parallel()

		// It is the sync's own machinery stamping rows it named explicitly,
		// not a consumer read that owes a tenancy scope — the same reason the
		// reindex scan is unscoped.
		queries := pg().StandardCRUD("things", columnsFor(LastIndexedAtColumn, BelongsToAccountColumn),
			WithOwnership(BelongsToAccountColumn))

		test.StrNotContains(t, named(t, queries, "MarkThingsAsIndexed").Content, BelongsToAccountColumn)
	})

	T.Run("nothing mutable, no update", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", []string{IDColumn, CreatedAtColumn, ArchivedAtColumn})

		test.SliceNotContains(t, queryNames(queries), "UpdateThings")
	})

	T.Run("nothing assignable, no create", func(t *testing.T) {
		t.Parallel()

		// An INSERT with an empty column list is a syntax error rather than a
		// degenerate insert, so it is left out rather than emitted broken.
		queries := pg().StandardCRUD("things", []string{IDColumn}, WithDatabaseOwned(IDColumn))

		test.SliceNotContains(t, queryNames(queries), "CreateThings")
		test.SliceContains(t, queryNames(queries), "GetThings")
	})

	T.Run("no last_updated_at, no stamp and no updated window", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", []string{IDColumn, "name", CreatedAtColumn, ArchivedAtColumn})

		test.StrNotContains(t, named(t, queries, "UpdateThings").Content, LastUpdatedAtColumn)
		test.StrNotContains(t, named(t, queries, "ListThings").Content, UpdatedAfterArg)
	})

	T.Run("a table with only an id still lists", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", []string{IDColumn})

		test.Eq(t, []string{"CreateThings", "GetThings", "CheckThingsExistence", "ListThings"}, queryNames(queries))
		test.StrContains(t, named(t, queries, "ListThings").Content, "WHERE things.id > COALESCE(sqlc.narg(cursor), '')")
	})
}

func TestStandardCRUD_options(T *testing.T) {
	T.Parallel()

	T.Run("WithEntity names the queries", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("valid_instruments", []string{IDColumn},
			WithEntity("ValidInstrument", "ValidInstruments"))

		test.Eq(t, []string{
			"CreateValidInstrument",
			"GetValidInstrument",
			"CheckValidInstrumentExistence",
			"ListValidInstruments",
		}, queryNames(queries))
	})

	T.Run("WithQueryName renames one of them", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", []string{IDColumn}, WithQueryName(ListQuery, "GetThingsForAccount"))

		test.SliceContains(t, queryNames(queries), "GetThingsForAccount")
		test.SliceNotContains(t, queryNames(queries), "ListThings")
	})

	T.Run("WithOwnership scopes every query that addresses a row", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(BelongsToAccountColumn),
			WithOwnership(BelongsToAccountColumn))

		scoped := "sqlc.arg(" + BelongsToAccountColumn + ")"
		for _, name := range []string{"GetThings", "CheckThingsExistence", "ListThings", "UpdateThings", "ArchiveThings"} {
			test.StrContains(t, named(t, queries, name).Content, scoped, test.Sprintf("%s is unscoped", name))
		}
	})

	T.Run("WithOwnership makes the owner column unassignable", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(BelongsToAccountColumn),
			WithOwnership(BelongsToAccountColumn))

		// It is still an insert column — a row has to be given an owner — but a
		// row that can reassign its own owner makes the scope on every other
		// query a formality, so it appears in the UPDATE's WHERE and not its SET.
		update := named(t, queries, "UpdateThings").Content
		assignments, where, found := strings.Cut(update, "\nWHERE ")
		must.True(t, found)

		test.StrContains(t, named(t, queries, "CreateThings").Content, BelongsToAccountColumn)
		test.StrNotContains(t, assignments, BelongsToAccountColumn)
		test.StrContains(t, where, BelongsToAccountColumn+" = sqlc.arg("+BelongsToAccountColumn+")")
	})

	T.Run("WithDatabaseOwned excludes a column from insert and update alike", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor("search_vector"), WithDatabaseOwned("search_vector"))

		test.StrNotContains(t, named(t, queries, "CreateThings").Content, "search_vector")
		test.StrNotContains(t, named(t, queries, "UpdateThings").Content, "search_vector")
		// It is still part of the row, so reads return it.
		test.StrContains(t, named(t, queries, "GetThings").Content, "things.search_vector")
	})

	T.Run("WithImmutable excludes a column from update only", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor("created_by_user"), WithImmutable("created_by_user"))

		test.StrContains(t, named(t, queries, "CreateThings").Content, "sqlc.arg(created_by_user)")
		test.StrNotContains(t, named(t, queries, "UpdateThings").Content, "created_by_user")
	})

	T.Run("WithOmitted drops the queries it names", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(),
			WithOmitted(GetQuery, ExistsQuery, ListQuery))

		test.Eq(t, []string{
			"CreateThings",
			"UpdateThings",
			"ArchiveThings",
		}, queryNames(queries))
	})

	T.Run("WithOmitted cannot add a query the columns exclude", func(t *testing.T) {
		t.Parallel()

		// No archived_at, so there is no Archive to omit and none appears.
		queries := pg().StandardCRUD("things", []string{IDColumn, "name", CreatedAtColumn},
			WithOmitted(ArchiveQuery, ScanIDsForReindexQuery))

		test.SliceNotContains(t, queryNames(queries), "ArchiveThings")
		test.SliceNotContains(t, queryNames(queries), "ScanThingsIDsForReindex")
		test.SliceContains(t, queryNames(queries), "CreateThings")
	})

	T.Run("WithOmitted accumulates across calls", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(),
			WithOmitted(ListQuery),
			WithOmitted(ExistsQuery))

		test.SliceNotContains(t, queryNames(queries), "ListThings")
		test.SliceNotContains(t, queryNames(queries), "CheckThingsExistence")
		test.SliceContains(t, queryNames(queries), "GetThings")
	})

	T.Run("WithOmitted frees the name it dropped", func(t *testing.T) {
		t.Parallel()

		// Two queries sharing a name is a panic, so a rename onto an omitted
		// query's default name is the check that the omitted one is truly gone
		// rather than merely filtered out of the result.
		queries := pg().StandardCRUD("things", columnsFor(),
			WithOmitted(ListQuery),
			WithQueryName(GetQuery, "ListThings"))

		test.SliceContains(t, queryNames(queries), "ListThings")
		test.SliceNotContains(t, queryNames(queries), "GetThings")
	})

	T.Run("omitting everything yields nothing to render", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(),
			WithOmitted(CreateQuery, GetQuery, ExistsQuery, ListQuery, UpdateQuery, ArchiveQuery,
				ScanIDsForReindexQuery, MarkAsIndexedQuery))

		test.SliceEmpty(t, queries)
		test.EqOp(t, "", RenderFile(queries))
	})

	T.Run("WithNullable binds a column through narg on both writes", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor("nickname"), WithNullable("nickname"))

		test.StrContains(t, named(t, queries, "CreateThings").Content, "sqlc.narg(nickname)")
		test.StrContains(t, named(t, queries, "UpdateThings").Content, "nickname = sqlc.narg(nickname)")
	})

	T.Run("WithNullable leaves the other columns alone", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor("nickname"), WithNullable("nickname"))

		create := named(t, queries, "CreateThings").Content
		test.StrContains(t, create, "sqlc.arg(name)")
		test.StrNotContains(t, create, "sqlc.narg(name)")
	})

	T.Run("WithNullable does not reach reads", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor("nickname"), WithNullable("nickname"))

		// A SELECT lists the column, it does not bind it.
		test.StrContains(t, named(t, queries, "GetThings").Content, "things.nickname")
		test.StrNotContains(t, named(t, queries, "GetThings").Content, "narg")
	})

	T.Run("WithNullable naming a column the writes exclude changes nothing", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", columnsFor(), WithNullable(CreatedAtColumn))

		test.StrNotContains(t, named(t, queries, "CreateThings").Content, CreatedAtColumn)
		test.StrNotContains(t, named(t, queries, "UpdateThings").Content, "sqlc.narg")
	})

	T.Run("options apply in order", func(t *testing.T) {
		t.Parallel()

		queries := pg().StandardCRUD("things", []string{IDColumn},
			WithEntity("Thing", "Things"),
			WithEntity("Widget", "Widgets"))

		test.SliceContains(t, queryNames(queries), "CreateWidget")
	})
}

func TestStandardCRUD_panics(T *testing.T) {
	T.Parallel()

	T.Run("a table name that is not an identifier", func(t *testing.T) {
		t.Parallel()

		err := recovered(func() { pg().StandardCRUD("things; DROP TABLE users", []string{IDColumn}) })

		must.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		test.StrContains(t, err.Error(), "table name")
	})

	T.Run("a column name that is not an identifier", func(t *testing.T) {
		t.Parallel()

		err := recovered(func() { pg().StandardCRUD("things", []string{IDColumn, "na me"}) })

		must.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		test.StrContains(t, err.Error(), "column name")
	})

	T.Run("an ownership column that is not an identifier", func(t *testing.T) {
		t.Parallel()

		err := recovered(func() { pg().StandardCRUD("things", []string{IDColumn}, WithOwnership("belongs to")) })

		must.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
		test.StrContains(t, err.Error(), "ownership column")
	})

	T.Run("a column set with no id", func(t *testing.T) {
		t.Parallel()

		err := recovered(func() { pg().StandardCRUD("things", []string{"name"}) })

		must.ErrorIs(t, err, ErrMissingIDColumn)
	})

	T.Run("two queries renamed onto the same name", func(t *testing.T) {
		t.Parallel()

		err := recovered(func() {
			pg().StandardCRUD("things", []string{IDColumn}, WithQueryName(ListQuery, "GetThings"))
		})

		must.ErrorIs(t, err, ErrDuplicateQueryName)
		test.StrContains(t, err.Error(), "GetThings")
	})

	T.Run("a conventional table does not panic", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, recovered(func() {
			pg().StandardCRUD("valid_instruments", columnsFor(LastIndexedAtColumn, BelongsToAccountColumn),
				WithEntity("ValidInstrument", "ValidInstruments"),
				WithOwnership(BelongsToAccountColumn))
		}))
	})
}

func TestStandardQuery_String(T *testing.T) {
	T.Parallel()

	T.Run("names every query it can", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "create", CreateQuery.String())
		test.EqOp(t, "scan IDs for reindex", ScanIDsForReindexQuery.String())
		test.EqOp(t, "mark as indexed", MarkAsIndexedQuery.String())
		test.StrContains(t, StandardQuery(99).String(), "unknown")
	})
}

func TestCamel(T *testing.T) {
	T.Parallel()

	T.Run("upper camel cases a snake_case name", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "ValidInstruments", camel("valid_instruments"))
		test.EqOp(t, "Things", camel("things"))
		test.EqOp(t, "OAuth2Clients", camel("oAuth2_clients"))
	})

	T.Run("tolerates empty segments", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "AB", camel("a__b"))
		test.EqOp(t, "", camel(""))
	})
}
