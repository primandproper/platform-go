package grpc_test

import (
	"context"
	"testing"

	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// granting is an authorization.GrantsExtractor answering with exactly perms,
// which is how a test describes a caller who may read and may not withdraw.
func granting(perms ...authorization.Permission) authorization.GrantsExtractor {
	return func(context.Context) (authorization.Grants, bool) {
		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	}
}

// TestIncludeArchivedIsAGrantAndNotAField is the ruling every other paged
// surface already keeps, executed on this one: a withdrawn registration is in
// the page for a caller holding PermissionArchiveClients and for nobody else,
// whatever the request says.
func TestIncludeArchivedIsAGrantAndNotAField(T *testing.T) {
	T.Parallel()

	// page seeds a live registration and a withdrawn one, asks for the archive
	// as the server's grants describe the caller, and answers with the ids that
	// came back and the two seeded ones.
	page := func(t *testing.T, opts ...oauth2clientsgrpc.Option) (got []string, live, withdrawn string) {
		t.Helper()

		h := newHarness(t, opts...)

		kept := h.seed(t, "")
		gone := h.seed(t, "")
		must.NoError(t, h.svc.ArchiveClient(t.Context(), testScope, gone.ID))

		include := true

		res, err := h.server.ListOAuth2Clients(h.ctx(t, testOwner), &oauth2clientspb.ListOAuth2ClientsRequest{
			Filter: &filteringpb.QueryFilter{IncludeArchived: &include},
		})
		must.NoError(t, err)

		for _, c := range res.GetResults() {
			got = append(got, c.GetId())
		}

		return got, kept.ID, gone.ID
	}

	T.Run("a caller holding the read grant alone pages the live registry", func(t *testing.T) {
		t.Parallel()

		got, live, withdrawn := page(t, oauth2clientsgrpc.WithGrantsExtractor(
			granting(oauth2clientsgrpc.PermissionReadClients)))

		test.SliceContains(t, got, live)
		test.SliceNotContains(t, got, withdrawn,
			test.Sprint("include_archived handed the read grant a withdrawn registration"))
	})

	T.Run("a caller holding the withdrawal grant pages the archive too", func(t *testing.T) {
		t.Parallel()

		got, live, withdrawn := page(t, oauth2clientsgrpc.WithGrantsExtractor(
			granting(oauth2clientsgrpc.PermissionReadClients, oauth2clientsgrpc.PermissionArchiveClients)))

		test.SliceContains(t, got, live)
		test.SliceContains(t, got, withdrawn,
			test.Sprint("a caller entitled to the archive asked for it and was answered without it"))
	})

	// The fail-closed default: a server that cannot see what the caller may do
	// cannot tell who is entitled to the archive, so nobody is.
	T.Run("a server built with no extractor pages the live registry for everybody", func(t *testing.T) {
		t.Parallel()

		got, live, withdrawn := page(t)

		test.SliceContains(t, got, live)
		test.SliceNotContains(t, got, withdrawn)
	})
}
