package settings

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reserved asserts the catalog's AdminOnly flag on the wire.
//
// SetValue and ClearValue each serve two kinds of setting under one method
// grant, and whether this one is reserved is a fact about the definition the
// request names. So the refusal is asked inside the handler, and an ordinary
// caller — every self-service member holds the grant that writes their own
// settings — is exactly who it has to refuse.
func reserved(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("an ordinary caller may neither set nor clear a reserved setting, and the refusal stores nothing", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)

		op := operator(t, s, caller)
		reservedName, openName := "conformance.reserved."+identifiers.New(), "conformance.open."+identifiers.New()

		define(t, op, &settingspb.SettingDefinitionInput{
			Name:         reservedName,
			Kind:         settingspb.SettingKind_SETTING_KIND_INTEGER,
			DefaultValue: new("90"),
			AdminOnly:    true,
		})
		define(t, op, &settingspb.SettingDefinitionInput{
			Name: openName,
			Kind: settingspb.SettingKind_SETTING_KIND_INTEGER,
		})

		// The positive control: the same caller writes a setting of the same
		// kind that nobody reserved. Without it, a caller refused every write
		// would pass the refusal below.
		set(t, caller, openName, intValue(7))

		ctx := caller.Context(t.Context())

		_, err := caller.Surfaces.Settings.SetValue(ctx,
			&settingspb.SetValueRequest{Subject: self(caller), Name: reservedName, Value: intValue(7)})
		must.Error(t, err, must.Sprint("an ordinary caller set a reserved setting"))

		// PermissionDenied and not Internal: the refusal is raised inside the
		// write's transaction, whose error path defaults to Internal.
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		// Clearing decides the setting for the subject exactly as naming a
		// value does, so it is refused on the same terms.
		_, err = caller.Surfaces.Settings.ClearValue(ctx,
			&settingspb.ClearValueRequest{Subject: self(caller), Name: reservedName})
		must.Error(t, err, must.Sprint("an ordinary caller cleared a reserved setting"))
		test.EqOp(t, codes.PermissionDenied, status.Code(err))

		// Still the default, which is what a setting nobody answered resolves
		// to — the refused write was not applied and then rolled back into
		// something else.
		resolved, err := caller.Surfaces.Settings.Resolve(ctx,
			&settingspb.ResolveRequest{Subject: self(caller), Name: reservedName})
		must.NoError(t, err)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, resolved.GetResolution().GetSource())
		test.EqOp(t, int64(90), resolved.GetResolution().GetTypedValue().GetIntValue())
	})

	// The other half, and the one that makes the refusal above a rule rather
	// than a setting nobody can write. It needs an administrator, which only a
	// subject with a notion of one can mint.
	t.Run("an administrator may set and clear a reserved setting", func(t *testing.T) {
		t.Parallel()

		admin := s.Subject(t, conformance.AsAdmin())
		needsUser(t, admin)

		name := "conformance.reserved." + identifiers.New()
		define(t, admin, &settingspb.SettingDefinitionInput{
			Name:         name,
			Kind:         settingspb.SettingKind_SETTING_KIND_INTEGER,
			DefaultValue: new("90"),
			AdminOnly:    true,
		})

		resolution := set(t, admin, name, intValue(7))
		test.EqOp(t, int64(7), resolution.GetTypedValue().GetIntValue())

		cleared, err := admin.Surfaces.Settings.ClearValue(admin.Context(t.Context()),
			&settingspb.ClearValueRequest{Subject: self(admin), Name: name})
		must.NoError(t, err)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, cleared.GetResolution().GetSource())
	})
}
