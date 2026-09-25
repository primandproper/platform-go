package notifications

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func inbox(t *testing.T, s *conformance.Session) {
	t.Helper()

	reads(t, s)
	marking(t, s)
	archiving(t, s)
}

func reads(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an inbox holds the caller's own notifications and not a colleague's", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		own := notified(t, s, mine)
		theirs := notified(t, s, other)

		// Presence and absence of two known notifications, never a count: a
		// deployment may tell a new user things of its own.
		ids := inboxOf(t, mine)
		test.SliceContains(t, ids, own,
			test.Sprint("the caller's own notification was missing from its inbox"))
		test.SliceNotContains(t, ids, theirs,
			test.Sprint("a colleague's notification reached this caller's inbox"))

		// And the mirror image, which rules out an inbox that happens to favor
		// whichever caller was made first.
		test.SliceContains(t, inboxOf(t, other), theirs)
	})

	t.Run("an inbox holds nothing from another tenant", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoTenants(t, s)

		own := notified(t, s, mine)
		neighbor := notified(t, s, theirs)

		ids := inboxOf(t, mine)
		test.SliceContains(t, ids, own)
		test.SliceNotContains(t, ids, neighbor,
			test.Sprint("a neighboring tenant's notification reached this caller's inbox"))
	})

	t.Run("a notification reads back as the caller's own, and unread", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		id := notified(t, s, mine)

		found := get(t, mine, id)
		test.EqOp(t, id, found.GetId())
		test.StrNotEqFold(t, "", found.GetTitle(),
			test.Sprint("a notification came back with no title, which a client renders as a blank line"))

		// Unread is the absence of a stamp rather than a false.
		test.Nil(t, found.GetReadAt())
	})

	// The row-level property this surface has instead of an authorizer, and
	// the reason it needs none: somebody else's notification is absent, not
	// forbidden, and the answer is the same one an identifier nobody minted
	// gets.
	t.Run("a colleague's notification reads as one that is not there", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		own := notified(t, s, mine)
		theirs := notified(t, s, other)

		// The positive control.
		test.EqOp(t, own, get(t, mine, own).GetId())

		_, err := mine.Surfaces.Notifications.GetNotification(mine.Context(t.Context()),
			&notificationspb.GetNotificationRequest{NotificationId: theirs})
		must.Error(t, err, must.Sprint("a colleague's notification was readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = mine.Surfaces.Notifications.GetNotification(mine.Context(t.Context()),
			&notificationspb.GetNotificationRequest{NotificationId: identifiers.New()})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err),
			test.Sprint("an identifier nobody minted was answered differently from a colleague's"))
	})
}

func marking(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("reading one notification takes it off the unread list and leaves the others there", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		first := notified(t, s, mine)
		second := notified(t, s, mine)

		before := unreadOf(t, mine)
		test.SliceContains(t, before, first)
		test.SliceContains(t, before, second)

		_, err := mine.Surfaces.Notifications.MarkNotificationRead(mine.Context(t.Context()),
			&notificationspb.MarkNotificationReadRequest{NotificationId: first})
		must.NoError(t, err)

		after := unreadOf(t, mine)
		test.SliceNotContains(t, after, first,
			test.Sprint("a notification marked read was still on the unread list"))
		test.SliceContains(t, after, second,
			test.Sprint("marking one notification read took another off the unread list"))

		test.NotNil(t, get(t, mine, first).GetReadAt())
	})

	// The store's idempotence held through the transport, which is what a
	// client retrying a tap depends on. The two stamps are one stored value read
	// twice, so the comparison holds at any dialect's precision.
	t.Run("marking a notification read twice does not move when it was read", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		id := notified(t, s, mine)

		_, err := mine.Surfaces.Notifications.MarkNotificationRead(mine.Context(t.Context()),
			&notificationspb.MarkNotificationReadRequest{NotificationId: id})
		must.NoError(t, err)

		first := get(t, mine, id).GetReadAt()
		must.NotNil(t, first)

		_, err = mine.Surfaces.Notifications.MarkNotificationRead(mine.Context(t.Context()),
			&notificationspb.MarkNotificationReadRequest{NotificationId: id})
		must.NoError(t, err, must.Sprint("marking an already-read notification read was refused"))

		second := get(t, mine, id).GetReadAt()
		must.NotNil(t, second)
		test.EqOp(t, first.AsTime(), second.AsTime(),
			test.Sprint("marking an already-read notification read moved its stamp"))
	})

	t.Run("marking a colleague's notification read is an absence and leaves it unread", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		own := notified(t, s, mine)
		theirs := notified(t, s, other)

		// The positive control: the same call reaches the caller's own.
		_, err := mine.Surfaces.Notifications.MarkNotificationRead(mine.Context(t.Context()),
			&notificationspb.MarkNotificationReadRequest{NotificationId: own})
		must.NoError(t, err, must.Sprint("the caller cannot mark its own notification read; the refusal below proves nothing"))

		_, err = mine.Surfaces.Notifications.MarkNotificationRead(mine.Context(t.Context()),
			&notificationspb.MarkNotificationReadRequest{NotificationId: theirs})
		must.Error(t, err, must.Sprint("a colleague's notification was markable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.SliceContains(t, unreadOf(t, other), theirs,
			test.Sprint("a colleague's notification was marked read by a request refused as absent"))
	})

	t.Run("marking everything read marks the caller's own and leaves a colleague's alone", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		first := notified(t, s, mine)
		second := notified(t, s, mine)
		theirs := notified(t, s, other)

		_, err := mine.Surfaces.Notifications.MarkAllNotificationsRead(mine.Context(t.Context()),
			&notificationspb.MarkAllNotificationsReadRequest{})
		must.NoError(t, err)

		unread := unreadOf(t, mine)
		test.SliceNotContains(t, unread, first)
		test.SliceNotContains(t, unread, second)
		test.NotNil(t, get(t, mine, first).GetReadAt())
		test.NotNil(t, get(t, mine, second).GetReadAt())

		// The half a scope-only statement would have got wrong: the colleague
		// shares the tenant, and their inbox is not the caller's.
		test.SliceContains(t, unreadOf(t, other), theirs,
			test.Sprint("marking everything read reached a colleague's inbox"))

		// A retry is an answer rather than an error, which is what a client that
		// lost the first response depends on.
		_, err = mine.Surfaces.Notifications.MarkAllNotificationsRead(mine.Context(t.Context()),
			&notificationspb.MarkAllNotificationsReadRequest{})
		must.NoError(t, err, must.Sprint("marking everything read a second time was refused"))
	})
}

func archiving(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an archived notification leaves the inbox, and archiving it again is an absence", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		id := notified(t, s, mine)

		// The positive control: it is in the inbox before it is archived, so
		// the absence afterwards is the archive's.
		must.SliceContains(t, inboxOf(t, mine), id)

		_, err := mine.Surfaces.Notifications.ArchiveNotification(mine.Context(t.Context()),
			&notificationspb.ArchiveNotificationRequest{NotificationId: id})
		must.NoError(t, err)

		test.SliceNotContains(t, inboxOf(t, mine), id,
			test.Sprint("an archived notification was still listed in the inbox"))

		_, err = mine.Surfaces.Notifications.GetNotification(mine.Context(t.Context()),
			&notificationspb.GetNotificationRequest{NotificationId: id})
		must.Error(t, err, must.Sprint("an archived notification was still readable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = mine.Surfaces.Notifications.ArchiveNotification(mine.Context(t.Context()),
			&notificationspb.ArchiveNotificationRequest{NotificationId: id})
		must.Error(t, err, must.Sprint("an archived notification was archived a second time"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("archiving a colleague's notification is an absence and leaves it in their inbox", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		own := notified(t, s, mine)
		theirs := notified(t, s, other)

		_, err := mine.Surfaces.Notifications.ArchiveNotification(mine.Context(t.Context()),
			&notificationspb.ArchiveNotificationRequest{NotificationId: own})
		must.NoError(t, err, must.Sprint("the caller cannot archive its own notification; the refusal below proves nothing"))

		_, err = mine.Surfaces.Notifications.ArchiveNotification(mine.Context(t.Context()),
			&notificationspb.ArchiveNotificationRequest{NotificationId: theirs})
		must.Error(t, err, must.Sprint("a colleague's notification was archivable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.SliceContains(t, inboxOf(t, other), theirs,
			test.Sprint("a colleague's notification was archived by a request refused as absent"))
	})
}
