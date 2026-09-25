package notifications

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func devices(t *testing.T, s *conformance.Session) {
	t.Helper()

	registrations(t, s)
	revocations(t, s)
}

func registrations(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a registered handset is the caller's and nobody else's", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		device := register(t, mine, freshToken())

		test.EqOp(t, notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, device.GetPlatform())
		test.NotNil(t, device.GetCreatedAt())
		test.NotNil(t, device.GetLastSeenAt())

		// The owner came off the connection and the request had no field for
		// one, which is the whole of the row-level authorization here.
		test.SliceContains(t, devicesOf(t, mine), device.GetId(),
			test.Sprint("a handset the caller registered was missing from its devices"))
		test.SliceNotContains(t, devicesOf(t, other), device.GetId(),
			test.Sprint("a handset one caller registered was listed to a colleague"))
	})

	// A handset that one person signs out of and another signs into presents
	// the same token under a new owner, and the registry moves the row rather
	// than keeping both — the alternative is the previous owner's pushes
	// arriving on somebody else's lock screen. The second registration answers
	// with the first one's identifier and creation time because the read-back
	// is of the row as stored rather than as sent; the two times are one stored
	// value, so the comparison holds at any dialect's precision.
	t.Run("registering a token again converges on one handset under its new owner", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)
		token := freshToken()

		first := register(t, mine, token)
		test.SliceContains(t, devicesOf(t, mine), first.GetId())

		second := register(t, other, token)

		test.EqOp(t, first.GetId(), second.GetId(),
			test.Sprint("a token registered twice became two handsets"))
		test.EqOp(t, first.GetCreatedAt().AsTime(), second.GetCreatedAt().AsTime())

		test.SliceNotContains(t, devicesOf(t, mine), first.GetId(),
			test.Sprint("the previous owner still holds a handset somebody else signed into"))
		test.SliceContains(t, devicesOf(t, other), first.GetId())
	})

	// Both halves of the enum's zero value: a client that set no platform, and
	// one that sent a number this module has no sender for. Either is refused
	// rather than recorded as a row nothing will ever deliver to or prune.
	for name, platform := range map[string]notificationspb.DevicePlatform{
		"a handset with no platform is refused as malformed":                  notificationspb.DevicePlatform_DEVICE_PLATFORM_UNSPECIFIED,
		"a handset on a platform nothing can push to is refused as malformed": notificationspb.DevicePlatform(99),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mine := s.Subject(t)

			_, err := mine.Surfaces.Notifications.RegisterDevice(mine.Context(t.Context()),
				registration(platform, freshToken()))
			must.Error(t, err)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		})
	}

	t.Run("a handset with no token is refused as malformed", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		_, err := mine.Surfaces.Notifications.RegisterDevice(mine.Context(t.Context()),
			registration(notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, ""))
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a registration that names no input is refused as malformed", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)

		_, err := mine.Surfaces.Notifications.RegisterDevice(mine.Context(t.Context()),
			&notificationspb.RegisterDeviceRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a device listing holds the caller's own handsets and nothing from a colleague or another tenant", func(t *testing.T) {
		t.Parallel()

		mine, neighbor := twoTenants(t, s)
		other := colleague(t, s, mine)

		own := register(t, mine, freshToken())
		colleagues := register(t, other, freshToken())
		neighbors := register(t, neighbor, freshToken())

		ids := devicesOf(t, mine)
		test.SliceContains(t, ids, own.GetId(),
			test.Sprint("the caller's own handset was missing from its devices"))
		test.SliceNotContains(t, ids, colleagues.GetId(),
			test.Sprint("a colleague's handset reached this caller's devices"))
		test.SliceNotContains(t, ids, neighbors.GetId(),
			test.Sprint("a neighboring tenant's handset reached this caller's devices"))
	})
}

func revocations(t *testing.T, s *conformance.Session) {
	t.Helper()

	// The row is deleted rather than archived, so a second revocation is an
	// absence rather than a second success.
	t.Run("a revoked handset leaves the caller's devices, and revoking it again is an absence", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		device := register(t, mine, freshToken())

		must.SliceContains(t, devicesOf(t, mine), device.GetId())

		_, err := mine.Surfaces.Notifications.RevokeDevice(mine.Context(t.Context()),
			&notificationspb.RevokeDeviceRequest{DeviceId: device.GetId()})
		must.NoError(t, err)

		test.SliceNotContains(t, devicesOf(t, mine), device.GetId(),
			test.Sprint("a revoked handset was still listed"))

		_, err = mine.Surfaces.Notifications.RevokeDevice(mine.Context(t.Context()),
			&notificationspb.RevokeDeviceRequest{DeviceId: device.GetId()})
		must.Error(t, err, must.Sprint("a revoked handset was revoked a second time"))
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	// The same reading the inbox takes of somebody else's notification, and
	// what makes revocation a safe RPC: it names a handset the caller owns, and
	// the statement binds the owner.
	t.Run("revoking a colleague's handset is an absence and leaves it theirs", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t)
		other := colleague(t, s, mine)

		own := register(t, mine, freshToken())
		theirs := register(t, other, freshToken())

		_, err := mine.Surfaces.Notifications.RevokeDevice(mine.Context(t.Context()),
			&notificationspb.RevokeDeviceRequest{DeviceId: own.GetId()})
		must.NoError(t, err, must.Sprint("the caller cannot revoke its own handset; the refusal below proves nothing"))

		_, err = mine.Surfaces.Notifications.RevokeDevice(mine.Context(t.Context()),
			&notificationspb.RevokeDeviceRequest{DeviceId: theirs.GetId()})
		must.Error(t, err, must.Sprint("a colleague's handset was revocable"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.SliceContains(t, devicesOf(t, other), theirs.GetId(),
			test.Sprint("a colleague's handset was revoked by a request refused as absent"))
	})
}
