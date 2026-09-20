package grpc_test

import (
	"context"
	"testing"

	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// grantsKey carries the authority a test wants its caller to have.
type grantsKey struct{}

// withGrants narrows a request context to the named grants, which is how a test
// describes a caller who may read and may not archive.
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

// dismiss retires a seeded notification through the store, so the row a test is
// about was dismissed without going through the surface the test is about.
func (h *harness) dismiss(tb testing.TB, principal, notificationID string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.ArchiveNotification(tb.Context(), tx, testScope, principal, notificationID)

		return err
	}))
}

// TestListNotifications_Archived is the finding this file closes: include_archived
// arrived on the wire and reached the store untouched, so the read grant was
// enough to see what only the archive grant dismisses.
func TestListNotifications_Archived(T *testing.T) {
	T.Parallel()

	T.Run("a caller without the archive grant is confined to the live rows", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		live := h.seedNotification(t, testScope, testPrincipal, "orders")
		dismissed := h.seedNotification(t, testScope, testPrincipal, "orders")
		h.dismiss(t, testPrincipal, dismissed.ID)

		ctx := withGrants(h.ctx(t, testPrincipal), notificationsgrpc.PermissionReadInbox)

		res, err := h.server.ListNotifications(ctx,
			&notificationspb.ListNotificationsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		ids := notificationIDs(res.GetResults())
		test.SliceContains(t, ids, live.ID)
		test.SliceNotContains(t, ids, dismissed.ID)
	})

	T.Run("a holder of the archive grant receives them", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		dismissed := h.seedNotification(t, testScope, testPrincipal, "orders")
		h.dismiss(t, testPrincipal, dismissed.ID)

		ctx := withGrants(h.ctx(t, testPrincipal),
			notificationsgrpc.PermissionReadInbox, notificationsgrpc.PermissionArchiveInbox)

		res, err := h.server.ListNotifications(ctx,
			&notificationspb.ListNotificationsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.SliceContains(t, notificationIDs(res.GetResults()), dismissed.ID)
	})

	// The fail-closed half of callerGrants' default.
	T.Run("a server with no grants extractor confines every read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, notificationsgrpc.WithGrantsExtractor(nil))
		dismissed := h.seedNotification(t, testScope, testPrincipal, "orders")
		h.dismiss(t, testPrincipal, dismissed.ID)

		res, err := h.server.ListNotifications(h.ctx(t, testPrincipal),
			&notificationspb.ListNotificationsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.SliceNotContains(t, notificationIDs(res.GetResults()), dismissed.ID)
	})
}

func notificationIDs(in []*notificationspb.Notification) []string {
	ids := make([]string, 0, len(in))
	for _, n := range in {
		ids = append(ids, n.GetId())
	}

	return ids
}
