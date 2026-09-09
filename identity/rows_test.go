package identity

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// What this file used to pin — placeholders matching arguments, projections
// matching scans, the update assigning only its own columns — is now the
// generated package's to guarantee and sqlc's to check; see
// identity/internal/identitydb and the canonical .sql beside
// identity/internal/queries. What is left to test here is the seam this
// package still owns: the dialect mapping, the filter window's translation
// into the generated params, and the conversions in rows.go.

// TestIdentitydbDialect pins the mapping onto the generated package's dialect
// set, for all three, and that the unknown case is an error naming the
// dialect rather than a working-looking querier.
func TestIdentitydbDialect(t *testing.T) {
	t.Parallel()

	for platform, generated := range map[dialect.Dialect]identitydb.Dialect{
		dialect.Postgres: identitydb.DialectPostgreSQL,
		dialect.MySQL:    identitydb.DialectMySQL,
		dialect.SQLite:   identitydb.DialectSQLite,
	} {
		mapped, err := identitydbDialect(platform)
		must.NoError(t, err, must.Sprintf("dialect %q", platform))
		test.EqOp(t, generated, mapped, test.Sprintf("dialect %q", platform))

		q, err := identitydb.New(mapped, "ddb_")
		must.NoError(t, err)
		must.NotNil(t, q)
	}

	_, err := identitydbDialect("oracle")
	must.ErrorIs(t, err, dialect.ErrUnsupported)
	must.StrContains(t, err.Error(), "oracle")
}

// TestWindowFrom pins the one reading of a filter every generated list binds:
// every field crosses, times cross in UTC, and absence stays absence.
func TestWindowFrom(t *testing.T) {
	t.Parallel()

	eastern := time.FixedZone("eastern", -5*60*60)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, eastern)

	filter := pageFilter(&filtering.QueryFilter{
		CreatedAfter:    pointer.To(at),
		CreatedBefore:   pointer.To(at.Add(time.Hour)),
		UpdatedAfter:    pointer.To(at),
		UpdatedBefore:   pointer.To(at.Add(time.Hour)),
		IncludeArchived: pointer.To(true),
		Cursor:          pointer.To("u1"),
		MaxResponseSize: pointer.To(uint16(20)),
	})

	w := windowFrom(filter)

	must.NotNil(t, w.createdAfter)
	test.EqOp(t, at.UTC(), *w.createdAfter)
	test.EqOp(t, time.UTC, w.createdAfter.Location())
	must.NotNil(t, w.createdBefore)
	must.NotNil(t, w.updatedAfter)
	must.NotNil(t, w.updatedBefore)
	test.True(t, w.includeArchived)
	must.NotNil(t, w.pageCursor)
	test.EqOp(t, "u1", *w.pageCursor)
	test.EqOp(t, int64(20), w.resultLimit)

	// The empty filter: nothing invented, the default page size, archived rows
	// excluded.
	empty := windowFrom(pageFilter(nil))
	test.Nil(t, empty.createdAfter)
	test.Nil(t, empty.createdBefore)
	test.Nil(t, empty.updatedAfter)
	test.Nil(t, empty.updatedBefore)
	test.Nil(t, empty.pageCursor)
	test.False(t, empty.includeArchived)
	test.EqOp(t, int64(filtering.DefaultQueryFilterLimit), empty.resultLimit)
}

// TestUserRowRoundTrip pins the conversion seam for the richest of the three
// entities: what the generated row carries is what the domain type reports,
// with every timestamp normalized to UTC and the named types restored.
func TestUserRowRoundTrip(t *testing.T) {
	t.Parallel()

	eastern := time.FixedZone("eastern", -5*60*60)
	created := time.Date(2026, 8, 20, 9, 30, 0, 0, eastern)
	verified := time.Date(2026, 8, 21, 10, 0, 0, 0, eastern)

	row := identitydb.GetUserRow{
		ID:                            "u1",
		Scope:                         tenancy.Of("dir"),
		Username:                      "ada",
		EmailAddress:                  "ada@example.com",
		FirstName:                     "Ada",
		LastName:                      "Lovelace",
		HashedPassword:                "hash",
		RequiresPasswordChange:        true,
		TwoFactorSecret:               "secret",
		EmailAddressVerifiedAt:        pointer.To(verified),
		EmailAddressVerificationToken: "token",
		AccountStatus:                 string(StatusGood),
		AccountStatusExplanation:      "fine",
		CreatedAt:                     created,
	}

	user := userFromRow(&row)

	test.EqOp(t, "u1", user.ID)
	test.EqOp(t, tenancy.Of("dir"), user.Scope)
	test.EqOp(t, StatusGood, user.AccountStatus)
	test.EqOp(t, created.UTC(), user.CreatedAt)
	test.EqOp(t, time.UTC, user.CreatedAt.Location())
	must.NotNil(t, user.EmailAddressVerifiedAt)
	test.EqOp(t, verified.UTC(), *user.EmailAddressVerifiedAt)
	test.Nil(t, user.PasswordLastChangedAt)
	test.Nil(t, user.ArchivedAt)

	// And back: the create params carry what the domain value holds, with the
	// named type spelled out the way the column stores it.
	params := createUserParams(user)
	test.EqOp(t, string(StatusGood), params.AccountStatus)
	test.EqOp(t, user.Scope, params.Scope)
	test.EqOp(t, user.HashedPassword, params.HashedPassword)

	// The list row is the same columns plus the page's two counts, converted
	// through the same function.
	page := userPageRow(&identitydb.ListUsersRow{
		ID: "u2", Scope: tenancy.Of("dir"), Username: "grace",
		AccountStatus: string(StatusGood),
		CreatedAt:     created,
		FilteredCount: 7, TotalCount: 9,
	})

	test.EqOp(t, "u2", page.value.ID)
	filtered, total := pageCounts(page)
	test.EqOp(t, int64(7), filtered)
	test.EqOp(t, int64(9), total)

	// The search row is those same columns with no counts beside them, because
	// the search's count is a statement of its own.
	found := userFromSearchRow(&identitydb.SearchUsersByUsernameRow{
		ID: "u3", Scope: tenancy.Of("dir"), Username: "hopper",
		EmailAddress:          "grace@example.com",
		AccountStatus:         string(StatusGood),
		CreatedAt:             created,
		PasswordLastChangedAt: pointer.To(verified),
	})

	test.EqOp(t, "u3", found.ID)
	test.EqOp(t, "hopper", found.Username)
	test.EqOp(t, "grace@example.com", found.EmailAddress)
	must.NotNil(t, found.PasswordLastChangedAt)
	test.EqOp(t, verified.UTC(), *found.PasswordLastChangedAt)
}

// TestSearchUsersParams pins what the two halves of the prefix search bind: an
// escaped pattern rather than the prefix somebody typed, the same one on both,
// and the window through the same clamp every other paged read goes through.
func TestSearchUsersParams(t *testing.T) {
	t.Parallel()

	scope := tenancy.Of("dir")

	// The wildcard is the user's literal, and the trailing one is the search.
	pattern := querygen.PrefixPattern("a%")
	test.EqOp(t, `a!%%`, pattern)

	search := searchUsersParams(scope, pattern, pageFilter(nil))
	test.EqOp(t, scope, search.Scope)
	test.EqOp(t, pattern, search.UsernamePrefix)
	test.EqOp(t, int64(filtering.DefaultQueryFilterLimit), search.ResultLimit)
	test.Nil(t, search.PageCursor)

	// The cursor is a username here, because the statement orders by that
	// column rather than by the id.
	cursored := searchUsersParams(scope, pattern, pageFilter(&filtering.QueryFilter{Cursor: pointer.To("ada")}))
	must.NotNil(t, cursored.PageCursor)
	test.EqOp(t, "ada", *cursored.PageCursor)

	count := countSearchUsersParams(scope, pattern)
	test.EqOp(t, scope, count.Scope)
	test.EqOp(t, search.UsernamePrefix, count.UsernamePrefix)
}

// TestListInvitationRows_RefusesAnUnkeyedColumn pins the closed switch: the two
// arms are the two canonical statements, and a third column is a programming
// error rather than a wider query — the same refusal the map of rendered
// statements used to make.
func TestListInvitationRows_RefusesAnUnkeyedColumn(t *testing.T) {
	t.Parallel()

	s := &SQLStore{}

	_, err := s.listInvitationRows(
		t.Context(), nil, "belongs_to_account", tenancy.Of("dir"), "a1", pageFilter(nil))
	must.Error(t, err)
	test.StrContains(t, err.Error(), "belongs_to_account")
}
