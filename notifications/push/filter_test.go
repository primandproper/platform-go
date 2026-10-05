package push_test

import (
	"context"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v15/notifications"
	notificationsmock "github.com/primandproper/platform-go/v15/notifications/mock"
	"github.com/primandproper/platform-go/v15/notifications/push"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/notifications/mobile"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errPreferenceUnavailable is the filter's read failing — a preference the
// fan-out could not learn, and so one it must not guess at.
var errPreferenceUnavailable = platformerrors.New("the recipient's preference is unavailable")

// filterCall is one question the fan-out asked its filter.
type filterCall struct {
	Q         database.SQLQueryExecutor
	Message   mobile.PushMessage
	Principal string
	Scope     tenancy.Scope
}

// recordingFilter is a filter that answers from a set of principals who opted
// out, and records every question it was asked.
type recordingFilter struct {
	optedOut map[string]bool
	err      error
	calls    []filterCall
}

func (r *recordingFilter) filter(
	_ context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	principal string,
	msg mobile.PushMessage,
) (bool, error) {
	r.calls = append(r.calls, filterCall{Q: q, Scope: scope, Principal: principal, Message: msg})

	if r.err != nil {
		return false, r.err
	}

	return !r.optedOut[principal], nil
}

func (r *recordingFilter) principalsAsked() []string {
	asked := make([]string, 0, len(r.calls))
	for i := range r.calls {
		asked = append(asked, r.calls[i].Principal)
	}

	return asked
}

// resolvingOwn is a registry that answers the read with only the devices
// belonging to the principals it was asked about, the way a real one does — so
// a test can tell a principal who was left out of the read from one who was
// left out of the loop.
func resolvingOwn(devices []*notifications.Device) *notificationsmock.RegistryMock {
	return &notificationsmock.RegistryMock{
		ListDevicesByPrincipalsFunc: func(
			_ context.Context,
			_ database.SQLQueryExecutor,
			_ tenancy.Scope,
			principals []string,
		) ([]*notifications.Device, error) {
			var own []*notifications.Device

			for _, d := range devices {
				if slices.Contains(principals, d.Principal) {
					own = append(own, d)
				}
			}

			return own, nil
		},
		InvalidateDeviceTokenFunc: func(context.Context, string, string) error { return nil },
	}
}

func TestFanout_Push_RecipientFilter(T *testing.T) {
	T.Parallel()

	twoPeople := func() []*notifications.Device {
		return []*notifications.Device{
			device(firstPrincipal, notifications.PlatformIOS, "token-a"),
			device(firstPrincipal, notifications.PlatformAndroid, "token-b"),
			device(secondPrincipal, notifications.PlatformIOS, "token-c"),
		}
	}

	T.Run("leaves out a principal the filter refused", func(t *testing.T) {
		t.Parallel()

		devices := twoPeople()
		registry := resolvingOwn(devices)
		sender := newStubSender(nil)
		filter := &recordingFilter{optedOut: map[string]bool{firstPrincipal: true}}

		result, err := newFanout(t, registry, sender, push.WithRecipientFilter(filter.filter)).
			Push(t.Context(), reader, testScope, []string{firstPrincipal, secondPrincipal}, testMessage)
		must.NoError(t, err)

		// The person who opted out is not asked about in the read, so their
		// handsets are never resolved, let alone sent to.
		resolves := registry.ListDevicesByPrincipalsCalls()
		must.SliceLen(t, 1, resolves)
		test.Eq(t, []string{secondPrincipal}, resolves[0].Principals)

		test.Eq(t, []string{"token-c"}, sender.tokensSent())

		must.SliceLen(t, 1, result.Deliveries)
		test.EqOp(t, devices[2], result.Deliveries[0].Device)
		test.EqOp(t, 1, result.Sent)

		// "Asked not to receive this" rather than "registered no handsets".
		test.Eq(t, []string{firstPrincipal}, result.Skipped)
	})

	T.Run("asks with the executor, scope and message it was handed", func(t *testing.T) {
		t.Parallel()

		// The executor is the caller's, so a filter reading a preference runs in
		// the caller's transaction and sees one written earlier in it.
		filter := &recordingFilter{}

		_, err := newFanout(t, resolvingOwn(twoPeople()), newStubSender(nil), push.WithRecipientFilter(filter.filter)).
			Push(t.Context(), reader, testScope, []string{firstPrincipal}, testMessage)
		must.NoError(t, err)

		test.Eq(t, []filterCall{{
			Q:         reader,
			Scope:     testScope,
			Principal: firstPrincipal,
			Message:   testMessage,
		}}, filter.calls)
	})

	T.Run("asks once per person rather than once per handset", func(t *testing.T) {
		t.Parallel()

		// The first principal has two handsets and is named twice. A preference
		// is a person's, so that is one question, and one name in the read.
		registry := resolvingOwn(twoPeople())
		sender := newStubSender(nil)
		filter := &recordingFilter{optedOut: map[string]bool{secondPrincipal: true}}

		result, err := newFanout(t, registry, sender, push.WithRecipientFilter(filter.filter)).
			Push(t.Context(), reader, testScope,
				[]string{firstPrincipal, secondPrincipal, firstPrincipal, secondPrincipal}, testMessage)
		must.NoError(t, err)

		test.Eq(t, []string{firstPrincipal, secondPrincipal}, filter.principalsAsked())
		test.Eq(t, []string{firstPrincipal}, registry.ListDevicesByPrincipalsCalls()[0].Principals)
		test.Eq(t, []string{"token-a", "token-b"}, sender.tokensSent())
		test.Eq(t, []string{secondPrincipal}, result.Skipped)
	})

	T.Run("sends nothing when the filter fails", func(t *testing.T) {
		t.Parallel()

		// Abort rather than fail open. Nothing has gone out, so there is no
		// partial fan-out to describe, and a preference that gave way on a
		// database blip is an opt-out that did not hold.
		registry := resolvingOwn(twoPeople())
		sender := newStubSender(nil)
		filter := &recordingFilter{err: errPreferenceUnavailable}

		result, err := newFanout(t, registry, sender, push.WithRecipientFilter(filter.filter)).
			Push(t.Context(), reader, testScope, []string{firstPrincipal, secondPrincipal}, testMessage)
		test.Nil(t, result)
		test.ErrorIs(t, err, errPreferenceUnavailable)

		// The first failure stops the questions, and nothing is resolved or sent.
		test.Eq(t, []string{firstPrincipal}, filter.principalsAsked())
		test.SliceEmpty(t, registry.ListDevicesByPrincipalsCalls())
		test.SliceEmpty(t, sender.sent)
	})

	T.Run("answers an announcement everybody declined", func(t *testing.T) {
		t.Parallel()

		sender := newStubSender(nil)
		filter := &recordingFilter{optedOut: map[string]bool{firstPrincipal: true, secondPrincipal: true}}

		result, err := newFanout(t, resolvingOwn(twoPeople()), sender, push.WithRecipientFilter(filter.filter)).
			Push(t.Context(), reader, testScope, []string{firstPrincipal, secondPrincipal}, testMessage)
		must.NoError(t, err)
		must.NotNil(t, result)

		test.SliceEmpty(t, result.Deliveries)
		test.SliceEmpty(t, sender.sent)
		test.Eq(t, []string{firstPrincipal, secondPrincipal}, result.Skipped)
	})

	T.Run("a nil filter asks nothing and skips nobody", func(t *testing.T) {
		t.Parallel()

		// The principals reach the read exactly as they were handed over,
		// duplicates included, which is what the fan-out did before the seam.
		registry := resolvingOwn(twoPeople())
		principals := []string{firstPrincipal, secondPrincipal, firstPrincipal}

		result, err := newFanout(t, registry, newStubSender(nil), push.WithRecipientFilter(nil)).
			Push(t.Context(), reader, testScope, principals, testMessage)
		must.NoError(t, err)

		test.Eq(t, principals, registry.ListDevicesByPrincipalsCalls()[0].Principals)
		test.Nil(t, result.Skipped)
		test.EqOp(t, 3, result.Sent)
	})
}
