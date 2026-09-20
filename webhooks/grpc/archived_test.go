package grpc_test

import (
	"context"
	"testing"

	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// grantsKey carries the authority a test wants its caller to have.
type grantsKey struct{}

// withGrants narrows a request context to the named grants, which is how a test
// describes a caller who may read and may not retire.
func withGrants(ctx context.Context, perms ...authorization.Permission) context.Context {
	return context.WithValue(ctx, grantsKey{}, authorization.NewGrants(authorization.NewPermissionSet(perms...)))
}

// extractGrants is the authorization.GrantsExtractor every harness here is built
// with.
//
// A context nothing narrowed carries every grant, because the rest of this suite
// is about what a handler does and not about what a policy allows — a default of
// "nothing" would make every existing test a test of this file.
func extractGrants(ctx context.Context) (authorization.Grants, bool) {
	grants, ok := ctx.Value(grantsKey{}).(authorization.Grants)
	if !ok {
		return authorization.AllowAll(), true
	}

	return grants, true
}

// includeArchived is the filter a client sets to ask for the retired rows.
func includeArchived() *filteringpb.QueryFilter {
	include := true

	return &filteringpb.QueryFilter{IncludeArchived: &include}
}

// retire archives a seeded endpoint through the store, so the row a test is
// about was retired without going through the surface the test is about.
func (h *harness) retire(tb testing.TB, endpointID string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.ArchiveEndpoint(tb.Context(), tx, testScope, endpointID)

		return err
	}))
}

// TestListEndpoints_Archived is the finding this file closes: include_archived
// arrived on the wire and reached the store untouched, so the read grant was
// enough to see the delivery targets a deployment had stopped trusting —
// their URLs included.
func TestListEndpoints_Archived(T *testing.T) {
	T.Parallel()

	T.Run("a caller without the archive grant is confined to the live rows", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seed(t, testScope)
		retired := h.seed(t, testScope)
		h.retire(t, retired.ID)

		ctx := withGrants(h.ctx(t), webhooksgrpc.PermissionReadEndpoints)

		res, err := h.server.ListEndpoints(ctx, &webhookspb.ListEndpointsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		ids := endpointIDs(res.GetResults())
		test.SliceContains(t, ids, live.ID)
		test.SliceNotContains(t, ids, retired.ID)
	})

	T.Run("a holder of the archive grant receives them", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		retired := h.seed(t, testScope)
		h.retire(t, retired.ID)

		ctx := withGrants(h.ctx(t),
			webhooksgrpc.PermissionReadEndpoints, webhooksgrpc.PermissionArchiveEndpoints)

		res, err := h.server.ListEndpoints(ctx, &webhookspb.ListEndpointsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.SliceContains(t, endpointIDs(res.GetResults()), retired.ID)
	})
}

func endpointIDs(in []*webhookspb.WebhookEndpoint) []string {
	ids := make([]string, 0, len(in))
	for _, e := range in {
		ids = append(ids, e.GetId())
	}

	return ids
}
