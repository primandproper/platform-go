package identity

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// ListAllAccountMembers reads an account's whole roster, walking
// [DirectoryReader.ListAccountMembers] page by page until it runs out, and
// answers as though the roster were one read.
//
// It is the convenience, not the contract, in the sense mediaregistry's
// ListObjectsByIDsInBatches is: a free function over the reader rather than a
// second method on it, so an implementer owes one paged read and not two, and
// so this composes over whichever implementation a consumer has. Nothing in
// Store knows it exists.
//
// It exists because the loop it replaces is easy to get wrong in a way that
// looks right. filtering's cursor is the last row's identifier, so it is empty
// only for a page that held no rows and says nothing about whether another page
// follows — a loop that stops on an empty cursor reads one page too many, and
// one that stops on a non-empty cursor reads one page. The end is a page
// shorter than the size that was applied, and the size applied is the page's
// own rather than the one asked for, because a reader that clamps to something
// smaller would otherwise make every page look short and end the walk after the
// first. And an account's members are bounded by nothing, which is why the
// roster is paged at all: the member a one-page read misses is the one whose
// membership a caller's policy then never considers.
//
// Pages are asked for at [filtering.MaxQueryFilterLimit], since nobody is
// rendering them. Members come back in the order the paged read gives them,
// membership id ascending, each carrying its roles.
//
// The pages run on the executor they are given and are not one read unless
// that executor is a transaction. A caller deciding something from the roster
// — whether an account has anybody left in it before DeleteAccount — passes the
// database.Tx the decision is made in, which is also what lets the walk see
// that transaction's own writes.
//
// A reader that hands back a full page carrying the cursor it was given would
// be read forever, and that is reported rather than turned into a short roster.
// Every other error is the reader's, returned as it came.
func ListAllAccountMembers(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	reader DirectoryReader,
	accountID string,
) ([]*MembershipWithUser, error) {
	if reader == nil {
		return nil, ErrNilStore
	}

	filter := filtering.DefaultQueryFilter()
	filter.MaxResponseSize = new(filtering.MaxQueryFilterLimit)

	var members []*MembershipWithUser

	for {
		page, err := reader.ListAccountMembers(ctx, q, scope, accountID, filter)
		if err != nil {
			return nil, err
		}

		members = append(members, page.Data...)

		applied := *filter.MaxResponseSize
		if page.MaxResponseSize > 0 {
			applied = page.MaxResponseSize
		}

		if page.Cursor == "" || len(page.Data) < int(applied) {
			return members, nil
		}

		if filter.Cursor != nil && page.Cursor == *filter.Cursor {
			return nil, platformerrors.Newf("identity account roster read repeated cursor %q", page.Cursor)
		}

		cursor := page.Cursor
		filter.SetCursor(&cursor)
	}
}
