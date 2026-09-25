package settings

import (
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func values(t *testing.T, s *conformance.Session) {
	t.Helper()

	// Each case sends the oneof branch its setting's kind declares and reads
	// the same branch back out of the resolution, which is the round trip a
	// generated client makes: a checkbox writes bool_value and renders
	// bool_value, and nothing on either side parses a string.
	t.Run("a value of each kind is answered back as that kind", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)

		cases := []struct {
			sent  *settingspb.TypedValue
			check func(t *testing.T, got *settingspb.TypedValue)
			name  string
		}{
			{name: c.digest, sent: stringValue(optionDaily), check: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, optionDaily, got.GetStringValue())
			}},
			{name: c.compact, sent: boolValue(true), check: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.True(t, got.GetBoolValue())
			}},
			{name: c.retention, sent: intValue(90), check: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, int64(90), got.GetIntValue())
			}},
			{name: c.ratio, sent: floatValue(2.25), check: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, 2.25, got.GetFloatValue())
			}},
		}

		for i := range cases {
			tc := &cases[i]
			resolution := set(t, caller, tc.name, tc.sent)

			test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, resolution.GetSource())
			must.NotNil(t, resolution.GetTypedValue(), must.Sprintf("setting %q answered with no typed value", tc.name))
			tc.check(t, resolution.GetTypedValue())

			// The row travels with it, because a client showing "you chose
			// this on the 3rd" needs it.
			must.NotNil(t, resolution.GetValue())
			test.EqOp(t, caller.UserID, resolution.GetValue().GetSubject().GetId())
		}
	})

	// The whole reason a write carries a typed value rather than a string. The
	// first case is the one a string would have accepted in silence — any text
	// is a valid text setting — and the second its mirror, since "true" parses
	// as a boolean.
	t.Run("a value of the wrong kind is refused and stores nothing", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		ctx := caller.Context(t.Context())

		for _, tc := range []struct {
			sent *settingspb.TypedValue
			name string
		}{
			{name: c.channel, sent: intValue(1)},
			{name: c.compact, sent: stringValue("true")},
			{name: c.retention, sent: floatValue(90)},
		} {
			_, err := caller.Surfaces.Settings.SetValue(ctx,
				&settingspb.SetValueRequest{Subject: self(caller), Name: tc.name, Value: tc.sent})
			must.Error(t, err, must.Sprintf("a value of the wrong kind was stored for %q", tc.name))
			test.EqOp(t, codes.InvalidArgument, status.Code(err))

			_, err = caller.Surfaces.Settings.GetValue(ctx,
				&settingspb.GetValueRequest{Subject: self(caller), Name: tc.name})
			test.EqOp(t, codes.NotFound, status.Code(err),
				test.Sprintf("a refused write to %q left a value behind", tc.name))
		}
	})

	// The empty string is a legal value of a text setting — somebody answered
	// with nothing — and a request naming no case carries no value at all. The
	// two answers are as far apart as a stored row and a refusal.
	t.Run("the empty string is a value and no value is not", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)

		resolution := set(t, caller, c.channel, stringValue(""))
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, resolution.GetSource())
		must.NotNil(t, resolution.GetTypedValue())
		test.EqOp(t, "", resolution.GetTypedValue().GetStringValue())

		for _, value := range []*settingspb.TypedValue{nil, {}} {
			_, err := caller.Surfaces.Settings.SetValue(caller.Context(t.Context()),
				&settingspb.SetValueRequest{Subject: self(caller), Name: c.digest, Value: value})
			must.Error(t, err, must.Sprint("a write naming no value was accepted"))
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		}
	})

	t.Run("a value the setting does not admit is refused", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)

		_, err := caller.Surfaces.Settings.SetValue(caller.Context(t.Context()),
			&settingspb.SetValueRequest{Subject: self(caller), Name: c.digest, Value: stringValue("hourly")})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.StrContains(t, status.Convert(err).Message(), "admits")
	})

	// A value is only meaningful against a definition, so a name nothing
	// defines is absent rather than a row with nothing to check it against.
	t.Run("a value for a setting nobody defined is refused as absent", func(t *testing.T) {
		t.Parallel()

		caller, _ := seeded(t, s)

		_, err := caller.Surfaces.Settings.SetValue(caller.Context(t.Context()), &settingspb.SetValueRequest{
			Subject: self(caller),
			Name:    names().digest,
			Value:   stringValue(optionDaily),
		})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	// The line between the two reads: GetValue is the row, Resolve is the row
	// falling back.
	t.Run("a value read is the row and never the default", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		ctx := caller.Context(t.Context())

		_, err := caller.Surfaces.Settings.GetValue(ctx, &settingspb.GetValueRequest{Subject: self(caller), Name: c.digest})
		must.Error(t, err, must.Sprint("a setting nobody answered read back its default as a stored row"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		set(t, caller, c.digest, stringValue(optionNever))

		response, err := caller.Surfaces.Settings.GetValue(ctx, &settingspb.GetValueRequest{Subject: self(caller), Name: c.digest})
		must.NoError(t, err)
		test.EqOp(t, optionNever, response.GetResult().GetRaw())
		test.EqOp(t, subjectUser, response.GetResult().GetSubject().GetType())
		test.EqOp(t, caller.UserID, response.GetResult().GetSubject().GetId())
	})

	// A screen that has just shown a "reset to default" button has to render
	// something next, and it is the default — or, for a setting with none, the
	// third answer.
	t.Run("clearing a value answers with what the setting resolves to next", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		ctx := caller.Context(t.Context())

		set(t, caller, c.digest, stringValue(optionDaily))
		set(t, caller, c.retention, intValue(30))

		withDefault, err := caller.Surfaces.Settings.ClearValue(ctx,
			&settingspb.ClearValueRequest{Subject: self(caller), Name: c.digest})
		must.NoError(t, err)

		resolution := withDefault.GetResolution()
		must.NotNil(t, resolution)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, resolution.GetSource())
		test.EqOp(t, optionWeekly, resolution.GetTypedValue().GetStringValue())
		test.Nil(t, resolution.GetValue(), test.Sprint("a cleared answer is still live in the resolution"))

		withNone, err := caller.Surfaces.Settings.ClearValue(ctx,
			&settingspb.ClearValueRequest{Subject: self(caller), Name: c.retention})
		must.NoError(t, err)

		resolution = withNone.GetResolution()
		must.NotNil(t, resolution)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_UNSET, resolution.GetSource())

		// No typed value, rather than a zero somebody could mistake for a
		// choice: nobody has decided, and the caller's own policy applies.
		test.Nil(t, resolution.GetTypedValue())
	})

	t.Run("a resolution answers all three cases", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		ctx := caller.Context(t.Context())
		set(t, caller, c.digest, stringValue(optionNever))

		chose, err := caller.Surfaces.Settings.Resolve(ctx, &settingspb.ResolveRequest{Subject: self(caller), Name: c.digest})
		must.NoError(t, err)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, chose.GetResolution().GetSource())
		test.EqOp(t, optionNever, chose.GetResolution().GetTypedValue().GetStringValue())

		undecided, err := caller.Surfaces.Settings.Resolve(ctx, &settingspb.ResolveRequest{Subject: self(caller), Name: c.channel})
		must.NoError(t, err)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_UNSET, undecided.GetResolution().GetSource())
		test.Nil(t, undecided.GetResolution().GetTypedValue())

		// The middle case, which is the one the package exists for: a subject
		// who has not chosen is answered by the definition rather than by a
		// missing row, and the definition travels with the answer because a
		// client rendering a control needs its kind.
		defaulted, err := caller.Surfaces.Settings.Resolve(ctx, &settingspb.ResolveRequest{Subject: self(caller), Name: c.compact})
		must.NoError(t, err)

		resolution := defaulted.GetResolution()
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, resolution.GetSource())
		must.NotNil(t, resolution.GetTypedValue())
		test.False(t, resolution.GetTypedValue().GetBoolValue())
		test.Nil(t, resolution.GetValue())
		must.NotNil(t, resolution.GetDefinition())
		test.EqOp(t, settingspb.SettingKind_SETTING_KIND_BOOLEAN, resolution.GetDefinition().GetKind())
	})

	// The read a settings page makes: the settings nobody answered are in it,
	// at their default or as unset, which is the difference between this and
	// the page of overrides.
	t.Run("resolving everything answers the untouched settings too, in name order", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		set(t, caller, c.digest, stringValue(optionDaily))

		response, err := caller.Surfaces.Settings.ResolveAll(caller.Context(t.Context()),
			&settingspb.ResolveAllRequest{Subject: self(caller)})
		must.NoError(t, err)

		sources := map[string]settingspb.ValueSource{}
		order := make([]string, 0, len(response.GetResolutions()))

		for _, resolution := range response.GetResolutions() {
			sources[resolution.GetDefinition().GetName()] = resolution.GetSource()
			order = append(order, resolution.GetDefinition().GetName())
		}

		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, sources[c.digest])
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, sources[c.compact])
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_UNSET, sources[c.retention])

		// Sorted by name, the store's own order arriving unchanged: a page that
		// rendered rows in whatever order a database returned them would move
		// under somebody every time a value was written.
		test.True(t, slices.IsSorted(order), test.Sprintf("the resolutions arrived out of name order: %v", order))
	})

	t.Run("a subject's page of values holds their overrides and nothing they left alone", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		set(t, caller, c.digest, stringValue(optionDaily))
		set(t, caller, c.retention, intValue(30))

		response, err := caller.Surfaces.Settings.ListValuesForSubject(caller.Context(t.Context()),
			&settingspb.ListValuesForSubjectRequest{Subject: self(caller)})
		must.NoError(t, err)
		test.NotNil(t, response.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		answered := map[string]string{}
		for _, value := range response.GetResults() {
			test.EqOp(t, caller.UserID, value.GetSubject().GetId(),
				test.Sprint("a page of one subject's values held somebody else's"))

			answered[value.GetDefinitionId()] = value.GetRaw()
		}

		op := operator(t, s, caller)
		test.EqOp(t, optionDaily, answered[byName(t, op, c.digest).GetId()])
		test.EqOp(t, "30", answered[byName(t, op, c.retention).GetId()])
		test.MapNotContainsKey(t, answered, byName(t, op, c.compact).GetId(),
			test.Sprint("a setting the subject never answered was listed as an override"))
	})
}
