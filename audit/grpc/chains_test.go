package grpc_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// actor is the second chain a signed-in person belongs to: a deployment that
// files logins under the actor rather than the account does it so that every
// login does not contend on one tenant's chain head.
var actor = tenancy.Of("user_ada")

// bothChains is the resolver such a deployment supplies — the account the
// connection resolved to, and the caller's own.
func bothChains() auditgrpc.ChainsResolver {
	return func(context.Context) ([]tenancy.Scope, error) {
		return []tenancy.Scope{ours, actor}, nil
	}
}

// seedInterleaved writes n entries alternating between the two chains, with
// identifiers that sort in write order so the merge has something to get wrong.
func seedInterleaved(t *testing.T, h *harness, n int) []string {
	t.Helper()

	ids := make([]string, 0, n)

	for i := range n {
		scope := ours
		if i%2 == 1 {
			scope = actor
		}

		id := fmt.Sprintf("entry_%03d", i)
		ids = append(ids, id)

		h.record(t, scope, &audit.Entry{
			ID:           id,
			EventType:    "thing.happened",
			ResourceType: "thing",
			ResourceID:   id,
			Actor:        audit.Actor{ID: "ada", Type: "user"},
			Scope:        scope,
		})
	}

	return ids
}

// TestListEntries_AcrossChains is the read a deployment with per-actor chains
// could not make: ListEntries binds one scope, so a caller's own logins lived
// in a chain no connection resolved to.
func TestListEntries_AcrossChains(T *testing.T) {
	T.Parallel()

	T.Run("one call returns both chains, in id order", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, auditgrpc.WithChainsResolver(bothChains()))
		want := seedInterleaved(t, h, 6)

		res, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{
			Query: ourEntries(),
		})
		must.NoError(t, err)

		test.Eq(t, want, entryIDs(res.GetResults()))
	})

	T.Run("absent, a read still spans one chain", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seedInterleaved(t, h, 6)

		res, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{
			Query: ourEntries(),
		})
		must.NoError(t, err)

		// Only the even-numbered ones, which are the account's.
		test.Eq(t, []string{"entry_000", "entry_002", "entry_004"}, entryIDs(res.GetResults()))
	})

	// The assertion the whole file exists for. Paging a merge is where the
	// cursor gets got wrong: advance one per chain and rows are dropped,
	// concatenate instead of merging and the page is ordered by chain. Neither
	// announces itself, so it is asserted rather than described.
	T.Run("paging the merge drops nothing and repeats nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, auditgrpc.WithChainsResolver(bothChains()))
		want := seedInterleaved(t, h, 10)

		var (
			got    []string
			cursor string
			size   = uint32(3)
		)

		for range 10 {
			res, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{
				Query:  ourEntries(),
				Filter: &filteringpb.QueryFilter{MaxResponseSize: &size, Cursor: &cursor},
			})
			must.NoError(t, err)

			page := entryIDs(res.GetResults())
			if len(page) == 0 {
				break
			}

			got = append(got, page...)
			cursor = res.GetPagination().GetCursor()
		}

		test.Eq(t, want, got, test.Sprint("the merged pages are not the whole log in order"))
	})

	T.Run("a page is never larger than it asked for", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, auditgrpc.WithChainsResolver(bothChains()))
		seedInterleaved(t, h, 10)

		size := uint32(4)

		res, err := h.client.ListEntries(h.asOurs(), &auditpb.ListEntriesRequest{
			Query:  ourEntries(),
			Filter: &filteringpb.QueryFilter{MaxResponseSize: &size},
		})
		must.NoError(t, err)

		// Without the truncation this is eight: a whole page from each chain.
		test.SliceLen(t, 4, res.GetResults())
	})
}

// ourEntries narrows a read to the rows these tests wrote. The harness seeds a
// log of its own, and a merge assertion that swept it up would be asserting
// about somebody else's fixture.
func ourEntries() *auditpb.EntryQuery {
	return &auditpb.EntryQuery{ResourceType: "thing"}
}

func entryIDs(in []*auditpb.Entry) []string {
	ids := make([]string, 0, len(in))
	for _, e := range in {
		ids = append(ids, e.GetId())
	}

	return ids
}
