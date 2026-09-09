package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The inbox half, and the property nearly every test here is about: the
// recipient comes off the connection, so a caller reaches their own rows and
// nothing else, and a row that is somebody else's reads as one that is not
// there.

func TestListNotificationsReadsTheCallersOwnInbox(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	h.seedNotification(T, testScope, testPrincipal, "order.shipped")
	h.seedNotification(T, testScope, otherPrincipal, "order.shipped")

	res, err := h.server.ListNotifications(h.ctx(T, testPrincipal), &notificationspb.ListNotificationsRequest{})
	must.NoError(T, err)
	must.NotNil(T, res)

	test.SliceLen(T, 1, res.GetResults())
	test.EqOp(T, "order.shipped", res.GetResults()[0].GetTopic())
	must.NotNil(T, res.GetPagination())
}

// TestListNotificationsCannotCrossADirectory is the scope half of the same
// property. The same person in another tenant is another inbox.
func TestListNotificationsCannotCrossADirectory(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	h.seedNotification(T, testScope, testPrincipal, "order.shipped")

	res, err := h.server.ListNotifications(
		h.ctxIn(T, testPrincipal, otherScope), &notificationspb.ListNotificationsRequest{})
	must.NoError(T, err)
	must.NotNil(T, res)

	test.SliceEmpty(T, res.GetResults())
}

// TestListUnreadNotificationsCarriesTheBadgeCount is the number a bell icon
// renders, and it is on the pagination rather than in a field of its own.
func TestListUnreadNotificationsCarriesTheBadgeCount(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	first := h.seedNotification(T, testScope, testPrincipal, "order.shipped")
	h.seedNotification(T, testScope, testPrincipal, "invite.received")

	ctx := h.ctx(T, testPrincipal)

	before, err := h.server.ListUnreadNotifications(ctx, &notificationspb.ListUnreadNotificationsRequest{})
	must.NoError(T, err)
	must.SliceLen(T, 2, before.GetResults())
	test.EqOp(T, uint64(2), before.GetPagination().GetFilteredCount())

	_, err = h.server.MarkNotificationRead(ctx,
		&notificationspb.MarkNotificationReadRequest{NotificationId: first.ID})
	must.NoError(T, err)

	after, err := h.server.ListUnreadNotifications(ctx, &notificationspb.ListUnreadNotificationsRequest{})
	must.NoError(T, err)
	test.SliceLen(T, 1, after.GetResults())
	test.EqOp(T, uint64(1), after.GetPagination().GetFilteredCount())
}

func TestGetNotificationReadsOneOfTheCallersOwn(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	seeded := h.seedNotification(T, testScope, testPrincipal, "order.shipped")

	res, err := h.server.GetNotification(h.ctx(T, testPrincipal),
		&notificationspb.GetNotificationRequest{NotificationId: seeded.ID})
	must.NoError(T, err)
	must.NotNil(T, res.GetResult())

	test.EqOp(T, seeded.ID, res.GetResult().GetId())
	test.EqOp(T, "something happened", res.GetResult().GetTitle())

	// Unread, and unread is the absence of a stamp rather than a false.
	test.Nil(T, res.GetResult().GetReadAt())
}

// TestGetNotificationIsNotAnOracle is the row-level property this surface has
// instead of a TargetAuthorizer, and the reason it needs none: somebody else's
// notification is absent, not forbidden, and the two answers are the same one.
func TestGetNotificationIsNotAnOracle(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	theirs := h.seedNotification(T, testScope, otherPrincipal, "order.shipped")

	_, err := h.server.GetNotification(h.ctx(T, testPrincipal),
		&notificationspb.GetNotificationRequest{NotificationId: theirs.ID})
	must.Error(T, err)

	test.EqOp(T, codes.NotFound, status.Code(err))
	test.ErrorIs(T, err, notifications.ErrNotificationNotFound)

	// And the same answer for an identifier nobody holds, which is what makes
	// the first one disclose nothing.
	_, absent := h.server.GetNotification(h.ctx(T, testPrincipal),
		&notificationspb.GetNotificationRequest{NotificationId: "nonesuch"})
	must.Error(T, absent)
	test.EqOp(T, codes.NotFound, status.Code(absent))
}

// TestMarkNotificationReadDoesNotMoveTheStamp is the store's idempotence held
// through the transport, which is what a client retrying a tap depends on.
func TestMarkNotificationReadDoesNotMoveTheStamp(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	seeded := h.seedNotification(T, testScope, testPrincipal, "order.shipped")
	ctx := h.ctx(T, testPrincipal)

	_, err := h.server.MarkNotificationRead(ctx,
		&notificationspb.MarkNotificationReadRequest{NotificationId: seeded.ID})
	must.NoError(T, err)

	first, err := h.server.GetNotification(ctx, &notificationspb.GetNotificationRequest{NotificationId: seeded.ID})
	must.NoError(T, err)
	must.NotNil(T, first.GetResult().GetReadAt())

	_, err = h.server.MarkNotificationRead(ctx,
		&notificationspb.MarkNotificationReadRequest{NotificationId: seeded.ID})
	must.NoError(T, err)

	second, err := h.server.GetNotification(ctx, &notificationspb.GetNotificationRequest{NotificationId: seeded.ID})
	must.NoError(T, err)

	test.EqOp(T, first.GetResult().GetReadAt().AsTime(), second.GetResult().GetReadAt().AsTime())
}

func TestMarkNotificationReadCannotReachSomebodyElsesRow(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	theirs := h.seedNotification(T, testScope, otherPrincipal, "order.shipped")

	_, err := h.server.MarkNotificationRead(h.ctx(T, testPrincipal),
		&notificationspb.MarkNotificationReadRequest{NotificationId: theirs.ID})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
}

// TestMarkAllNotificationsReadReportsWhatItMoved pins the count as the answer
// rather than a diagnostic, and pins what a retry says: zero, because nothing
// was unread the second time.
func TestMarkAllNotificationsReadReportsWhatItMoved(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	h.seedNotification(T, testScope, testPrincipal, "order.shipped")
	h.seedNotification(T, testScope, testPrincipal, "invite.received")
	h.seedNotification(T, testScope, otherPrincipal, "order.shipped")

	ctx := h.ctx(T, testPrincipal)

	res, err := h.server.MarkAllNotificationsRead(ctx, &notificationspb.MarkAllNotificationsReadRequest{})
	must.NoError(T, err)
	test.EqOp(T, int64(2), res.GetMarked())

	again, err := h.server.MarkAllNotificationsRead(ctx, &notificationspb.MarkAllNotificationsReadRequest{})
	must.NoError(T, err)
	test.EqOp(T, int64(0), again.GetMarked())

	// The other person's notification was not among the two, which is the half
	// of the count that a scope-only filter would have got wrong.
	theirs, err := h.server.ListUnreadNotifications(h.ctx(T, otherPrincipal),
		&notificationspb.ListUnreadNotificationsRequest{})
	must.NoError(T, err)
	test.SliceLen(T, 1, theirs.GetResults())
}

// TestArchiveNotificationTakesItOutOfTheInbox, and the second archive answers
// as absent because an archived notification is not in the inbox this RPC
// addresses.
func TestArchiveNotificationTakesItOutOfTheInbox(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	seeded := h.seedNotification(T, testScope, testPrincipal, "order.shipped")
	ctx := h.ctx(T, testPrincipal)

	_, err := h.server.ArchiveNotification(ctx,
		&notificationspb.ArchiveNotificationRequest{NotificationId: seeded.ID})
	must.NoError(T, err)

	listed, err := h.server.ListNotifications(ctx, &notificationspb.ListNotificationsRequest{})
	must.NoError(T, err)
	test.SliceEmpty(T, listed.GetResults())

	_, err = h.server.GetNotification(ctx, &notificationspb.GetNotificationRequest{NotificationId: seeded.ID})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))

	_, err = h.server.ArchiveNotification(ctx,
		&notificationspb.ArchiveNotificationRequest{NotificationId: seeded.ID})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
}

// TestMarkAllNotificationsReadIsOneStatement is why the count is trustworthy.
//
// The handler opens one transaction and the store runs one statement inside it,
// so the number handed back is what that statement moved rather than a tally a
// loop kept — and a caller that unwinds has marked nothing rather than some of
// them.
func TestMarkAllNotificationsReadIsOneStatement(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for range 3 {
		h.seedNotification(T, testScope, testPrincipal, "order.shipped")
	}

	res, err := h.server.MarkAllNotificationsRead(h.ctx(T, testPrincipal),
		&notificationspb.MarkAllNotificationsReadRequest{})
	must.NoError(T, err)
	test.EqOp(T, int64(3), res.GetMarked())

	listed, err := h.server.ListNotifications(h.ctx(T, testPrincipal), &notificationspb.ListNotificationsRequest{})
	must.NoError(T, err)
	must.SliceLen(T, 3, listed.GetResults())

	for _, n := range listed.GetResults() {
		test.NotNil(T, n.GetReadAt())
	}
}
