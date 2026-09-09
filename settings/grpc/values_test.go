package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSetValueStoresEachKindAndAnswersWithTheResolution is the write half of
// the typed value.
//
// Each case sends the oneof branch its setting's kind declares and reads the
// same branch back out of the resolution, which is the round trip a generated
// client makes: a checkbox writes bool_value and renders bool_value, and
// nothing on either side parses a string.
func TestSetValueStoresEachKindAndAnswersWithTheResolution(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		sent *settingspb.TypedValue
		want func(t *testing.T, got *settingspb.TypedValue)
		name string
	}{
		"a string": {
			name: digestSetting,
			sent: stringValue("daily"),
			want: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, "daily", got.GetStringValue())
			},
		},
		"a boolean": {
			name: compactSetting,
			sent: boolValue(true),
			want: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.True(t, got.GetBoolValue())
			},
		},
		"an integer": {
			name: retentionSetting,
			sent: intValue(90),
			want: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, int64(90), got.GetIntValue())
			},
		},
		"a float": {
			name: ratioSetting,
			sent: floatValue(2.25),
			want: func(t *testing.T, got *settingspb.TypedValue) {
				t.Helper()
				test.EqOp(t, 2.25, got.GetFloatValue())
			},
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newSeededHarness(t)

			response, err := h.server.SetValue(h.ctx(t), &settingspb.SetValueRequest{
				Subject: subjectOf(testSubject),
				Name:    tc.name,
				Value:   tc.sent,
			})
			must.NoError(t, err)

			resolution := response.GetResolution()
			must.NotNil(t, resolution)

			test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, resolution.GetSource())
			must.NotNil(t, resolution.GetTypedValue())
			tc.want(t, resolution.GetTypedValue())

			// The row is here too, because a client showing "you chose this on
			// the 3rd" needs it, and it carries the text the store stores.
			must.NotNil(t, resolution.GetValue())
			test.EqOp(t, testSubject.ID, resolution.GetValue().GetSubject().GetId())
		})
	}
}

// TestSetValueRefusesACaseThatIsNotTheSettingsKind is the whole reason a write
// carries a typed value rather than a string.
//
// The first case is the one a string would have accepted in silence:
// settings.KindString admits any text, so an integer written to a text setting
// would have been stored as "1" and nothing would have complained. The second
// is its mirror — "true" parses as a boolean, so a string written to a boolean
// setting would have been stored and read back as the value the client
// accidentally meant.
func TestSetValueRefusesACaseThatIsNotTheSettingsKind(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		sent *settingspb.TypedValue
		name string
	}{
		"an integer into a text setting": {name: channelSetting, sent: intValue(1)},
		"a string into a boolean":        {name: compactSetting, sent: stringValue("true")},
		"a float into an integer":        {name: retentionSetting, sent: floatValue(90)},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newSeededHarness(t)

			response, err := h.server.SetValue(h.ctx(t), &settingspb.SetValueRequest{
				Subject: subjectOf(testSubject),
				Name:    tc.name,
				Value:   tc.sent,
			})
			test.Nil(t, response)
			must.Error(t, err)

			test.ErrorIs(t, err, settings.ErrKindMismatch)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))

			// And nothing was written: the refusal is before the write, inside
			// the transaction that would have held it.
			_, readErr := h.server.GetValue(h.ctx(t), &settingspb.GetValueRequest{
				Subject: subjectOf(testSubject),
				Name:    tc.name,
			})
			test.ErrorIs(t, readErr, settings.ErrValueNotFound)
		})
	}
}

// TestSetValueTellsAnEmptyStringFromNoValueAtAll is the presence the oneof
// exists for, and it is the reason the request does not carry a bare string.
//
// The empty string is a legal value of a text setting — somebody answered with
// nothing — and a request that named no case is a request with no value in it.
// A proto3 string field could not hold the difference, and the two answers here
// are as far apart as a stored row and a refusal.
func TestSetValueTellsAnEmptyStringFromNoValueAtAll(T *testing.T) {
	T.Parallel()

	T.Run("the empty string is a value", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)

		response, err := h.server.SetValue(h.ctx(t), &settingspb.SetValueRequest{
			Subject: subjectOf(testSubject),
			Name:    channelSetting,
			Value:   stringValue(""),
		})
		must.NoError(t, err)

		resolution := response.GetResolution()
		must.NotNil(t, resolution)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, resolution.GetSource())
		must.NotNil(t, resolution.GetTypedValue())
		test.EqOp(t, "", resolution.GetTypedValue().GetStringValue())
	})

	for name, value := range map[string]*settingspb.TypedValue{
		"no message":  nil,
		"no case set": {},
	} {
		T.Run(name+" is not", func(t *testing.T) {
			t.Parallel()

			h := newSeededHarness(t)

			response, err := h.server.SetValue(h.ctx(t), &settingspb.SetValueRequest{
				Subject: subjectOf(testSubject),
				Name:    channelSetting,
				Value:   value,
			})
			test.Nil(t, response)
			must.Error(t, err)

			test.ErrorIs(t, err, settingsgrpc.ErrNoValueNamed)
			test.EqOp(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// TestSetValueRefusesAValueTheSettingDoesNotAdmit is the store's rule arriving
// unchanged, and the reason this surface reads the definition without becoming
// the thing that decides.
//
// The kind check above happens here; this one happens in the store, on its own
// read of the same definition, which is what keeps "a value is of its kind and
// in its enumeration" a rule of the store's rather than one the transport is
// trusted to have applied.
func TestSetValueRefusesAValueTheSettingDoesNotAdmit(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)

	response, err := h.server.SetValue(h.ctx(T), &settingspb.SetValueRequest{
		Subject: subjectOf(testSubject),
		Name:    digestSetting,
		Value:   stringValue("hourly"),
	})
	test.Nil(T, response)
	must.Error(T, err)

	test.ErrorIs(T, err, settings.ErrNotEnumerated)
	test.EqOp(T, codes.InvalidArgument, status.Code(err))
}

// TestSetValueRefusesASettingNobodyDefined: a value is only meaningful against
// a definition, so a name nothing defines is codes.NotFound rather than a row
// with nothing to check it against.
func TestSetValueRefusesASettingNobodyDefined(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)

	_, err := h.server.SetValue(h.ctx(T), &settingspb.SetValueRequest{
		Subject: subjectOf(testSubject),
		Name:    "nothing.defines.this",
		Value:   stringValue("daily"),
	})
	must.Error(T, err)

	test.ErrorIs(T, err, settings.ErrDefinitionNotFound)
	test.EqOp(T, codes.NotFound, status.Code(err))
}

// TestGetValueReadsTheRowAndNotTheDefault is the line between the two reads on
// this surface. GetValue is the row; Resolve is the row falling back.
func TestGetValueReadsTheRowAndNotTheDefault(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)

	// Nobody has answered, and the setting has a default. The row read says so
	// and the resolution answers with the default, which is the whole
	// distinction.
	_, err := h.server.GetValue(h.ctx(T), &settingspb.GetValueRequest{
		Subject: subjectOf(testSubject),
		Name:    digestSetting,
	})
	must.Error(T, err)
	test.ErrorIs(T, err, settings.ErrValueNotFound)
	test.EqOp(T, codes.NotFound, status.Code(err))

	h.seedValue(T, testScope, testSubject, digestSetting, "never")

	response, err := h.server.GetValue(h.ctx(T), &settingspb.GetValueRequest{
		Subject: subjectOf(testSubject),
		Name:    digestSetting,
	})
	must.NoError(T, err)

	value := response.GetResult()
	must.NotNil(T, value)
	test.EqOp(T, "never", value.GetRaw())
	test.EqOp(T, testSubject.Type.String(), value.GetSubject().GetType())
}

// TestClearValueAnswersWithWhatTheSettingResolvesToNext is why the response is
// a resolution rather than an acknowledgement.
//
// A screen that has just shown a "reset to default" button has to render
// something next, and it is the default — or, for a setting that has none, the
// third answer. Reading it back inside the transaction that cleared the row is
// the only place that answer is the one the caller just brought about.
func TestClearValueAnswersWithWhatTheSettingResolvesToNext(T *testing.T) {
	T.Parallel()

	T.Run("a setting with a default", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		h.seedValue(t, testScope, testSubject, digestSetting, "daily")

		response, err := h.server.ClearValue(h.ctx(t), &settingspb.ClearValueRequest{
			Subject: subjectOf(testSubject),
			Name:    digestSetting,
		})
		must.NoError(t, err)

		resolution := response.GetResolution()
		must.NotNil(t, resolution)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, resolution.GetSource())
		test.EqOp(t, "weekly", resolution.GetTypedValue().GetStringValue())

		// The row is gone from the resolution, which is what "the default
		// answered" means: the value the subject chose is no longer live.
		test.Nil(t, resolution.GetValue())
	})

	T.Run("a setting with none", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		h.seedValue(t, testScope, testSubject, retentionSetting, "30")

		response, err := h.server.ClearValue(h.ctx(t), &settingspb.ClearValueRequest{
			Subject: subjectOf(testSubject),
			Name:    retentionSetting,
		})
		must.NoError(t, err)

		resolution := response.GetResolution()
		must.NotNil(t, resolution)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_UNSET, resolution.GetSource())

		// No typed value, and that is the answer rather than a zero somebody
		// could mistake for a choice: nobody has decided, and the caller's own
		// policy applies.
		test.Nil(t, resolution.GetTypedValue())
	})
}

// TestResolveAnswersAllThreeCases is the method this package exists for.
func TestResolveAnswersAllThreeCases(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)
	h.seedValue(T, testScope, testSubject, digestSetting, "never")

	cases := map[string]struct {
		name   string
		typed  string
		want   settingspb.ValueSource
		hasVal bool
	}{
		"the subject chose": {
			name: digestSetting, want: settingspb.ValueSource_VALUE_SOURCE_SUBJECT,
			typed: "never", hasVal: true,
		},
		"nobody has decided": {
			name: channelSetting, want: settingspb.ValueSource_VALUE_SOURCE_UNSET,
		},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			response, err := h.server.Resolve(h.ctx(t), &settingspb.ResolveRequest{
				Subject: subjectOf(testSubject),
				Name:    tc.name,
			})
			must.NoError(t, err)

			resolution := response.GetResolution()
			must.NotNil(t, resolution)
			test.EqOp(t, tc.want, resolution.GetSource())

			if !tc.hasVal {
				test.Nil(t, resolution.GetTypedValue())

				return
			}

			test.EqOp(t, tc.typed, resolution.GetTypedValue().GetStringValue())
		})
	}

	// The middle case, spelled on its own because it is the one the package
	// exists for: a subject who has not chosen is answered by the definition
	// rather than by a missing row.
	T.Run("the definition answered", func(t *testing.T) {
		t.Parallel()

		response, err := h.server.Resolve(h.ctx(t), &settingspb.ResolveRequest{
			Subject: subjectOf(testSubject),
			Name:    compactSetting,
		})
		must.NoError(t, err)

		resolution := response.GetResolution()
		must.NotNil(t, resolution)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, resolution.GetSource())
		test.False(t, resolution.GetTypedValue().GetBoolValue())
		test.Nil(t, resolution.GetValue())

		// And the definition travels with it, because a client rendering a
		// control needs the kind and the values it admits, not just the answer.
		must.NotNil(t, resolution.GetDefinition())
		test.EqOp(t, settingspb.SettingKind_SETTING_KIND_BOOLEAN, resolution.GetDefinition().GetKind())
	})
}

// TestResolveAllAnswersEverySettingIncludingTheUntouched is the read a settings
// page makes.
//
// The settings nobody has answered are in the answer, at their default or as
// unset, which is the difference between this and ListValuesForSubject: a page
// rendering "your preferences" wants a row per setting, not a row per override.
func TestResolveAllAnswersEverySettingIncludingTheUntouched(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)
	h.seedValue(T, testScope, testSubject, digestSetting, "daily")

	response, err := h.server.ResolveAll(h.ctx(T), &settingspb.ResolveAllRequest{
		Subject: subjectOf(testSubject),
	})
	must.NoError(T, err)

	resolutions := response.GetResolutions()
	must.SliceLen(T, 5, resolutions)

	sources := map[string]settingspb.ValueSource{}
	for _, resolution := range resolutions {
		sources[resolution.GetDefinition().GetName()] = resolution.GetSource()
	}

	test.EqOp(T, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, sources[digestSetting])
	test.EqOp(T, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, sources[compactSetting])
	test.EqOp(T, settingspb.ValueSource_VALUE_SOURCE_UNSET, sources[retentionSetting])

	// Sorted by name, which is the store's own order arriving unchanged: a page
	// that rendered its rows in whatever order a database returned them would
	// move under somebody every time a value was written.
	test.EqOp(T, compactSetting, resolutions[0].GetDefinition().GetName())
}

// TestListValuesForSubjectPagesOverridesOnly is the other half of that pair.
func TestListValuesForSubjectPagesOverridesOnly(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)
	h.seedValue(T, testScope, testSubject, digestSetting, "daily")
	h.seedValue(T, testScope, testSubject, retentionSetting, "30")

	response, err := h.server.ListValuesForSubject(h.ctx(T), &settingspb.ListValuesForSubjectRequest{
		Subject: subjectOf(testSubject),
	})
	must.NoError(T, err)

	test.SliceLen(T, 2, response.GetResults())
	test.NotNil(T, response.GetPagination())

	for _, value := range response.GetResults() {
		test.EqOp(T, testSubject.ID, value.GetSubject().GetId())
	}
}

// TestTheSubjectAuthorizerGatesEveryValueRPC is the half of authorization a
// grant on the method cannot reach.
//
// The caller here holds every permission this service declares — there is no
// interceptor in this suite — and is still refused, because the question these
// six ask is not whether they may call the method but whose settings they named.
func TestTheSubjectAuthorizerGatesEveryValueRPC(T *testing.T) {
	T.Parallel()

	subject := subjectOf(strangeSubject)

	cases := map[string]func(t *testing.T, h *harness) error{
		"SetValue": func(t *testing.T, h *harness) error {
			t.Helper()

			_, err := h.server.SetValue(h.ctx(t), &settingspb.SetValueRequest{
				Subject: subject, Name: digestSetting, Value: stringValue("daily"),
			})

			return err
		},
		"GetValue": func(t *testing.T, h *harness) error {
			t.Helper()

			_, err := h.server.GetValue(h.ctx(t), &settingspb.GetValueRequest{
				Subject: subject, Name: digestSetting,
			})

			return err
		},
		"ClearValue": func(t *testing.T, h *harness) error {
			t.Helper()

			_, err := h.server.ClearValue(h.ctx(t), &settingspb.ClearValueRequest{
				Subject: subject, Name: digestSetting,
			})

			return err
		},
		"ListValuesForSubject": func(t *testing.T, h *harness) error {
			t.Helper()

			_, err := h.server.ListValuesForSubject(h.ctx(t), &settingspb.ListValuesForSubjectRequest{
				Subject: subject,
			})

			return err
		},
		"Resolve": func(t *testing.T, h *harness) error {
			t.Helper()

			_, err := h.server.Resolve(h.ctx(t), &settingspb.ResolveRequest{
				Subject: subject, Name: digestSetting,
			})

			return err
		},
		"ResolveAll": func(t *testing.T, h *harness) error {
			t.Helper()

			_, err := h.server.ResolveAll(h.ctx(t), &settingspb.ResolveAllRequest{Subject: subject})

			return err
		},
	}

	for name, call := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newSeededHarness(t)

			err := call(t, h)
			must.Error(t, err)

			test.ErrorIs(t, err, settingsgrpc.ErrTargetNotPermitted)
			test.EqOp(t, codes.PermissionDenied, status.Code(err))
		})
	}

	// And the refusal is decided before anything is read, so a subject nobody
	// has stored a value for is refused exactly as one who belongs to somebody
	// else is. A caller cannot learn which subjects exist by watching the codes.
	T.Run("an account nobody has settings for", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)

		_, err := h.server.Resolve(h.ctx(t), &settingspb.ResolveRequest{
			Subject: subjectOf(accountSubject),
			Name:    digestSetting,
		})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})
}

// TestAnAuthorizerThatCannotDecideIsNotARefusal keeps an unavailable database
// from reading as a policy answer.
//
// Reporting the second as the first tells a consumer to widen their policy
// while their database is down, and leaves a dashboard counting server faults
// reading zero through it.
func TestAnAuthorizerThatCannotDecideIsNotARefusal(T *testing.T) {
	T.Parallel()

	unavailable := platformerrors.New("the membership table is unavailable")

	h := newHarness(T, settingsgrpc.SubjectAuthorizerFunc(
		func(context.Context, settingsgrpc.Principal, settings.Subject) error { return unavailable },
	))
	h.seedCatalog(T, testScope)

	_, err := h.server.Resolve(h.ctx(T), &settingspb.ResolveRequest{
		Subject: subjectOf(testSubject),
		Name:    digestSetting,
	})
	must.Error(T, err)

	test.ErrorIs(T, err, unavailable)
	test.EqOp(T, codes.Internal, status.Code(err))
}

// TestAnotherScopesSettingsAreNotThere is the tenancy assertion, made the only
// way it can be: by asking for the same rows as a caller the extractor puts
// somewhere else.
//
// The scope is bound into every statement rather than checked in front of one,
// so a neighboring tenant's catalog is not refused, it is absent.
func TestAnotherScopesSettingsAreNotThere(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)
	h.seedValue(T, testScope, testSubject, digestSetting, "daily")

	// The neighbor's own authorizer question passes — they are asking about
	// themselves — and the setting still is not there.
	otherSelf := settings.Subject{Type: settings.SubjectUser, ID: otherUser}

	_, err := h.server.Resolve(h.otherCtx(T), &settingspb.ResolveRequest{
		Subject: subjectOf(otherSelf),
		Name:    digestSetting,
	})
	must.Error(T, err)
	test.ErrorIs(T, err, settings.ErrDefinitionNotFound)
	test.EqOp(T, codes.NotFound, status.Code(err))

	// And resolving everything answers with an empty catalog rather than with
	// somebody else's.
	response, err := h.server.ResolveAll(h.otherCtx(T), &settingspb.ResolveAllRequest{
		Subject: subjectOf(otherSelf),
	})
	must.NoError(T, err)
	test.SliceEmpty(T, response.GetResolutions())
}

// TestAnAnonymousRequestIsUnauthenticated: every RPC here needs a principal,
// because a read with no principal has no scope to filter on.
func TestAnAnonymousRequestIsUnauthenticated(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)

	_, err := h.server.Resolve(T.Context(), &settingspb.ResolveRequest{
		Subject: subjectOf(testSubject),
		Name:    digestSetting,
	})
	must.Error(T, err)

	test.ErrorIs(T, err, settingsgrpc.ErrNoPrincipal)
	test.EqOp(T, codes.Unauthenticated, status.Code(err))
}
