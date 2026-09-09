package queries

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/sessions/database/migrations"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyDialect is what the rendering assertions run against, because the
// interesting failures are the ones that are correct on two of the three.
var everyDialect = []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite}

// TestRender_MatchesTheCommittedFiles is the regeneration gate, run locally
// rather than only in CI.
//
// The .sql files are what sqlc is run over, and the whole value of running it is
// that they are the statements the backend executes. A hand-edit to one — or a
// column list changed without regenerating — would leave sqlc checking SQL
// nobody runs, which is a green check over an unchecked store.
func TestRender_MatchesTheCommittedFiles(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			committed, err := os.ReadFile(FileName(d))
			must.NoError(t, err)

			// The committed file carries the generated-code header, which is
			// the generator's rather than this function's.
			body := string(committed)
			if index := strings.Index(body, "-- name:"); index > 0 {
				body = body[index:]
			}

			test.EqOp(t, Render(d), body,
				test.Sprintf("run `make generate` and commit %s", FileName(d)))
		})
	}
}

// TestRender_EmitsTheStatementsTheBackendExecutes pins the set, since a query
// emitted here and not executed is SQL nobody checks the other way round: sqlc
// would be reading a statement the backend does not run.
//
// Nine, and there is no tenth hiding: this table has no filtered list to page
// in two directions and no archive to stamp, because it carries neither the
// convention triple nor anything a cursor could walk that a sweeper does not
// eventually delete. The enumeration below is unpaged for that reason and for
// one of its own — see heldRead.
func TestRender_EmitsTheStatementsTheBackendExecutes(T *testing.T) {
	T.Parallel()

	want := []string{
		"CreateSession", "GetSession", "UpdateSession",
		"SessionExists", "DeleteSession",
		"ListSessionsForPrincipal", "DeleteSessionForPrincipal", "DeleteSessionsForPrincipal",
		"SweepSessions",
	}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			rendered := Render(d)

			for _, name := range want {
				test.StrContains(t, rendered, "-- name: "+name+" ",
					test.Sprintf("%s is not in the %s corpus", name, d))
			}

			test.EqOp(t, len(want), strings.Count(rendered, "-- name: "),
				test.Sprintf("the %s corpus holds statements this list does not name", d))
		})
	}
}

// TestRender_RegistersEveryTable is the registry half of the same guarantee the
// canonical .sql files are the query half of.
//
// Nothing here calls querygen.Generator.StandardCRUD, which is what registers a
// conventional table, so without the explicit registration a consumer reading
// the registry back to truncate a database between integration tests would find
// no session table at all — and the symptom would be a different test failing
// later on rows the previous one left behind.
func TestRender_RegistersEveryTable(t *testing.T) {
	t.Parallel()

	for _, d := range everyDialect {
		_ = Render(d)
	}

	for _, table := range TableNames {
		test.True(t, querygen.TableRegistered(table), test.Sprintf("%s is not registered", table))
	}
}

// TestTableNames_AreTheTablesTheDDLCreates is the cross-check between the two
// halves of "what tables does this package own": the canonical spelling here,
// which the registry and the backend's prefix rendering both read, and the list
// migrations.Tables reads out of the DDL for a consumer.
//
// Neither derives from the other on purpose — one is a Go constant a statement
// interpolates, the other is read from the schema that creates the table — so
// this is where a table added to one and not the other stops being invisible.
func TestTableNames_AreTheTablesTheDDLCreates(t *testing.T) {
	t.Parallel()

	created, err := migrations.Tables("")
	must.NoError(t, err)

	declared := slices.Clone(TableNames)
	slices.Sort(declared)

	test.Eq(t, created, declared)
}

// TestColumns_AreTheColumnsTheDDLDeclares is the other cross-check, and the one
// a rename breaks first.
//
// The corpus binds these names and the DDL creates them, and nothing between
// the two would notice a column renamed in one and not the other until a server
// rejected a statement — which for the sweep is a background goroutine logging
// an error nobody reads.
func TestColumns_AreTheColumnsTheDDLDeclares(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			ddl, err := migrations.SQL(d, "")
			must.NoError(t, err)

			for _, column := range Columns {
				test.StrContains(t, ddl, column, test.Sprintf("%s is not in the %s schema", column, d))
			}
		})
	}
}

// TestUpdateColumns_NeverAssignCreatedAt is the structural half of "an update
// never extends a session's total lifetime".
//
// created_at anchors the absolute timeout. A touch, a payload save, or the
// write half of a renewal that moved it would make the timeout it anchors stop
// being absolute — so the column is not in the statement at all, and this is
// where that stays true.
func TestUpdateColumns_NeverAssignCreatedAt(T *testing.T) {
	T.Parallel()

	T.Run("the assignable set leaves the anchor out", func(t *testing.T) {
		t.Parallel()

		test.False(t, slices.Contains(UpdateColumns, querygen.CreatedAtColumn))
		test.False(t, slices.Contains(UpdateColumns, querygen.IDColumn))
	})

	T.Run("and so does every dialect's statement", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			update := statement(t, Render(d), "UpdateSession")

			test.StrNotContains(t, update, querygen.CreatedAtColumn, test.Sprintf("dialect %q", d))
		}
	})
}

// TestSweep_BindsItsDeadline covers the one predicate in this corpus that is
// not an equality, and the reason it binds rather than reading the server's
// clock is worth a test of its own: expires_at is stamped from the backend's
// injected clock, so CURRENT_TIMESTAMP here would be a second clock deciding
// one row.
func TestSweep_BindsItsDeadline(T *testing.T) {
	T.Parallel()

	T.Run("compares the deadline against a bound instant", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			sweep := statement(t, Render(d), "SweepSessions")

			test.StrContains(t, sweep, ExpiresAtColumn+" <= sqlc.arg("+SweepCutoffArg+")",
				test.Sprintf("dialect %q", d))
			test.StrNotContains(t, sweep, querygen.NowExpression, test.Sprintf("dialect %q", d))
		}
	})

	// The deadline instant itself is swept, matching the store's rule that a
	// session is over at its deadline rather than one moment after it.
	T.Run("includes the deadline instant", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			test.StrContains(t, statement(t, Render(d), "SweepSessions"), "<=",
				test.Sprintf("dialect %q", d))
		}
	})

	// A DELETE keyed on nothing empties the table. This one is keyed on the
	// deadline and on nothing else, which is what a sweep is — and an id
	// predicate here would make it a delete of one row wearing a sweep's name.
	T.Run("keys on the deadline and not on a row", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			sweep := statement(t, Render(d), "SweepSessions")

			test.StrNotContains(t, sweep, "id = ", test.Sprintf("dialect %q", d))
			test.EqOp(t, 1, strings.Count(sweep, "WHERE"), test.Sprintf("dialect %q", d))
		}
	})
}

// TestCreate_SkipsADuplicateRow covers the clause a taken identifier is
// reportable through.
//
// A duplicate primary key has to leave zero rows affected rather than raising a
// dialect-specific error, or reporting one would mean parsing three drivers'
// errors — and inside Rename's transaction, a constraint violation on Postgres
// would take the whole transaction down with it.
func TestCreate_SkipsADuplicateRow(T *testing.T) {
	T.Parallel()

	T.Run("in each dialect's own spelling", func(t *testing.T) {
		t.Parallel()

		pg := statement(t, Render(dialect.Postgres), "CreateSession")
		test.StrContains(t, pg, "ON CONFLICT (id) DO NOTHING")

		my := statement(t, Render(dialect.MySQL), "CreateSession")
		test.StrContains(t, my, "INSERT IGNORE INTO")
		test.StrNotContains(t, my, "ON CONFLICT")

		lite := statement(t, Render(dialect.SQLite), "CreateSession")
		test.StrContains(t, lite, "INSERT OR IGNORE INTO")
		test.StrNotContains(t, lite, "ON CONFLICT")
	})

	// The count is the answer, so the annotation has to be the one that reports
	// it: :exec would discard exactly the number a taken identifier is read
	// from.
	T.Run("and reports how many rows it wrote", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			test.StrContains(t, Render(d), "-- name: CreateSession :execrows",
				test.Sprintf("dialect %q", d))
		}
	})
}

// TestRead_ProjectsARecordRatherThanARow pins what comes back, since the scan
// side is generated from it: the id the caller already has and the column that
// exists for the sweeper's benefit are both left out.
func TestRead_ProjectsARecordRatherThanARow(T *testing.T) {
	T.Parallel()

	T.Run("leaves out the id and the deadline", func(t *testing.T) {
		t.Parallel()

		test.False(t, slices.Contains(RecordColumns, querygen.IDColumn))
		test.False(t, slices.Contains(RecordColumns, ExpiresAtColumn))
	})

	// Expiry belongs to the store, which decides it from the record's own
	// anchors, so that both backends answer the question identically and clock
	// skew between a writer and a reader cannot hide a live session.
	T.Run("and never mentions the deadline at all", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			get := statement(t, Render(d), "GetSession")

			test.StrNotContains(t, get, ExpiresAtColumn, test.Sprintf("dialect %q", d))
		}
	})
}

// TestHeldStatements_KeyOnTheWholeHolder is the test that would fail if a
// revocation ever reached across tenants or across people.
//
// Each of the three is keyed on the scope and the principal together, and the
// two failures a missing half produces are the ones nothing else here would
// catch: without the scope, a revocation reaches every tenant that spells an
// identifier the same way; without the principal, it signs out everybody in the
// tenant. Both are working SQL.
func TestHeldStatements_KeyOnTheWholeHolder(T *testing.T) {
	T.Parallel()

	held := []string{"ListSessionsForPrincipal", "DeleteSessionForPrincipal", "DeleteSessionsForPrincipal"}

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			for _, name := range held {
				rendered := statement(t, Render(d), name)

				for _, column := range AttributionColumns {
					test.StrContains(t, rendered, column+" = sqlc.arg("+column+")",
						test.Sprintf("%s does not bind %s", name, column))
				}
			}
		})
	}
}

// TestHeldRevocations_DifferInTheirIDPredicate pins the one thing that
// separates the three revocation shapes, since they are otherwise the same
// statement and a swap between them would still run.
func TestHeldRevocations_DifferInTheirIDPredicate(T *testing.T) {
	T.Parallel()

	// Revoking one session names it, and inclusively: the row that goes is the
	// one the caller asked about.
	T.Run("the single revocation matches the identifier", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			one := statement(t, Render(d), "DeleteSessionForPrincipal")

			test.StrContains(t, one, querygen.IDColumn+" = sqlc.arg("+querygen.IDColumn+")",
				test.Sprintf("dialect %q", d))
		}
	})

	// The bulk revocation excludes one, through an argument that may be unset —
	// which is what lets "sign out everywhere" and "sign out my other devices"
	// be one statement rather than two that could disagree about what "every
	// session I hold" means.
	T.Run("the bulk revocation excludes an optional identifier", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			all := statement(t, Render(d), "DeleteSessionsForPrincipal")

			test.StrContains(t, all, querygen.IDColumn+" <> COALESCE(sqlc.narg("+KeptSessionArg+"), '')",
				test.Sprintf("dialect %q", d))
			test.StrNotContains(t, all, querygen.IDColumn+" = sqlc.arg", test.Sprintf("dialect %q", d))
		}
	})
}

// TestHeldRead_IsUnpagedAndUnfiltered covers the two things the enumeration
// deliberately does not do.
//
// A page of a person's sessions would be a "sign out everywhere" that acted on
// the rows the reader happened to be looking at, and a predicate on expires_at
// would be the server's stored deadline deciding what a person is shown while
// the store decides what a request may use — two clocks, one of which is
// answering requests with sessions the other has hidden.
func TestHeldRead_IsUnpagedAndUnfiltered(T *testing.T) {
	T.Parallel()

	for _, d := range everyDialect {
		T.Run(string(d), func(t *testing.T) {
			t.Parallel()

			list := statement(t, Render(d), "ListSessionsForPrincipal")

			test.StrNotContains(t, list, ExpiresAtColumn)
			test.StrNotContains(t, list, "LIMIT")
			test.StrNotContains(t, list, "filtered_count")
			test.StrContains(t, list, "ORDER BY sessions."+querygen.CreatedAtColumn+" DESC")
		})
	}
}

// TestListedColumns_CarryTheIdentifier is the projection half of the same
// point.
//
// An enumeration that came back without identifiers would be a list a person
// could read and not act on: the identifier is what a revocation is aimed at,
// and what IsCurrent is decided by. It is the one read here that projects the
// id, precisely because it is the one the caller did not ask with.
func TestListedColumns_CarryTheIdentifier(t *testing.T) {
	t.Parallel()

	test.True(t, slices.Contains(ListedColumns, querygen.IDColumn))
	test.False(t, slices.Contains(ListedColumns, ExpiresAtColumn))

	for _, column := range AttributionColumns {
		test.False(t, slices.Contains(ListedColumns, column),
			test.Sprintf("%s is echoed back to a caller who bound it", column))
	}
}

// TestUpdateColumns_NeverAssignTheAttribution is the structural half of "a
// session does not change hands".
//
// Who holds a session and what it was established from are facts about its
// establishment, so no update can move them: a touch that reassigned a
// principal would be a session changing hands with no row deleted, which is the
// one thing a revocation surface cannot survive.
func TestUpdateColumns_NeverAssignTheAttribution(T *testing.T) {
	T.Parallel()

	immutable := append(slices.Clone(AttributionColumns),
		DeviceNameColumn, IPAddressColumn, UserAgentColumn, LoginMethodColumn)

	T.Run("the assignable set leaves them out", func(t *testing.T) {
		t.Parallel()

		for _, column := range immutable {
			test.False(t, slices.Contains(UpdateColumns, column), test.Sprintf("column %q", column))
		}
	})

	T.Run("and so does every dialect's statement", func(t *testing.T) {
		t.Parallel()

		for _, d := range everyDialect {
			update := statement(t, Render(d), "UpdateSession")

			where := strings.Index(update, "WHERE")
			must.GreaterEq(t, 0, where, must.Sprintf("the %s update has no WHERE clause", d))

			for _, column := range immutable {
				test.StrNotContains(t, update[:where], column,
					test.Sprintf("dialect %q, column %q", d, column))
			}
		}
	})
}

// statement returns one named statement out of a rendered corpus, including its
// annotation line.
func statement(t *testing.T, rendered, name string) string {
	t.Helper()

	marker := "-- name: " + name + " "

	start := strings.Index(rendered, marker)
	must.GreaterEq(t, 0, start, must.Sprintf("%s is not in the corpus", name))

	rest := rendered[start:]
	if next := strings.Index(rest[len(marker):], "-- name: "); next >= 0 {
		rest = rest[:len(marker)+next]
	}

	return rest
}
