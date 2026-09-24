package notifications

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test/must"
)

// noPagination is what every paged read here fails with when it answers
// without the pagination a client renders "n of m" from.
const noPagination = "a paged read answered with no pagination"

// Suite is the inbox and device surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    "notifications",
		Mounted: func(s conformance.Surfaces) bool { return s.Notifications != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("the inbox", func(t *testing.T) {
		t.Parallel()
		inbox(t, s)
	})
	t.Run("devices", func(t *testing.T) {
		t.Parallel()
		devices(t, s)
	})
}

// twoTenants mints two callers and refuses to proceed if the subject put them
// in one tenant, since every confinement assertion here would then compare a
// tenant with itself.
func twoTenants(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.Subject(t), s.Subject(t)

	must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
		must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

	return mine, theirs
}

// colleague mints a second person in of's tenant — the case the scope alone
// cannot confine, and the one this surface's addressing exists for. A subject
// that cannot put two callers in one tenant declines, and the assertion that
// asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject) *conformance.Subject {
	t.Helper()

	other := s.Subject(t, conformance.InTenant(of.Scope))

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user; the addressing this asserts cannot be observed"))

	return other
}

// notified has the deployment tell sub something, and reports the identifier
// of the notification that landed in their inbox. It skips where the subject
// cannot say how its deployment would, or does not surface the user to tell.
func notified(t *testing.T, s *conformance.Session, sub *conformance.Subject) string {
	t.Helper()

	notify := s.Seams().Actions.Notified
	s.NeedsAction(t, notify != nil, "notified")

	if sub.UserID == "" {
		t.Skip("conformance: this subject does not surface the caller's user identifier, so there is nobody to notify")
	}

	id, err := notify(t.Context(), sub.Scope, sub.UserID)
	must.NoError(t, err, must.Sprint("having the deployment notify a user"))
	must.StrNotEqFold(t, "", id, must.Sprint("the notified action reported no notification identifier"))

	return id
}

// inboxOf is the identifiers on the first page of sub's inbox.
//
// The first page is enough: every caller here is minted fresh, so the rows an
// assertion looks for are the only ones this suite put there, and a
// deployment's own notifications on registration are a handful rather than a
// page.
func inboxOf(t *testing.T, sub *conformance.Subject) []string {
	t.Helper()

	page, err := sub.Surfaces.Notifications.ListNotifications(sub.Context(t.Context()),
		&notificationspb.ListNotificationsRequest{})
	must.NoError(t, err, must.Sprint("reading an inbox"))
	must.NotNil(t, page.GetPagination(), must.Sprint(noPagination))

	return notificationIDs(page.GetResults())
}

// unreadOf is the identifiers on the first page of sub's unread notifications.
func unreadOf(t *testing.T, sub *conformance.Subject) []string {
	t.Helper()

	page, err := sub.Surfaces.Notifications.ListUnreadNotifications(sub.Context(t.Context()),
		&notificationspb.ListUnreadNotificationsRequest{})
	must.NoError(t, err, must.Sprint("reading an inbox's unread notifications"))
	must.NotNil(t, page.GetPagination(), must.Sprint(noPagination))

	return notificationIDs(page.GetResults())
}

// get reads one of sub's notifications, failing if it cannot.
func get(t *testing.T, sub *conformance.Subject, id string) *notificationspb.Notification {
	t.Helper()

	found, err := sub.Surfaces.Notifications.GetNotification(sub.Context(t.Context()),
		&notificationspb.GetNotificationRequest{NotificationId: id})
	must.NoError(t, err, must.Sprint("reading one of the caller's own notifications"))
	must.NotNil(t, found.GetResult())

	return found.GetResult()
}

// freshToken is a device token nobody has registered. Tokens are unique across
// every tenant, so one shared between assertions would converge them onto one
// row.
func freshToken() string { return "conf-" + identifiers.New() }

// register records a handset for sub through the surface.
func register(t *testing.T, sub *conformance.Subject, token string) *notificationspb.Device {
	t.Helper()

	registered, err := sub.Surfaces.Notifications.RegisterDevice(sub.Context(t.Context()),
		registration(notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, token))
	must.NoError(t, err, must.Sprint("registering a device"))
	must.NotNil(t, registered.GetResult())
	must.StrNotEqFold(t, "", registered.GetResult().GetId(), must.Sprint("a registered device came back with no identifier"))

	return registered.GetResult()
}

func registration(platform notificationspb.DevicePlatform, token string) *notificationspb.RegisterDeviceRequest {
	return &notificationspb.RegisterDeviceRequest{
		Input: &notificationspb.DeviceRegistrationInput{Platform: platform, Token: token},
	}
}

// devicesOf is the identifiers on the first page of sub's devices.
func devicesOf(t *testing.T, sub *conformance.Subject) []string {
	t.Helper()

	page, err := sub.Surfaces.Notifications.ListDevices(sub.Context(t.Context()),
		&notificationspb.ListDevicesRequest{})
	must.NoError(t, err, must.Sprint("listing a caller's devices"))
	must.NotNil(t, page.GetPagination(), must.Sprint(noPagination))

	ids := make([]string, 0, len(page.GetResults()))
	for _, d := range page.GetResults() {
		ids = append(ids, d.GetId())
	}

	return ids
}

func notificationIDs(in []*notificationspb.Notification) []string {
	ids := make([]string, 0, len(in))
	for _, n := range in {
		ids = append(ids, n.GetId())
	}

	return ids
}
