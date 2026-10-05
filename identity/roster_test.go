package identity_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v15/identity"
	identitymock "github.com/primandproper/platform-go/v15/identity/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// pagedRoster answers ListAccountMembers the way the SQL store does — ordered
// by membership id, resuming after the cursor, the cursor being the last row's
// id — over a roster of n members, clamping every page to pageSize.
//
// The clamp is what lets a test reach "more than a page" without writing a
// ceiling's worth of rows, and it is also the case the walk has to read the
// page's own size for: a reader that answers with fewer rows than were asked
// for, every time.
func pagedRoster(n int, pageSize uint16) (*identitymock.StoreMock, *[]*filtering.QueryFilter) {
	roster := make([]*identity.MembershipWithUser, 0, n)
	for i := range n {
		member := &identity.MembershipWithUser{}
		member.ID = fmt.Sprintf("membership_%04d", i)
		roster = append(roster, member)
	}

	var asked []*filtering.QueryFilter

	store := &identitymock.StoreMock{
		ListAccountMembersFunc: func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			_ tenancy.Scope,
			_ string,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.MembershipWithUser], error) {
			snapshot := *filter
			asked = append(asked, &snapshot)

			start := 0
			if filter.Cursor != nil {
				for start < len(roster) && roster[start].ID <= *filter.Cursor {
					start++
				}
			}

			end := min(start+int(pageSize), len(roster))
			page := roster[start:end]

			result := &filtering.QueryFilteredResult[identity.MembershipWithUser]{Data: page}
			result.MaxResponseSize = pageSize

			if len(page) > 0 {
				result.Cursor = page[len(page)-1].ID
			}

			return result, nil
		},
	}

	return store, &asked
}

func TestListAllAccountMembers(T *testing.T) {
	T.Parallel()

	T.Run("reaches every member of a roster longer than a page", func(t *testing.T) {
		t.Parallel()

		const pageSize = 3

		for _, n := range []int{0, 1, pageSize - 1, pageSize, pageSize + 1, 2 * pageSize, 2*pageSize + 1} {
			t.Run(fmt.Sprintf("%d members", n), func(t *testing.T) {
				t.Parallel()

				store, asked := pagedRoster(n, pageSize)

				members, err := identity.ListAllAccountMembers(t.Context(), nil, tenancy.Global(), store, "acct")
				must.NoError(t, err)
				must.SliceLen(t, n, members)

				// Each exactly once and in order: a cursor that did not advance
				// would repeat the first page, and one that skipped would lose a
				// member between pages.
				for i, member := range members {
					test.EqOp(t, fmt.Sprintf("membership_%04d", i), member.ID)
				}

				// A full last page costs one more read to learn it was the
				// last, and nothing else does.
				test.SliceLen(t, n/pageSize+1, *asked)

				// Every page asks for the ceiling, since nobody is rendering it.
				for _, filter := range *asked {
					test.EqOp(t, filtering.MaxQueryFilterLimit, *filter.MaxResponseSize)
				}
			})
		}
	})

	T.Run("refuses a reader whose full page repeats its cursor", func(t *testing.T) {
		t.Parallel()

		member := &identity.MembershipWithUser{}
		member.ID = "membership_0000"

		store := &identitymock.StoreMock{
			ListAccountMembersFunc: func(
				context.Context,
				database.SQLQueryExecutor,
				tenancy.Scope,
				string,
				*filtering.QueryFilter,
			) (*filtering.QueryFilteredResult[identity.MembershipWithUser], error) {
				result := &filtering.QueryFilteredResult[identity.MembershipWithUser]{
					Data: []*identity.MembershipWithUser{member},
				}
				result.MaxResponseSize = 1
				result.Cursor = member.ID

				return result, nil
			},
		}

		members, err := identity.ListAllAccountMembers(t.Context(), nil, tenancy.Global(), store, "acct")
		must.Error(t, err)
		test.Nil(t, members)

		// Two reads: the first page, and the one that handed it back again.
		test.SliceLen(t, 2, store.ListAccountMembersCalls())
	})

	T.Run("returns the reader's error and nothing it collected", func(t *testing.T) {
		t.Parallel()

		boom := errors.New("boom")
		store, _ := pagedRoster(10, 3)

		read := store.ListAccountMembersFunc
		store.ListAccountMembersFunc = func(
			ctx context.Context,
			q database.SQLQueryExecutor,
			scope tenancy.Scope,
			accountID string,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[identity.MembershipWithUser], error) {
			if filter.Cursor != nil {
				return nil, boom
			}

			return read(ctx, q, scope, accountID, filter)
		}

		members, err := identity.ListAllAccountMembers(t.Context(), nil, tenancy.Global(), store, "acct")
		must.ErrorIs(t, err, boom)
		test.Nil(t, members)
	})

	T.Run("passes the scope, account and executor through", func(t *testing.T) {
		t.Parallel()

		store, _ := pagedRoster(1, 3)
		scope := tenancy.Of("dir_1")

		_, err := identity.ListAllAccountMembers(t.Context(), nil, scope, store, "acct_1")
		must.NoError(t, err)

		calls := store.ListAccountMembersCalls()
		must.SliceLen(t, 1, calls)
		test.EqOp(t, scope, calls[0].Scope)
		test.EqOp(t, "acct_1", calls[0].AccountID)
	})

	T.Run("refuses a nil reader", func(t *testing.T) {
		t.Parallel()

		_, err := identity.ListAllAccountMembers(t.Context(), nil, tenancy.Global(), nil, "acct")
		must.ErrorIs(t, err, identity.ErrNilStore)
	})
}
