package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"
	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The registry half, whose caller is a handset. Most of what is asserted here is
// the convergence — the property that makes a phone re-registering on every
// launch a write rather than a leak of rows — and the two things a response
// deliberately does not carry.

func registration(platform notificationspb.DevicePlatform, token string) *notificationspb.RegisterDeviceRequest {
	return &notificationspb.RegisterDeviceRequest{
		Input: &notificationspb.DeviceRegistrationInput{Platform: platform, Token: token},
	}
}

func TestRegisterDeviceRecordsTheCallersHandset(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	res, err := h.server.RegisterDevice(h.ctx(T, testPrincipal),
		registration(notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, testToken))
	must.NoError(T, err)
	must.NotNil(T, res.GetResult())

	test.NotEq(T, "", res.GetResult().GetId())
	test.EqOp(T, notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, res.GetResult().GetPlatform())
	must.NotNil(T, res.GetResult().GetCreatedAt())
	must.NotNil(T, res.GetResult().GetLastSeenAt())

	// It is the caller's handset and nobody else's, which is the whole of the
	// row-level authorization here: the principal came off the connection and the
	// request had no field for one.
	listed, err := h.server.ListDevices(h.ctx(T, otherPrincipal), &notificationspb.ListDevicesRequest{})
	must.NoError(T, err)
	test.SliceEmpty(T, listed.GetResults())
}

// TestRegisterDeviceConvergesOnTheToken is the property the issue's ruling
// turned on, and the reason there is no variant on this surface that skips it: a
// handset that changes hands has one owner.
//
// The read-back runs inside the handler's transaction, which is what makes the
// second registration answer with the first one's id and creation time rather
// than with whatever the caller sent.
func TestRegisterDeviceConvergesOnTheToken(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	first, err := h.server.RegisterDevice(h.ctx(T, testPrincipal),
		registration(notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, testToken))
	must.NoError(T, err)

	second, err := h.server.RegisterDevice(h.ctx(T, otherPrincipal),
		registration(notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, testToken))
	must.NoError(T, err)

	test.EqOp(T, first.GetResult().GetId(), second.GetResult().GetId())
	test.EqOp(T, first.GetResult().GetCreatedAt().AsTime(), second.GetResult().GetCreatedAt().AsTime())

	// One row, and it is the new owner's. The previous owner's push would have
	// arrived on somebody else's lock screen.
	previous, err := h.server.ListDevices(h.ctx(T, testPrincipal), &notificationspb.ListDevicesRequest{})
	must.NoError(T, err)
	test.SliceEmpty(T, previous.GetResults())

	current, err := h.server.ListDevices(h.ctx(T, otherPrincipal), &notificationspb.ListDevicesRequest{})
	must.NoError(T, err)
	test.SliceLen(T, 1, current.GetResults())
}

// TestRegisterDeviceRefusesAPlatformItCannotPushTo covers both halves of the
// enum's zero value: a client that set no platform, and one that sent a number
// this module has no sender for. Both are notifications.ErrUnknownPlatform
// rather than a row nothing will ever deliver to and nothing will ever prune.
func TestRegisterDeviceRefusesAPlatformItCannotPushTo(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for name, platform := range map[string]notificationspb.DevicePlatform{
		"unset":        notificationspb.DevicePlatform_DEVICE_PLATFORM_UNSPECIFIED,
		"out of range": notificationspb.DevicePlatform(99),
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := h.server.RegisterDevice(h.ctx(t, testPrincipal), registration(platform, testToken))
			must.Error(t, err)

			test.EqOp(t, codes.InvalidArgument, status.Code(err))
			test.ErrorIs(t, err, notifications.ErrUnknownPlatform)
		})
	}
}

func TestRegisterDeviceRefusesAnEmptyToken(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	_, err := h.server.RegisterDevice(h.ctx(T, testPrincipal),
		registration(notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, ""))
	must.Error(T, err)

	test.EqOp(T, codes.InvalidArgument, status.Code(err))
	test.ErrorIs(T, err, notifications.ErrEmptyToken)
}

// TestRegisterDeviceRefusesARequestWithNoInput is the malformed request rather
// than the nil argument, which is why this surface has a sentinel of its own for
// it: there is nothing for the store to refuse yet.
func TestRegisterDeviceRefusesARequestWithNoInput(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	_, err := h.server.RegisterDevice(h.ctx(T, testPrincipal), &notificationspb.RegisterDeviceRequest{})
	must.Error(T, err)

	test.EqOp(T, codes.InvalidArgument, status.Code(err))
	test.ErrorIs(T, err, notificationsgrpc.ErrNilRegistrationInput)
}

func TestListDevicesPagesTheCallersOwn(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	h.seedDevice(T, testScope, testPrincipal, "token-a")
	h.seedDevice(T, testScope, otherPrincipal, "token-b")
	h.seedDevice(T, otherScope, testPrincipal, "token-c")

	res, err := h.server.ListDevices(h.ctx(T, testPrincipal), &notificationspb.ListDevicesRequest{})
	must.NoError(T, err)
	must.SliceLen(T, 1, res.GetResults())

	test.EqOp(T, notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, res.GetResults()[0].GetPlatform())
	must.NotNil(T, res.GetPagination())
}

func TestRevokeDeviceRemovesOneOfTheCallersOwn(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	device := h.seedDevice(T, testScope, testPrincipal, testToken)
	ctx := h.ctx(T, testPrincipal)

	_, err := h.server.RevokeDevice(ctx, &notificationspb.RevokeDeviceRequest{DeviceId: device.ID})
	must.NoError(T, err)

	listed, err := h.server.ListDevices(ctx, &notificationspb.ListDevicesRequest{})
	must.NoError(T, err)
	test.SliceEmpty(T, listed.GetResults())

	// The row is deleted rather than archived, so a second revocation is absent
	// rather than a second success.
	_, err = h.server.RevokeDevice(ctx, &notificationspb.RevokeDeviceRequest{DeviceId: device.ID})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
	test.ErrorIs(T, err, notifications.ErrDeviceNotFound)
}

// TestRevokeDeviceCannotReachSomebodyElsesHandset is the same reading the inbox
// takes of somebody else's notification, and it is what makes RevokeDevice a
// safe RPC where InvalidateDeviceToken would not be: this one names a device the
// caller owns, and the statement binds the owner.
func TestRevokeDeviceCannotReachSomebodyElsesHandset(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	theirs := h.seedDevice(T, testScope, otherPrincipal, testToken)

	_, err := h.server.RevokeDevice(h.ctx(T, testPrincipal),
		&notificationspb.RevokeDeviceRequest{DeviceId: theirs.ID})
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))

	// And it is still there, which is the half a NotFound alone would not show.
	listed, err := h.server.ListDevices(h.ctx(T, otherPrincipal), &notificationspb.ListDevicesRequest{})
	must.NoError(T, err)
	test.SliceLen(T, 1, listed.GetResults())
}
