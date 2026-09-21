package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/v2/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reservedSetting is a setting the catalog marked AdminOnly, which
// seedCatalog's five deliberately are not: every other test in this package is
// about a setting an ordinary member may answer, and one of them being reserved
// would make them all tests of this file.
const reservedSetting = "audit.retention.days"

// seedReserved defines the reserved setting and hands back what was stored.
func seedReserved(tb testing.TB, h *harness) *settings.Definition {
	tb.Helper()

	return h.seed(tb, testScope, &settings.Definition{
		Name:      reservedSetting,
		Kind:      settings.KindInt,
		Default:   pointer.To("90"),
		AdminOnly: true,
	})
}

// memberCtx is a caller who may write their own settings and holds nothing
// else — which is every self-service member, because PermissionWriteValues is
// what self-service means.
func memberCtx(tb testing.TB, h *harness) context.Context {
	tb.Helper()

	return withGrants(h.ctx(tb), settingsgrpc.PermissionWriteValues, settingsgrpc.PermissionReadValues)
}

// adminCtx is that caller plus the grant this file adds.
func adminCtx(tb testing.TB, h *harness) context.Context {
	tb.Helper()

	return withGrants(h.ctx(tb),
		settingsgrpc.PermissionWriteValues,
		settingsgrpc.PermissionReadValues,
		settingsgrpc.PermissionWriteAdminValues)
}

func setReserved(ctx context.Context, h *harness, raw int64) error {
	_, err := h.server.SetValue(ctx, &settingspb.SetValueRequest{
		Subject: subjectOf(testSubject),
		Name:    reservedSetting,
		Value:   &settingspb.TypedValue{Value: &settingspb.TypedValue_IntValue{IntValue: raw}},
	})

	return err
}

// TestSetValue_ReservedSetting is the finding this file closes: an ordinary
// member holding the grant that lets them set their own preferences could set a
// setting the deployment reserved, because both are one method under one grant.
func TestSetValue_ReservedSetting(T *testing.T) {
	T.Parallel()

	T.Run("a member holding only the write grant is refused", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		seedReserved(t, h)

		err := setReserved(memberCtx(t, h), h, 7)
		must.Error(t, err)

		// PermissionDenied and not Internal. The refusal is raised inside
		// Client.WithTransaction, whose error path passes codes.Internal as its
		// default, so this asserts that the status prepared at the refusal
		// survives it rather than being overwritten on the way out.
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})

	T.Run("the refusal stores nothing", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		seedReserved(t, h)

		must.Error(t, setReserved(memberCtx(t, h), h, 7))

		// The default, which is what a setting nobody has answered resolves to.
		response, err := h.server.Resolve(adminCtx(t, h), &settingspb.ResolveRequest{
			Subject: subjectOf(testSubject),
			Name:    reservedSetting,
		})
		must.NoError(t, err)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, response.GetResolution().GetSource())
	})

	T.Run("a holder of the admin grant writes it", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		seedReserved(t, h)

		must.NoError(t, setReserved(adminCtx(t, h), h, 7))
	})

	T.Run("an unreserved setting is untouched by any of this", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)

		_, err := h.server.SetValue(memberCtx(t, h), &settingspb.SetValueRequest{
			Subject: subjectOf(testSubject),
			Name:    compactSetting,
			Value:   &settingspb.TypedValue{Value: &settingspb.TypedValue_BoolValue{BoolValue: true}},
		})
		must.NoError(t, err)
	})

	// The fail-closed half of callerGrants' default, read at this door. A
	// surface that cannot see what the caller may do cannot tell an
	// administrator from anybody else.
	T.Run("a server with no grants extractor refuses every reserved write", func(t *testing.T) {
		t.Parallel()

		// Options apply in order, so this one lands after the harness's own
		// and leaves the server with no extractor at all.
		h := newHarness(t, nil, settingsgrpc.WithGrantsExtractor(nil))
		h.seedCatalog(t, testScope)
		seedReserved(t, h)

		err := setReserved(adminCtx(t, h), h, 7)
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})
}

// TestClearValue_ReservedSetting is the same ruling on the other write. Taking
// an administrator's answer back returns the setting to its default, which
// decides it for the subject exactly as naming a value does.
func TestClearValue_ReservedSetting(T *testing.T) {
	T.Parallel()

	T.Run("a member may not clear what they may not set", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		seedReserved(t, h)
		must.NoError(t, setReserved(adminCtx(t, h), h, 7))

		_, err := h.server.ClearValue(memberCtx(t, h), &settingspb.ClearValueRequest{
			Subject: subjectOf(testSubject),
			Name:    reservedSetting,
		})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		// And the administrator's answer still stands, which is the half a
		// refusal that rolled back after clearing would not have.
		response, resolveErr := h.server.Resolve(adminCtx(t, h), &settingspb.ResolveRequest{
			Subject: subjectOf(testSubject),
			Name:    reservedSetting,
		})
		must.NoError(t, resolveErr)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, response.GetResolution().GetSource())
	})

	T.Run("a holder of the admin grant clears it", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		seedReserved(t, h)
		must.NoError(t, setReserved(adminCtx(t, h), h, 7))

		_, err := h.server.ClearValue(adminCtx(t, h), &settingspb.ClearValueRequest{
			Subject: subjectOf(testSubject),
			Name:    reservedSetting,
		})
		must.NoError(t, err)
	})
}
