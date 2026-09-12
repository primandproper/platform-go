package oauth2clients

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/internal/oauth2clientsdb"

	"github.com/primandproper/primitives-go/v2/filtering"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestEncodeStringsAndDecodeStringsRoundTrip pins the pair as a pair.
//
// They are a private encoding shared with authentication/oauth2serverstore,
// so what matters is that a value written here reads back as itself — a
// half-fixed pair is two columns holding two different shapes with nothing
// saying so.
func TestEncodeStringsAndDecodeStringsRoundTrip(T *testing.T) {
	T.Parallel()

	for name, values := range map[string][]string{
		"one":                      {"https://example.test/callback"},
		"several":                  {"read", "write", "offline_access"},
		"one that needs escaping":  {`https://example.test/cb?a="b"&c=\d`},
		"one that is empty itself": {""},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			decoded, err := decodeStrings(encodeStrings(values))
			must.NoError(t, err)
			test.Eq(t, values, decoded)
		})
	}
}

// TestEncodeStringsWritesValidJSONForNothing is why a reader never has a third
// case to handle.
//
// An empty column would be a second spelling of "no redirect URIs" beside "[]",
// and the two would be indistinguishable from a column something outside this
// package wrote.
func TestEncodeStringsWritesValidJSONForNothing(T *testing.T) {
	T.Parallel()

	test.EqOp(T, "[]", encodeStrings(nil))
	test.EqOp(T, "[]", encodeStrings([]string{}))
}

// TestDecodeStringsReadsBothSpellingsOfEmpty is the other half: this store
// writes "[]", and a row written before it — or by hand — may hold "".
//
// Both are nil rather than an empty slice, because to every caller in this
// package they are the same thing and a caller that had to tell them apart would
// be reading the column's history rather than its value.
func TestDecodeStringsReadsBothSpellingsOfEmpty(T *testing.T) {
	T.Parallel()

	for _, encoded := range []string{"", "[]"} {
		values, err := decodeStrings(encoded)
		must.NoError(T, err)
		test.Nil(T, values)
	}
}

// TestDecodeStringsRefusesAColumnItCannotRead is the one fallible step in the
// conversion, and the reason clientFromRow returns an error at all.
//
// A redirect_uris column that is not a JSON array is a row something outside
// this package wrote. Answering with no redirect URIs would leave a registration
// that matches nothing and reads as though it were registered that way.
func TestDecodeStringsRefusesAColumnItCannotRead(T *testing.T) {
	T.Parallel()

	for name, encoded := range map[string]string{
		"not JSON":          "https://example.test/callback",
		"a JSON object":     `{"uri":"https://example.test/callback"}`,
		"a list of numbers": `[1, 2, 3]`,
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			values, err := decodeStrings(encoded)
			must.Error(t, err)
			test.Nil(t, values)
		})
	}
}

// TestClientFromRowCarriesEveryColumn is the conversion every other read
// converts into, so a field it dropped would be dropped from all of them.
func TestClientFromRowCarriesEveryColumn(T *testing.T) {
	T.Parallel()

	updated := time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)
	archived := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

	client, err := clientFromRow(&oauth2clientsdb.GetRegisteredClientRow{
		ID:            "row_1",
		Scope:         testScope,
		BelongsToUser: testOwner,
		Name:          "test client",
		Description:   "for the suite",
		ClientID:      "cid_1",
		SecretHash:    "digest",
		RedirectUris:  encodeStrings([]string{testRedirect}),
		Scopes:        encodeStrings([]string{"read", "write"}),
		CreatedAt:     updated.Add(-time.Hour),
		LastUpdatedAt: &updated,
		ArchivedAt:    &archived,
	})
	must.NoError(T, err)
	must.NotNil(T, client)

	test.EqOp(T, "row_1", client.ID)
	test.EqOp(T, testScope, client.Scope)
	test.EqOp(T, testOwner, client.BelongsToUser)
	test.EqOp(T, "test client", client.Name)
	test.EqOp(T, "for the suite", client.Description)
	test.EqOp(T, "cid_1", client.ClientID)
	test.EqOp(T, "digest", client.SecretHash)
	test.Eq(T, []string{testRedirect}, client.RedirectURIs)
	test.Eq(T, []string{"read", "write"}, client.Scopes)
	test.EqOp(T, updated.Add(-time.Hour), client.CreatedAt)
	must.NotNil(T, client.LastUpdatedAt)
	test.EqOp(T, updated, *client.LastUpdatedAt)
	test.True(T, client.Archived())
}

// TestClientFromArchivedRowIsTheSameProjection pins the cast the archive's
// read-back rides on.
//
// The two statements project the same list in the same order and differ only in
// which rows they will look at, so the conversion is what fails to build the day
// they stop agreeing. What the case asserts beyond that is the field the archive
// exists to deliver: a row that carries archived_at is a row that says when the
// credential stopped working.
func TestClientFromArchivedRowIsTheSameProjection(T *testing.T) {
	T.Parallel()

	archived := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

	client, err := clientFromArchivedRow(&oauth2clientsdb.GetArchivedRegisteredClientRow{
		ID:            "row_1",
		Scope:         testScope,
		BelongsToUser: testOwner,
		Name:          "test client",
		ClientID:      "cid_1",
		SecretHash:    "digest",
		RedirectUris:  encodeStrings([]string{testRedirect}),
		Scopes:        encodeStrings([]string{"read"}),
		CreatedAt:     archived.Add(-time.Hour),
		ArchivedAt:    &archived,
	})
	must.NoError(T, err)
	must.NotNil(T, client)

	test.EqOp(T, "row_1", client.ID)
	test.EqOp(T, testScope, client.Scope)
	test.EqOp(T, "cid_1", client.ClientID)
	test.Eq(T, []string{testRedirect}, client.RedirectURIs)
	test.True(T, client.Archived(),
		test.Sprint("the archive's read-back reported a live registration"))

	// The digest travels and the plaintext cannot: the row has never held one,
	// so neither does anything a write hands back.
	test.EqOp(T, "digest", client.SecretHash)
}

// TestClientFromRowReportsAnUnreadableList is the failure surfacing rather than
// being converted into a registration with no addresses.
func TestClientFromRowReportsAnUnreadableList(T *testing.T) {
	T.Parallel()

	T.Run("redirect URIs", func(t *testing.T) {
		t.Parallel()

		client, err := clientFromRow(&oauth2clientsdb.GetRegisteredClientRow{
			RedirectUris: "not json",
			Scopes:       "[]",
		})
		must.Error(t, err)
		test.Nil(t, client)
	})

	T.Run("scopes", func(t *testing.T) {
		t.Parallel()

		// Separately, because they are two decodes and a fix applied to the
		// first is not a fix applied to the second.
		client, err := clientFromRow(&oauth2clientsdb.GetRegisteredClientRow{
			RedirectUris: "[]",
			Scopes:       "not json",
		})
		must.Error(t, err)
		test.Nil(t, client)
	})
}

// TestClientPageRowCarriesTheCountsBesideTheValue is what makes a page and the
// number describing it come from one snapshot of the table.
//
// The counts ride on the rows rather than arriving from a second query, so a row
// conversion that dropped them would leave filtering.Drain reporting a page's
// size as its total.
func TestClientPageRowCarriesTheCountsBesideTheValue(T *testing.T) {
	T.Parallel()

	row, err := clientPageRow(&oauth2clientsdb.ListRegisteredClientsRow{
		ID:            "row_1",
		Scope:         testScope,
		BelongsToUser: testOwner,
		Name:          "test client",
		ClientID:      "cid_1",
		SecretHash:    "digest",
		RedirectUris:  encodeStrings([]string{testRedirect}),
		Scopes:        "[]",
		FilteredCount: 3,
		TotalCount:    7,
	})
	must.NoError(T, err)

	must.NotNil(T, row.value)
	test.EqOp(T, "row_1", row.value.ID)

	filtered, total := pageCounts(row)
	test.EqOp(T, int64(3), filtered)
	test.EqOp(T, int64(7), total)

	test.EqOp(T, row.value, pageValue(row))
}

// TestClientPageRowReportsAnUnreadableList keeps the wider row's conversion from
// swallowing what the narrower one reports.
func TestClientPageRowReportsAnUnreadableList(T *testing.T) {
	T.Parallel()

	row, err := clientPageRow(&oauth2clientsdb.ListRegisteredClientsRow{RedirectUris: "not json"})
	must.Error(T, err)
	test.Nil(T, row.value)
}

// TestSortedRowsRunsTheStatementTheFilterNames is the pick this helper exists to
// make once rather than at each of the two paged reads.
//
// A direction is which way the ORDER BY runs and which way the cursor comparison
// points — statement text on all three engines, not a bound value — so a read
// that reached for the ascending statement while holding a descending filter
// would answer in the order the client did not ask for, and nothing about the
// rows that came back would say so.
func TestSortedRowsRunsTheStatementTheFilterNames(T *testing.T) {
	T.Parallel()

	// Two distinguishable row types, so the assertion is about which statement
	// ran rather than about what it returned.
	type ascending struct{ from string }

	type descending struct{ from string }

	// A conversion rather than a field-by-field restatement, which is the point
	// sortedRows's own callers make: the two projections are one shape
	// rendered twice, so Go is what asserts they have not diverged.
	same := func(d descending) ascending { return ascending(d) }

	run := func(t *testing.T, filter *filtering.QueryFilter) []ascending {
		t.Helper()

		rows, err := sortedRows(filter,
			func() ([]ascending, error) { return []ascending{{from: "ascending"}}, nil },
			func() ([]descending, error) { return []descending{{from: "descending"}}, nil },
			same)
		must.NoError(t, err)
		must.SliceLen(t, 1, rows)

		return rows
	}

	T.Run("an ascending filter runs the ascending statement", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "ascending", run(t, filtering.DefaultQueryFilter())[0].from)
	})

	T.Run("a descending filter runs the descending statement", func(t *testing.T) {
		t.Parallel()

		filter := filtering.DefaultQueryFilter()
		filter.SortBy = filtering.SortDescending

		test.EqOp(t, "descending", run(t, filter)[0].from)
	})

	T.Run("either statement's failure is the answer", func(t *testing.T) {
		t.Parallel()

		filter := filtering.DefaultQueryFilter()
		filter.SortBy = filtering.SortDescending

		rows, err := sortedRows(filter,
			func() ([]ascending, error) { return nil, ErrClientNotFound },
			func() ([]descending, error) { return nil, ErrClientNotFound },
			same)
		must.Error(t, err)
		test.ErrorIs(t, err, ErrClientNotFound)
		test.Nil(t, rows)
	})
}

// TestPageFilterAlwaysBoundsThePage is why the generated statements can bind the
// limit as a plain int64 rather than a nullable.
//
// A page with no limit is a table scan the caller did not ask for.
func TestPageFilterAlwaysBoundsThePage(T *testing.T) {
	T.Parallel()

	T.Run("a nil filter becomes the default one", func(t *testing.T) {
		t.Parallel()

		bounded := pageFilter(nil)
		must.NotNil(t, bounded)
		must.NotNil(t, bounded.MaxResponseSize)
		test.EqOp(t, uint16(filtering.DefaultQueryFilterLimit), *bounded.MaxResponseSize)
	})

	T.Run("a filter naming no page size gets the default", func(t *testing.T) {
		t.Parallel()

		bounded := pageFilter(&filtering.QueryFilter{})
		must.NotNil(t, bounded.MaxResponseSize)
		test.EqOp(t, uint16(filtering.DefaultQueryFilterLimit), *bounded.MaxResponseSize)
	})

	T.Run("a page size above the ceiling clamps to it", func(t *testing.T) {
		t.Parallel()

		asked := uint16(60000)

		bounded := pageFilter(&filtering.QueryFilter{MaxResponseSize: &asked})
		must.NotNil(t, bounded.MaxResponseSize)
		test.EqOp(t, filtering.MaxQueryFilterLimit, *bounded.MaxResponseSize)
	})

	T.Run("it does not write through to the caller's filter", func(t *testing.T) {
		t.Parallel()

		// The caller's filter is theirs. Bounding it in place would change a
		// value they may describe the page with afterwards.
		caller := &filtering.QueryFilter{}

		_ = pageFilter(caller)

		test.Nil(t, caller.MaxResponseSize)
	})
}

// TestWindowFromNormalizesEveryBoundTimeToUTC is load-bearing on SQLite rather
// than cosmetic.
//
// That column compares as text, the stored shape is UTC `YYYY-MM-DD HH:MM:SS`,
// and the driver renders a bound time.Time with its own zone's clock in exactly
// that prefix position — so a UTC value compares correctly to the second and a
// zoned one is off by its offset, silently, excluding rows nobody would think to
// look for.
func TestWindowFromNormalizesEveryBoundTimeToUTC(T *testing.T) {
	T.Parallel()

	// A zone with a whole-hour offset, so a comparison that kept it would be
	// wrong by an amount a reader can see.
	zone := time.FixedZone("UTC+7", 7*60*60)
	instant := time.Date(2026, time.September, 7, 12, 0, 0, 0, zone)

	filter := filtering.DefaultQueryFilter()
	filter.CreatedAfter = &instant
	filter.CreatedBefore = &instant
	filter.UpdatedAfter = &instant
	filter.UpdatedBefore = &instant

	window := windowFrom(filter)

	for name, bound := range map[string]*time.Time{
		"created after":  window.createdAfter,
		"created before": window.createdBefore,
		"updated after":  window.updatedAfter,
		"updated before": window.updatedBefore,
	} {
		must.NotNil(T, bound, must.Sprintf("%s was dropped", name))

		test.EqOp(T, time.UTC, bound.Location(), test.Sprintf("%s is not in UTC", name))

		// Normalized rather than shifted: the instant is the same one.
		test.True(T, bound.Equal(instant), test.Sprintf("%s names a different instant", name))
	}
}

// TestWindowFromLeavesAnUnnamedBoundUnset keeps an absent window from becoming a
// window nobody asked for.
func TestWindowFromLeavesAnUnnamedBoundUnset(T *testing.T) {
	T.Parallel()

	window := windowFrom(filtering.DefaultQueryFilter())

	test.Nil(T, window.createdAfter)
	test.Nil(T, window.createdBefore)
	test.Nil(T, window.updatedAfter)
	test.Nil(T, window.updatedBefore)
	test.Nil(T, window.pageCursor)

	// The limit is always bound, because pageFilter has already run.
	test.EqOp(T, int64(filtering.DefaultQueryFilterLimit), window.resultLimit)
}

// TestWindowFromDefaultsToExcludingArchivedRows pins the one field that defaults
// here, and it defaults to the answer the statement's COALESCE would have
// reached anyway — bound explicitly so the parameter is a bool rather than a
// pointer whose nil means the same thing.
func TestWindowFromDefaultsToExcludingArchivedRows(T *testing.T) {
	T.Parallel()

	test.False(T, windowFrom(filtering.DefaultQueryFilter()).includeArchived)

	included := filtering.DefaultQueryFilter()
	included.IncludeArchived = new(true)

	test.True(T, windowFrom(included).includeArchived)
}

// TestUTCPtrLeavesNothingAsNothing: an absent bound must stay absent, or the
// statement gets a window the caller never named.
func TestUTCPtrLeavesNothingAsNothing(T *testing.T) {
	T.Parallel()

	test.Nil(T, utcPtr(nil))
}
