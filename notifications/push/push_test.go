package push_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/notifications"
	notificationsmock "github.com/primandproper/platform-go/v14/notifications/mock"
	"github.com/primandproper/platform-go/v14/notifications/push"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/notifications/mobile"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewFanout(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil registry", func(t *testing.T) {
		t.Parallel()

		fanout, err := push.NewFanout(nil, newStubSender(nil))
		test.Nil(t, fanout)
		test.ErrorIs(t, err, push.ErrNilRegistry)
	})

	T.Run("refuses a nil sender", func(t *testing.T) {
		t.Parallel()

		// Refused rather than defaulted to the noop sender: a fan-out that
		// reported every push delivered and sent none is the failure this
		// package exists to end, wearing a successful result.
		fanout, err := push.NewFanout(resolving(nil, nil), nil)
		test.Nil(t, fanout)
		test.ErrorIs(t, err, push.ErrNilSender)
	})

	T.Run("ignores a nil option", func(t *testing.T) {
		t.Parallel()

		fanout, err := push.NewFanout(resolving(nil, nil), newStubSender(nil), nil)
		must.NoError(t, err)
		test.NotNil(t, fanout)
	})

	T.Run("needs no observability at all", func(t *testing.T) {
		t.Parallel()

		// Absent means noop, everywhere. A consumer that wants none of the three
		// names none of them, and the fan-out still runs.
		fanout := newFanout(t, resolving([]*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
		}, nil), newStubSender(nil))

		result, err := fanout.Push(t.Context(), reader, testScope, []string{firstPrincipal}, testMessage)
		must.NoError(t, err)
		test.EqOp(t, 1, result.Sent)
	})
}

func TestFanout_Push(T *testing.T) {
	T.Parallel()

	T.Run("resolves the recipients and sends to every handset", func(t *testing.T) {
		t.Parallel()

		devices := []*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
			device(firstPrincipal, notifications.PlatformAndroid, "token-b"),
			device(secondPrincipal, notifications.PlatformIOS, "token-c"),
		}

		registry := resolving(devices, nil)
		sender := newStubSender(nil)
		fanout := newFanout(t, registry, sender)

		principals := []string{firstPrincipal, secondPrincipal}

		result, err := fanout.Push(t.Context(), reader, testScope, principals, testMessage)
		must.NoError(t, err)

		// One query for the tokens, not one per person.
		resolves := registry.ListDevicesByPrincipalsCalls()
		must.SliceLen(t, 1, resolves)
		test.Eq(t, principals, resolves[0].Principals)
		test.EqOp(t, testScope, resolves[0].Scope)

		// The caller's executor is what the read ran on, so a fan-out inside the
		// transaction a handset was registered in sees it.
		test.EqOp(t, database.SQLQueryExecutor(reader), resolves[0].Q)

		// Each token gets its own send, labeled with its own platform.
		test.Eq(t, []sentPush{
			{Platform: "ios", Token: "token-a", Message: testMessage},
			{Platform: "android", Token: "token-b", Message: testMessage},
			{Platform: "ios", Token: "token-c", Message: testMessage},
		}, sender.sent)

		test.EqOp(t, 3, result.Sent)
		test.EqOp(t, 0, result.Failed)
		test.EqOp(t, 0, result.Invalidated)
		must.SliceLen(t, 3, result.Deliveries)

		for i, delivery := range result.Deliveries {
			test.NoError(t, delivery.Err)
			test.False(t, delivery.Invalidated)
			test.EqOp(t, devices[i], delivery.Device)
		}

		// Nothing was pruned, because nothing was rejected.
		test.SliceEmpty(t, registry.InvalidateDeviceTokenCalls())
	})

	T.Run("answers an announcement nobody has a handset for", func(t *testing.T) {
		t.Parallel()

		sender := newStubSender(nil)
		fanout := newFanout(t, resolving(nil, nil), sender)

		result, err := fanout.Push(t.Context(), reader, testScope, []string{firstPrincipal}, testMessage)
		must.NoError(t, err)
		must.NotNil(t, result)

		test.SliceEmpty(t, result.Deliveries)
		test.EqOp(t, 0, result.Sent)
		test.SliceEmpty(t, sender.sent)
	})

	T.Run("stops on a resolve that failed", func(t *testing.T) {
		t.Parallel()

		// The one failure that leaves nothing sent, and the one that answers
		// with no result: there is nothing to describe.
		registry := &notificationsmock.RegistryMock{
			ListDevicesByPrincipalsFunc: func(
				context.Context,
				database.SQLQueryExecutor,
				tenancy.Scope,
				[]string,
			) ([]*notifications.Device, error) {
				return nil, errRegistryUnavailable
			},
		}

		sender := newStubSender(nil)
		fanout := newFanout(t, registry, sender)

		result, err := fanout.Push(t.Context(), reader, testScope, []string{firstPrincipal}, testMessage)
		test.Nil(t, result)
		test.ErrorIs(t, err, errRegistryUnavailable)
		test.SliceEmpty(t, sender.sent)
	})

	T.Run("carries on past a handset that refused", func(t *testing.T) {
		t.Parallel()

		// One person's provider being unreachable is not a reason the other two
		// hear nothing.
		devices := []*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
			device(firstPrincipal, notifications.PlatformIOS, "token-b"),
			device(secondPrincipal, notifications.PlatformAndroid, "token-c"),
		}

		registry := resolving(devices, nil)
		sender := newStubSender(map[string]error{"token-b": errProviderUnreachable})
		fanout := newFanout(t, registry, sender)

		result, err := fanout.Push(t.Context(), reader, testScope,
			[]string{firstPrincipal, secondPrincipal}, testMessage)

		// Both answers: the failure is reported, and so is everything that did
		// arrive.
		test.ErrorIs(t, err, errProviderUnreachable)
		must.NotNil(t, result)

		test.Eq(t, []string{"token-a", "token-b", "token-c"}, sender.tokensSent())
		test.EqOp(t, 2, result.Sent)
		test.EqOp(t, 1, result.Failed)

		must.SliceLen(t, 3, result.Deliveries)
		test.ErrorIs(t, result.Deliveries[1].Err, errProviderUnreachable)
		test.EqOp(t, "token-b", result.Deliveries[1].Device.Token)

		// A provider that could not be reached says nothing about the token, so
		// the row stays.
		test.SliceEmpty(t, registry.InvalidateDeviceTokenCalls())
		test.EqOp(t, 0, result.Invalidated)
	})

	T.Run("reports every failure together", func(t *testing.T) {
		t.Parallel()

		registry := resolving([]*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
			device(secondPrincipal, notifications.PlatformAndroid, "token-b"),
		}, nil)

		sender := newStubSender(map[string]error{
			"token-a": errProviderUnreachable,
			"token-b": deadToken("fcm: UNREGISTERED"),
		})

		result, err := newFanout(t, registry, sender).Push(t.Context(), reader, testScope,
			[]string{firstPrincipal, secondPrincipal}, testMessage)

		// Joined rather than first-wins: a caller that checks only the error
		// still learns about both.
		test.ErrorIs(t, err, errProviderUnreachable)
		test.ErrorIs(t, err, mobile.ErrTokenInvalid)
		test.EqOp(t, 2, result.Failed)
		test.EqOp(t, 0, result.Sent)
	})

	T.Run("prunes the token the provider called dead", func(t *testing.T) {
		t.Parallel()

		devices := []*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
			device(secondPrincipal, notifications.PlatformAndroid, "token-b"),
		}

		registry := resolving(devices, nil)
		sender := newStubSender(map[string]error{"token-b": deadToken("fcm: UNREGISTERED")})
		fanout := newFanout(t, registry, sender)

		result, err := fanout.Push(t.Context(), reader, testScope,
			[]string{firstPrincipal, secondPrincipal}, testMessage)

		// The send still failed. Whether the registry has since been tidied is a
		// different fact, and not one that turns a push that did not arrive into
		// one that did.
		test.ErrorIs(t, err, mobile.ErrTokenInvalid)

		prunes := registry.InvalidateDeviceTokenCalls()
		must.SliceLen(t, 1, prunes)
		test.EqOp(t, "android", prunes[0].Platform)
		test.EqOp(t, "token-b", prunes[0].Token)

		test.EqOp(t, 1, result.Sent)
		test.EqOp(t, 1, result.Failed)
		test.EqOp(t, 1, result.Invalidated)

		test.False(t, result.Deliveries[0].Invalidated)
		test.True(t, result.Deliveries[1].Invalidated)
	})

	T.Run("matches the sentinel rather than the provider's wording", func(t *testing.T) {
		t.Parallel()

		// The property the whole package turns on. Two adapters word a dead
		// token differently, a third will word it differently again next
		// release, and all of them are the same sentinel underneath — which is
		// why nothing here reads the message.
		devices := []*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "apns-token"),
			device(firstPrincipal, notifications.PlatformAndroid, "fcm-token"),
			device(secondPrincipal, notifications.PlatformIOS, "reworded-token"),
		}

		registry := resolving(devices, nil)
		sender := newStubSender(map[string]error{
			"apns-token":     deadToken("apns: Unregistered"),
			"fcm-token":      deadToken("fcm: UNREGISTERED"),
			"reworded-token": deadToken("apns: this device token is no longer valid"),
		})

		result, err := newFanout(t, registry, sender).Push(t.Context(), reader, testScope,
			[]string{firstPrincipal, secondPrincipal}, testMessage)
		test.ErrorIs(t, err, mobile.ErrTokenInvalid)

		test.EqOp(t, 3, result.Invalidated)
		must.SliceLen(t, 3, registry.InvalidateDeviceTokenCalls())

		// The provider's own words survive on the delivery, because the sentinel
		// is wrapped around them rather than substituted for them.
		test.StrContains(t, result.Deliveries[0].Err.Error(), "apns: Unregistered")
	})

	T.Run("leaves a live token alone", func(t *testing.T) {
		t.Parallel()

		// A push that succeeded is not a reason to ask anything of the registry.
		registry := resolving([]*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
		}, nil)

		result, err := newFanout(t, registry, newStubSender(nil)).Push(t.Context(), reader, testScope,
			[]string{firstPrincipal}, testMessage)
		must.NoError(t, err)

		test.SliceEmpty(t, registry.InvalidateDeviceTokenCalls())
		test.EqOp(t, 0, result.Invalidated)
	})

	T.Run("keeps the provider's diagnosis when the prune fails", func(t *testing.T) {
		t.Parallel()

		// The failure that is swallowed, and the one place it stays visible: the
		// delivery says the token is dead and says the prune did not take. What
		// it must not do is replace "this handset is gone" with "the database
		// was busy".
		registry := resolving([]*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
		}, errPruneRefused)

		result, err := newFanout(t, registry,
			newStubSender(map[string]error{"token-a": deadToken("apns: Unregistered")})).
			Push(t.Context(), reader, testScope, []string{firstPrincipal}, testMessage)

		test.ErrorIs(t, err, mobile.ErrTokenInvalid)
		test.False(t, errors.Is(err, errPruneRefused))

		must.SliceLen(t, 1, result.Deliveries)
		test.ErrorIs(t, result.Deliveries[0].Err, mobile.ErrTokenInvalid)
		test.False(t, result.Deliveries[0].Invalidated)
		test.EqOp(t, 0, result.Invalidated)
		test.EqOp(t, 1, result.Failed)
	})
}
