package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestKindToProtoCoversTheClosedSetAndNothingElse walks the four and one that
// is not.
//
// The default case is the one worth having: a kind this package does not
// implement renders as unspecified rather than as one of the four, because
// answering "it is a string" about a value nothing parses is the coercion
// settings.Kind exists to refuse. It cannot arrive from a request — the
// unspecified case reads back as the empty kind, which the store refuses — so
// the only way to reach it is a row written by something else.
func TestKindToProtoCoversTheClosedSetAndNothingElse(T *testing.T) {
	T.Parallel()

	cases := map[settings.Kind]settingspb.SettingKind{
		settings.KindString:       settingspb.SettingKind_SETTING_KIND_STRING,
		settings.KindBool:         settingspb.SettingKind_SETTING_KIND_BOOLEAN,
		settings.KindInt:          settingspb.SettingKind_SETTING_KIND_INTEGER,
		settings.KindFloat:        settingspb.SettingKind_SETTING_KIND_FLOAT,
		settings.Kind("duration"): settingspb.SettingKind_SETTING_KIND_UNSPECIFIED,
		settings.Kind(""):         settingspb.SettingKind_SETTING_KIND_UNSPECIFIED,
	}

	for kind, want := range cases {
		test.EqOp(T, want, settingsgrpc.KindToProto(kind), test.Sprintf("kind %q", kind))
	}
}

// TestSourceToProtoCoversTheThreeCases, and answers unspecified for a fourth: a
// client switching on the source must not be told "the subject chose it" about
// a state nobody has named.
func TestSourceToProtoCoversTheThreeCases(T *testing.T) {
	T.Parallel()

	cases := map[settings.Source]settingspb.ValueSource{
		settings.SourceSubject:     settingspb.ValueSource_VALUE_SOURCE_SUBJECT,
		settings.SourceDefault:     settingspb.ValueSource_VALUE_SOURCE_DEFAULT,
		settings.SourceUnset:       settingspb.ValueSource_VALUE_SOURCE_UNSET,
		settings.Source("guessed"): settingspb.ValueSource_VALUE_SOURCE_UNSPECIFIED,
	}

	for source, want := range cases {
		test.EqOp(T, want, settingsgrpc.SourceToProto(source), test.Sprintf("source %q", source))
	}
}

// TestTypedValueToProtoReadsEachKindThroughTheAccessorsSettingsShips is the
// parse this package exists to have made once, made here rather than in every
// consumer's generated client.
func TestTypedValueToProtoReadsEachKindThroughTheAccessorsSettingsShips(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		assert func(t *testing.T, got *settingspb.TypedValue)
		kind   settings.Kind
		raw    string
	}{
		"a string": {
			kind: settings.KindString, raw: "weekly",
			assert: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, "weekly", got.GetStringValue())
			},
		},
		"a boolean": {
			kind: settings.KindBool, raw: "true",
			assert: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.True(t, got.GetBoolValue())
			},
		},
		"an integer": {
			kind: settings.KindInt, raw: "-12",
			assert: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, int64(-12), got.GetIntValue())
			},
		},
		"a float": {
			kind: settings.KindFloat, raw: "1.25",
			assert: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, 1.25, got.GetFloatValue())
			},
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			typed, err := settingsgrpc.TypedValueToProto(&settings.Resolution{
				Definition: &settings.Definition{Name: digestSetting, Kind: tc.kind},
				Raw:        tc.raw,
				Source:     settings.SourceSubject,
			})
			must.NoError(t, err)
			must.NotNil(t, typed)

			tc.assert(t, typed)
		})
	}
}

// TestAnUnsetResolutionHasNoTypedValueAndIsNotAnError is the tri-state arriving
// on the wire as data.
//
// settings.Resolution.readable reports settings.ErrSettingUnset to a Go caller
// asking for a typed value here, and this surface answers with the source
// instead — because "nobody has decided" is an answer, and a refusal would make
// every settings page's most ordinary row an error case.
func TestAnUnsetResolutionHasNoTypedValueAndIsNotAnError(T *testing.T) {
	T.Parallel()

	typed, err := settingsgrpc.TypedValueToProto(&settings.Resolution{
		Definition: &settings.Definition{Name: retentionSetting, Kind: settings.KindInt},
		Source:     settings.SourceUnset,
	})
	must.NoError(T, err)
	test.Nil(T, typed)
}

// TestAStoredValueThatWillNotParseFailsTheRead is the other direction, and it
// is deliberate rather than incidental.
//
// This surface cannot have written such a row — a value set through it is
// checked against the definition twice — so it is a row something else wrote.
// Answering with the resolution and no value would be the silent reading: a
// settings page showing a default the subject did not choose.
func TestAStoredValueThatWillNotParseFailsTheRead(T *testing.T) {
	T.Parallel()

	resolution := &settings.Resolution{
		Definition: &settings.Definition{Name: retentionSetting, Kind: settings.KindInt},
		Raw:        "thirty",
		Source:     settings.SourceSubject,
	}

	typed, err := settingsgrpc.TypedValueToProto(resolution)
	test.Nil(T, typed)
	must.Error(T, err)
	test.ErrorIs(T, err, settings.ErrMalformedValue)

	// And it fails the whole conversion rather than one field of it, on both
	// the single read and the page.
	_, err = settingsgrpc.ResolutionToProto(resolution)
	test.ErrorIs(T, err, settings.ErrMalformedValue)

	_, err = settingsgrpc.ResolutionsToProto([]*settings.Resolution{resolution})
	test.ErrorIs(T, err, settings.ErrMalformedValue)
}

// TestAKindNothingImplementsIsRefusedRatherThanRendered: a resolution whose
// definition names a kind this package cannot parse has no typed value to
// answer with, and saying so is better than picking one of four.
func TestAKindNothingImplementsIsRefusedRatherThanRendered(T *testing.T) {
	T.Parallel()

	_, err := settingsgrpc.TypedValueToProto(&settings.Resolution{
		Definition: &settings.Definition{Name: digestSetting, Kind: settings.Kind("duration")},
		Raw:        "5m",
		Source:     settings.SourceSubject,
	})
	must.Error(T, err)
	test.ErrorIs(T, err, settings.ErrUnknownKind)
}

// TestTheConvertersRefuseNothingRatherThanRenderIt keeps a nil from becoming an
// empty message somebody would read as a row.
func TestTheConvertersRefuseNothingRatherThanRenderIt(T *testing.T) {
	T.Parallel()

	test.Nil(T, settingsgrpc.DefinitionToProto(nil))
	test.Nil(T, settingsgrpc.ValueToProto(nil))
	test.SliceEmpty(T, settingsgrpc.DefinitionsToProto(nil))
	test.SliceEmpty(T, settingsgrpc.ValuesToProto(nil))

	_, err := settingsgrpc.ResolutionToProto(nil)
	must.Error(T, err)
	test.ErrorIs(T, err, platformerrors.ErrNilInputParameter)

	_, err = settingsgrpc.TypedValueToProto(&settings.Resolution{})
	must.Error(T, err)
	test.ErrorIs(T, err, platformerrors.ErrNilInputParameter)
}

// TestTheNullableTimesStayUnset: a client rendering "last edited" wants to know
// there was no edit, and 1970 is not that answer.
func TestTheNullableTimesStayUnset(T *testing.T) {
	T.Parallel()

	edited := time.Now().UTC().Truncate(time.Second)

	definition := settingsgrpc.DefinitionToProto(&settings.Definition{
		CreatedAt: edited,
		ID:        "def-1",
		Name:      digestSetting,
		Kind:      settings.KindString,
		Scope:     tenancy.Of("acme"),
	})
	must.NotNil(T, definition)
	test.Nil(T, definition.GetLastUpdatedAt())
	test.Nil(T, definition.GetArchivedAt())
	test.Nil(T, definition.DefaultValue)

	edited2 := edited.Add(time.Minute)

	value := settingsgrpc.ValueToProto(&settings.Value{
		CreatedAt:     edited,
		LastUpdatedAt: &edited2,
		Subject:       testSubject,
		ID:            "val-1",
		DefinitionID:  "def-1",
		Raw:           "daily",
	})
	must.NotNil(T, value)
	test.EqOp(T, edited2.Unix(), value.GetLastUpdatedAt().AsTime().Unix())
	test.Nil(T, value.GetArchivedAt())

	// And a default of the empty string is present, which is the difference
	// this whole doctrine is about.
	withDefault := settingsgrpc.DefinitionToProto(&settings.Definition{
		Name:    channelSetting,
		Kind:    settings.KindString,
		Default: pointer.To(""),
	})
	must.NotNil(T, withDefault.DefaultValue)
	test.EqOp(T, "", withDefault.GetDefaultValue())
}

// TestNoConverterPutsAScopeOnTheWire is the Go half of the schema's reserved
// name: even a definition that carries one is rendered without it, because the
// message has nowhere to put it.
func TestNoConverterPutsAScopeOnTheWire(T *testing.T) {
	T.Parallel()

	definition := settingsgrpc.DefinitionToProto(&settings.Definition{
		Name:  digestSetting,
		Kind:  settings.KindString,
		Scope: tenancy.Of("acme"),
	})

	test.Nil(T, definition.ProtoReflect().Descriptor().Fields().ByName("scope"),
		test.Sprint("a definition message grew a scope field"))
}
