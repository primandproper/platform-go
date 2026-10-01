package identity

import (
	"context"
	"strconv"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"google.golang.org/grpc"
)

// pagedDirectory answers ListAccounts two accounts to a page, the cursor being
// the index of the next row.
type pagedDirectory struct {
	identitypb.IdentityServiceClient

	accounts []*identitypb.Account
	requests []*identitypb.ListAccountsRequest
}

func (d *pagedDirectory) ListAccounts(
	_ context.Context,
	req *identitypb.ListAccountsRequest,
	_ ...grpc.CallOption,
) (*identitypb.ListAccountsResponse, error) {
	d.requests = append(d.requests, req)

	start := 0
	if req.GetFilter() != nil && req.GetFilter().Cursor != nil {
		start, _ = strconv.Atoi(req.GetFilter().GetCursor())
	}

	end := min(start+2, len(d.accounts))

	page := &identitypb.ListAccountsResponse{
		Results:    d.accounts[start:end],
		Pagination: &filteringpb.Pagination{},
	}
	if end < len(d.accounts) {
		page.Pagination.Cursor = strconv.Itoa(end)
	}

	return page, nil
}

func TestDirectoryAccounts(t *testing.T) {
	t.Parallel()

	t.Run("an owner's account past the first page of a shared directory is found", func(t *testing.T) {
		t.Parallel()

		directory := &pagedDirectory{accounts: []*identitypb.Account{
			{Id: "a", OwnerUserId: "someone"},
			{Id: "b", OwnerUserId: "someone"},
			{Id: "c", OwnerUserId: "somebody"},
			{Id: "d", OwnerUserId: "somebody"},
			{Id: "mine", OwnerUserId: "owner"},
		}}
		operator := &conformance.Subject{Surfaces: conformance.Surfaces{Identity: directory}}

		listed := directoryAccounts(t, operator)

		test.Eq(t, []string{"a", "b", "c", "d", "mine"}, accountIDs(listed))
		test.Eq(t, []string{"mine"}, ownedBy(listed, "owner"))
		test.SliceLen(t, 3, directory.requests)

		// The first request carries no cursor at all, because an empty
		// cursor is a cursor.
		test.True(t, directory.requests[0].GetFilter() == nil)
	})

	t.Run("a walk that asks for closed accounts asks on every page", func(t *testing.T) {
		t.Parallel()

		directory := &pagedDirectory{accounts: []*identitypb.Account{
			{Id: "a", OwnerUserId: "someone"},
			{Id: "b", OwnerUserId: "someone"},
			{Id: "c", OwnerUserId: "somebody"},
			{Id: "d", OwnerUserId: "somebody"},
			{Id: "mine", OwnerUserId: "owner"},
		}}
		operator := &conformance.Subject{Surfaces: conformance.Surfaces{Identity: directory}}

		test.Eq(t, []string{"a", "b", "c", "d", "mine"}, accountIDs(closedAccountsToo(t, operator)))
		test.SliceLen(t, 3, directory.requests)

		// A cursor that dropped the question would page the archive on the
		// first page and the live directory on every page after it.
		for i, request := range directory.requests {
			test.True(t, request.GetFilter().GetIncludeArchived(),
				test.Sprintf("page %d did not ask for closed accounts", i))
		}
	})

	t.Run("a directory that fits one page is read once", func(t *testing.T) {
		t.Parallel()

		directory := &pagedDirectory{accounts: []*identitypb.Account{{Id: "mine", OwnerUserId: "owner"}}}
		operator := &conformance.Subject{Surfaces: conformance.Surfaces{Identity: directory}}

		test.Eq(t, []string{"mine"}, accountIDs(directoryAccounts(t, operator)))
		test.SliceLen(t, 1, directory.requests)
	})
}
