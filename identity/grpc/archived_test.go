package grpc_test

import (
	"context"
	"testing"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// granting is an authorization.GrantsExtractor answering with exactly perms,
// which is how a test describes a caller who may read and may not archive.
func granting(perms ...authorization.Permission) authorization.GrantsExtractor {
	return func(context.Context) (authorization.Grants, bool) {
		return authorization.NewGrants(authorization.NewPermissionSet(perms...)), true
	}
}

// includeArchived is the filter a client sets to ask for the archived rows.
func includeArchived() *filteringpb.QueryFilter {
	include := true

	return &filteringpb.QueryFilter{IncludeArchived: &include}
}

// TestIncludeArchivedIsAGrantAndNotAField is the ruling every other paged
// surface already keeps, executed on the directory: an archived user or a
// closed account is in a page for a caller holding the grant that archives it,
// and for nobody else, whatever the request says.
func TestIncludeArchivedIsAGrantAndNotAField(T *testing.T) {
	T.Parallel()

	// Each read seeds one live row and one archived one, asks for the archive,
	// and answers with the ids that came back and the two it seeded.
	reads := map[string]struct {
		read  func(t *testing.T, h *harness) (got []string, live, archived string)
		grant authorization.Permission
	}{
		"ListUsers": {
			grant: identitygrpc.PermissionArchiveUsers,
			read: func(t *testing.T, h *harness) (got []string, live, archived string) {
				t.Helper()

				kept := h.seedUser(t, testScope, "kept")
				gone := h.seedUser(t, testScope, "gone")

				must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
					_, err := h.store.ArchiveUser(t.Context(), tx, testScope, gone.ID)

					return err
				}))

				page, err := h.client.ListUsers(h.ctx(), &identitypb.ListUsersRequest{Filter: includeArchived()})
				must.NoError(t, err)

				for _, u := range page.GetResults() {
					got = append(got, u.GetId())
				}

				return got, kept.ID, gone.ID
			},
		},
		"ListAccounts": {
			grant: identitygrpc.PermissionArchiveAccounts,
			read: func(t *testing.T, h *harness) (got []string, live, archived string) {
				t.Helper()

				kept := h.seedAccount(t, testScope, "kept")
				gone := h.seedAccount(t, testScope, "gone")

				must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
					_, err := h.store.ArchiveAccount(t.Context(), tx, testScope, gone.Account.ID)

					return err
				}))

				page, err := h.client.ListAccounts(h.ctx(), &identitypb.ListAccountsRequest{Filter: includeArchived()})
				must.NoError(t, err)

				for _, a := range page.GetResults() {
					got = append(got, a.GetId())
				}

				return got, kept.Account.ID, gone.Account.ID
			},
		},
	}

	for name, read := range reads {
		T.Run(name+" hides the archived rows from a caller who cannot archive", func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, identitygrpc.WithGrantsExtractor(granting(identitygrpc.PermissionReadUsers,
				identitygrpc.PermissionReadAccounts, identitygrpc.PermissionListAllAccounts)))

			got, live, archived := read.read(t, h)

			test.SliceContains(t, got, live)
			test.SliceNotContains(t, got, archived,
				test.Sprint("include_archived handed the read grant an archived row"))
		})

		T.Run(name+" shows the archived rows to a caller who can archive them", func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, identitygrpc.WithGrantsExtractor(granting(read.grant)))

			got, live, archived := read.read(t, h)

			test.SliceContains(t, got, live)
			test.SliceContains(t, got, archived,
				test.Sprint("a caller entitled to the archive asked for it and was answered without it"))
		})

		// The fail-closed default: a server that cannot see what the caller may
		// do cannot tell who is entitled to the archive, so nobody is.
		T.Run(name+" hides the archived rows on a server built with no extractor", func(t *testing.T) {
			t.Parallel()

			got, live, archived := read.read(t, newHarness(t))

			test.SliceContains(t, got, live)
			test.SliceNotContains(t, got, archived)
		})
	}
}
