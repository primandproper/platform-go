package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"
	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestNewServerRefusesEachMissingDependency is four separate refusals rather
// than one, because each of them fails differently in production if it is
// tolerated: no inbox or no registry is a surface that answers three or six of
// its nine methods by panicking, no client is one with nothing to read on and no
// transaction to open, and no principal extractor is either an unusable service
// or an open one.
func TestNewServerRefusesEachMissingDependency(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	cases := map[string]struct {
		build func() (*notificationsgrpc.Server, error)
		want  error
	}{
		"no inbox": {
			build: func() (*notificationsgrpc.Server, error) {
				return notificationsgrpc.NewServer(nil, h.store, h.db, extractPrincipal)
			},
			want: notificationsgrpc.ErrNilInbox,
		},
		"no registry": {
			build: func() (*notificationsgrpc.Server, error) {
				return notificationsgrpc.NewServer(h.store, nil, h.db, extractPrincipal)
			},
			want: notificationsgrpc.ErrNilRegistry,
		},
		"no database client": {
			build: func() (*notificationsgrpc.Server, error) {
				return notificationsgrpc.NewServer(h.store, h.store, nil, extractPrincipal)
			},
			want: notificationsgrpc.ErrNilDatabaseClient,
		},
		"no principal extractor": {
			build: func() (*notificationsgrpc.Server, error) {
				return notificationsgrpc.NewServer(h.store, h.store, h.db, nil)
			},
			want: notificationsgrpc.ErrNilPrincipalExtractor,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			srv, err := tc.build()
			test.Nil(t, srv)
			must.Error(t, err)
			test.ErrorIs(t, err, tc.want)

			// Every one of them wraps the platform sentinel too, so a consumer
			// checking either spelling is asking the same question.
			test.True(t, platformerrors.Is(err, platformerrors.ErrNilInputParameter))
		})
	}
}

// TestAnAnonymousRequestIsRefusedOnEveryMethod is the first of the two things
// caller resolves, held across the whole surface rather than on the one method
// somebody remembered to test.
func TestAnAnonymousRequestIsRefusedOnEveryMethod(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for name, call := range everyRPC(h) {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			err := call(t.Context())
			must.Error(t, err)

			test.EqOp(t, codes.Unauthenticated, status.Code(err))
			test.ErrorIs(t, err, notificationsgrpc.ErrNoPrincipal)
		})
	}
}

// TestACallerWithNoIdentifierHasNoInbox is the second, and it is this surface's
// own refusal rather than a shape it inherited.
//
// A principal with an empty user identifier is an ordinary machine caller on the
// OAuth2 client registry next door. Here the caller is the row's address, and
// letting one through would reach a store that refuses it with a message about
// an argument the client cannot see — so the refusal is made where the reason is
// legible.
func TestACallerWithNoIdentifierHasNoInbox(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	for name, call := range everyRPC(h) {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			err := call(withPrincipal(t.Context(), &testPrincipalValue{userID: "", scope: testScope}))
			must.Error(t, err)

			test.EqOp(t, codes.Unauthenticated, status.Code(err))
			test.ErrorIs(t, err, notificationsgrpc.ErrNoPrincipalIdentifier)

			// Not the store's sentinel, which is the point: there is no field
			// for a client to correct.
			test.False(t, platformerrors.Is(err, notifications.ErrEmptyPrincipal))
		})
	}
}

// everyRPC is each of the nine, reduced to "what error did it answer with", so
// the two refusals above are asserted over the whole surface.
//
// It is written out rather than reflected over the descriptor because the point
// is that each handler routes through caller before it does anything, and a
// reflective call would prove that only for whichever methods it managed to
// build a request for.
func everyRPC(h *harness) map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		"ListNotifications": func(ctx context.Context) error {
			_, err := h.server.ListNotifications(ctx, &notificationspb.ListNotificationsRequest{})

			return err
		},
		"ListUnreadNotifications": func(ctx context.Context) error {
			_, err := h.server.ListUnreadNotifications(ctx, &notificationspb.ListUnreadNotificationsRequest{})

			return err
		},
		"GetNotification": func(ctx context.Context) error {
			_, err := h.server.GetNotification(ctx, &notificationspb.GetNotificationRequest{NotificationId: "x"})

			return err
		},
		"MarkNotificationRead": func(ctx context.Context) error {
			_, err := h.server.MarkNotificationRead(ctx,
				&notificationspb.MarkNotificationReadRequest{NotificationId: "x"})

			return err
		},
		"MarkAllNotificationsRead": func(ctx context.Context) error {
			_, err := h.server.MarkAllNotificationsRead(ctx, &notificationspb.MarkAllNotificationsReadRequest{})

			return err
		},
		"ArchiveNotification": func(ctx context.Context) error {
			_, err := h.server.ArchiveNotification(ctx,
				&notificationspb.ArchiveNotificationRequest{NotificationId: "x"})

			return err
		},
		"RegisterDevice": func(ctx context.Context) error {
			_, err := h.server.RegisterDevice(ctx,
				registration(notificationspb.DevicePlatform_DEVICE_PLATFORM_IOS, testToken))

			return err
		},
		"ListDevices": func(ctx context.Context) error {
			_, err := h.server.ListDevices(ctx, &notificationspb.ListDevicesRequest{})

			return err
		},
		"RevokeDevice": func(ctx context.Context) error {
			_, err := h.server.RevokeDevice(ctx, &notificationspb.RevokeDeviceRequest{DeviceId: "x"})

			return err
		},
	}
}

// TestNilOptionsAreIgnored: a caller building an option list conditionally ends
// up with a nil in it, and a constructor that panicked on one would make the
// conditional the caller's problem.
func TestNilOptionsAreIgnored(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	srv, err := notificationsgrpc.NewServer(h.store, h.store, h.db, extractPrincipal, nil)
	must.NoError(T, err)
	test.NotNil(T, srv)
}

// TestRegisterOnMountsTheService is the signature server/grpc's RegistrationFunc
// wants, checked by using it as one.
func TestRegisterOnMountsTheService(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	srv := grpc.NewServer()
	T.Cleanup(srv.Stop)

	h.server.RegisterOn(srv)

	_, mounted := srv.GetServiceInfo()["primandproper.platform.notifications.v1.NotificationsService"]
	test.True(T, mounted)
}

// TestTheServerIsBuiltOverTheTwoSeams keeps the surface mountable over a
// consumer's own implementation rather than only over notifications.SQLStore,
// which is the whole reason notifications declares interfaces at all.
func TestTheServerIsBuiltOverTheTwoSeams(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	var (
		_ notifications.Inbox    = h.store
		_ notifications.Registry = h.store
	)

	var _ interface {
		RegisterOn(*grpc.Server)
	} = h.server
}
