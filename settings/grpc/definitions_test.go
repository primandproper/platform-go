package grpc_test

import (
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

// TestCreateDefinitionStoresTheCatalogRow: the store hands back the row as
// stored, so this RPC has nothing to read back — which is the difference
// between it and the update below.
func TestCreateDefinitionStoresTheCatalogRow(T *testing.T) {
	T.Parallel()

	h := newHarness(T, nil)

	response, err := h.server.CreateDefinition(h.ctx(T), &settingspb.CreateDefinitionRequest{
		Definition: &settingspb.SettingDefinitionInput{
			Name:         digestSetting,
			Description:  "how often we mail you",
			Kind:         settingspb.SettingKind_SETTING_KIND_STRING,
			DefaultValue: pointer.To("weekly"),
			Enumeration:  []string{"weekly", "daily", "never"},
			AdminOnly:    false,
		},
	})
	must.NoError(T, err)

	definition := response.GetResult()
	must.NotNil(T, definition)

	test.NotEq(T, "", definition.GetId(), test.Sprint("the row was stored without an identifier"))
	test.EqOp(T, digestSetting, definition.GetName())
	test.EqOp(T, settingspb.SettingKind_SETTING_KIND_STRING, definition.GetKind())
	test.EqOp(T, "weekly", definition.GetDefaultValue())

	// Sorted, which is the store's reading of an enumeration as a set rather
	// than a sequence arriving unchanged.
	test.Eq(T, []string{"daily", "never", "weekly"}, definition.GetEnumeration())

	// The database's clock, read back by the write. A response carrying the
	// epoch here is a console rendering "defined in 1970".
	must.NotNil(T, definition.GetCreatedAt())
	test.True(T, definition.GetCreatedAt().AsTime().Unix() > 0)

	// Nobody has edited it and nobody has retired it, and both stay unset
	// rather than becoming the zero timestamp.
	test.Nil(T, definition.GetLastUpdatedAt())
	test.Nil(T, definition.GetArchivedAt())
}

// TestADefaultKeepsItsPresence is the doctrine this package is built on,
// checked on the wire: a text setting defaulting to "" answers every subject
// who has not chosen, and one with no default answers none of them.
func TestADefaultKeepsItsPresence(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		sent *string
	}{
		"no default at all":    {sent: nil},
		"a default of no text": {sent: pointer.To("")},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t, nil)

			response, err := h.server.CreateDefinition(h.ctx(t), &settingspb.CreateDefinitionRequest{
				Definition: &settingspb.SettingDefinitionInput{
					Name:         channelSetting,
					Kind:         settingspb.SettingKind_SETTING_KIND_STRING,
					DefaultValue: tc.sent,
				},
			})
			must.NoError(t, err)

			definition := response.GetResult()
			must.NotNil(t, definition)

			if tc.sent == nil {
				test.Nil(t, definition.DefaultValue,
					test.Sprint("a setting with no default came back with one"))

				return
			}

			must.NotNil(t, definition.DefaultValue,
				must.Sprint("a default of the empty string came back as no default"))
			test.EqOp(t, "", definition.GetDefaultValue())

			// And it answers: a subject who has not chosen resolves to it,
			// which is the whole of what the difference is for.
			resolved, err := h.server.Resolve(h.ctx(t), &settingspb.ResolveRequest{
				Subject: subjectOf(testSubject),
				Name:    channelSetting,
			})
			must.NoError(t, err)
			test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, resolved.GetResolution().GetSource())
		})
	}
}

// TestCreateDefinitionRefusesTheMalformedRequests, each in the place the answer
// belongs: a request that named no definition is this transport's refusal, and
// a kind nothing implements is the store's.
func TestCreateDefinitionRefusesTheMalformedRequests(T *testing.T) {
	T.Parallel()

	T.Run("no definition at all", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.server.CreateDefinition(h.ctx(t), &settingspb.CreateDefinitionRequest{})
		must.Error(t, err)

		test.ErrorIs(t, err, settingsgrpc.ErrNilDefinitionInput)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("an unspecified kind", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)

		_, err := h.server.CreateDefinition(h.ctx(t), &settingspb.CreateDefinitionRequest{
			Definition: &settingspb.SettingDefinitionInput{Name: digestSetting},
		})
		must.Error(t, err)

		// The converter reads the unspecified case as the empty kind rather
		// than guessing one of four, and the store is what refuses it — which
		// is where that refusal already lived.
		test.ErrorIs(t, err, settings.ErrUnknownKind)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("a name already defined", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)

		_, err := h.server.CreateDefinition(h.ctx(t), &settingspb.CreateDefinitionRequest{
			Definition: &settingspb.SettingDefinitionInput{
				Name: digestSetting,
				Kind: settingspb.SettingKind_SETTING_KIND_STRING,
			},
		})
		must.Error(t, err)

		test.ErrorIs(t, err, settings.ErrDefinitionNameTaken)
		test.EqOp(t, codes.AlreadyExists, status.Code(err))
	})
}

// TestTheTwoReadsFindTheSameRow: a console holds an id and application code
// holds a name, and both are on the surface because neither should have to look
// the other up first.
func TestTheTwoReadsFindTheSameRow(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)

	byName, err := h.server.GetDefinitionByName(h.ctx(T), &settingspb.GetDefinitionByNameRequest{
		Name: digestSetting,
	})
	must.NoError(T, err)
	must.NotNil(T, byName.GetResult())

	byID, err := h.server.GetDefinition(h.ctx(T), &settingspb.GetDefinitionRequest{
		DefinitionId: byName.GetResult().GetId(),
	})
	must.NoError(T, err)

	test.EqOp(T, byName.GetResult().GetId(), byID.GetResult().GetId())
	test.EqOp(T, digestSetting, byID.GetResult().GetName())
}

// TestListDefinitionsPagesTheCallersCatalogAlone is the tenancy assertion for
// the catalog half.
func TestListDefinitionsPagesTheCallersCatalogAlone(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)
	h.seed(T, otherScope, &settings.Definition{Name: "somebody.elses", Kind: settings.KindBool})

	response, err := h.server.ListDefinitions(h.ctx(T), &settingspb.ListDefinitionsRequest{})
	must.NoError(T, err)

	test.SliceLen(T, 5, response.GetResults())
	test.NotNil(T, response.GetPagination())

	for _, definition := range response.GetResults() {
		test.NotEq(T, "somebody.elses", definition.GetName())
	}

	// And the neighbor sees theirs and nothing of the caller's.
	theirs, err := h.server.ListDefinitions(h.otherCtx(T), &settingspb.ListDefinitionsRequest{})
	must.NoError(T, err)
	test.SliceLen(T, 1, theirs.GetResults())
}

// TestUpdateDefinitionAnswersWithTheRowItWrote is the read-back this RPC owes.
//
// A response assembled from the request would carry the epoch where
// last_updated_at belongs. What answers it is the store's own return, read on
// the transaction that wrote — which is what the store's reads taking an
// executor rather than a reader is for.
func TestUpdateDefinitionAnswersWithTheRowItWrote(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)

	existing, err := h.server.GetDefinitionByName(h.ctx(T), &settingspb.GetDefinitionByNameRequest{
		Name: digestSetting,
	})
	must.NoError(T, err)

	response, err := h.server.UpdateDefinition(h.ctx(T), &settingspb.UpdateDefinitionRequest{
		DefinitionId: existing.GetResult().GetId(),
		Definition: &settingspb.SettingDefinitionInput{
			Name:         digestSetting,
			Description:  "how often we mail you, revised",
			Kind:         settingspb.SettingKind_SETTING_KIND_STRING,
			DefaultValue: pointer.To("never"),
			Enumeration:  []string{"daily", "never", "weekly"},
		},
	})
	must.NoError(T, err)

	updated := response.GetResult()
	must.NotNil(T, updated)
	test.EqOp(T, "how often we mail you, revised", updated.GetDescription())
	test.EqOp(T, "never", updated.GetDefaultValue())

	must.NotNil(T, updated.GetLastUpdatedAt(), must.Sprint("the edit answered with no edit time"))
	test.True(T, updated.GetLastUpdatedAt().AsTime().Unix() > 0)
}

// TestUpdateDefinitionRefusesAnEditThatWouldStrandValues is the rule this store
// exists to own, reaching a client as something they can act on rather than as
// a 500.
//
// The message names the subject and the value, because clearing or migrating
// them is what the administrator has to do before the edit can succeed — which
// is why the sentinel is client-safe and why the code is FailedPrecondition
// rather than InvalidArgument: the request is well formed and it is the stored
// values that refuse it.
func TestUpdateDefinitionRefusesAnEditThatWouldStrandValues(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)
	h.seedValue(T, testScope, testSubject, digestSetting, "daily")

	existing, err := h.server.GetDefinitionByName(h.ctx(T), &settingspb.GetDefinitionByNameRequest{
		Name: digestSetting,
	})
	must.NoError(T, err)

	_, err = h.server.UpdateDefinition(h.ctx(T), &settingspb.UpdateDefinitionRequest{
		DefinitionId: existing.GetResult().GetId(),
		Definition: &settingspb.SettingDefinitionInput{
			Name:        digestSetting,
			Kind:        settingspb.SettingKind_SETTING_KIND_STRING,
			Enumeration: []string{"never", "weekly"},
		},
	})
	must.Error(T, err)

	test.ErrorIs(T, err, settings.ErrStrandedValues)
	test.EqOp(T, codes.FailedPrecondition, status.Code(err))

	// The subject's value is still what it was: the edit was refused rather
	// than applied over rows it would have broken.
	value, err := h.server.GetValue(h.ctx(T), &settingspb.GetValueRequest{
		Subject: subjectOf(testSubject),
		Name:    digestSetting,
	})
	must.NoError(T, err)
	test.EqOp(T, "daily", value.GetResult().GetRaw())
}

// TestArchiveDefinitionRetiresTheSettingAndKeepsTheValues is the store's
// decision arriving on the wire: archiving is not erasure.
func TestArchiveDefinitionRetiresTheSettingAndKeepsTheValues(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)
	h.seedValue(T, testScope, testSubject, digestSetting, "daily")

	existing, err := h.server.GetDefinitionByName(h.ctx(T), &settingspb.GetDefinitionByNameRequest{
		Name: digestSetting,
	})
	must.NoError(T, err)

	_, err = h.server.ArchiveDefinition(h.ctx(T), &settingspb.ArchiveDefinitionRequest{
		DefinitionId: existing.GetResult().GetId(),
	})
	must.NoError(T, err)

	// Gone from every read that does not ask for archived rows, and this
	// surface has no read that does.
	_, err = h.server.GetDefinitionByName(h.ctx(T), &settingspb.GetDefinitionByNameRequest{
		Name: digestSetting,
	})
	test.ErrorIs(T, err, settings.ErrDefinitionNotFound)
	test.EqOp(T, codes.NotFound, status.Code(err))

	// And the name stays claimed, because freeing it would let a second
	// definition inherit the rows written for the first.
	_, err = h.server.CreateDefinition(h.ctx(T), &settingspb.CreateDefinitionRequest{
		Definition: &settingspb.SettingDefinitionInput{
			Name: digestSetting,
			Kind: settingspb.SettingKind_SETTING_KIND_STRING,
		},
	})
	test.ErrorIs(T, err, settings.ErrDefinitionNameTaken)
}

// TestListValuesForDefinitionIsTheOperatorRead is the one value-side RPC no
// SubjectAuthorizer gates, and this is why that is a decision rather than an
// oversight: it names a setting rather than a subject, and what it answers is
// every subject's row.
//
// The caller here is the one the harness's authorizer refuses every other
// subject to, and they read a stranger's value through this method — behind
// PermissionReadAllValues, which is a grant a consumer hands to whoever
// administers the catalog and not to the person on a preferences page.
func TestListValuesForDefinitionIsTheOperatorRead(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)
	h.seedValue(T, testScope, testSubject, digestSetting, "daily")
	h.seedValue(T, testScope, strangeSubject, digestSetting, "never")

	response, err := h.server.ListValuesForDefinition(h.ctx(T),
		&settingspb.ListValuesForDefinitionRequest{Name: digestSetting})
	must.NoError(T, err)

	test.SliceLen(T, 2, response.GetResults())

	subjects := map[string]string{}
	for _, value := range response.GetResults() {
		subjects[value.GetSubject().GetId()] = value.GetRaw()
	}

	test.EqOp(T, "daily", subjects[testSubject.ID])
	test.EqOp(T, "never", subjects[strangeSubject.ID])
}

// TestTheCatalogRPCsAreScopedToTheCaller keeps the four reads and the two
// id-keyed writes from reaching a neighboring tenant's row, and does it by
// asking for a real identifier from the wrong connection.
//
// A definition in another scope is not refused, it is absent — the scope is
// bound into the statement rather than checked in front of it.
func TestTheCatalogRPCsAreScopedToTheCaller(T *testing.T) {
	T.Parallel()

	h := newSeededHarness(T)

	existing, err := h.server.GetDefinitionByName(h.ctx(T), &settingspb.GetDefinitionByNameRequest{
		Name: digestSetting,
	})
	must.NoError(T, err)

	id := existing.GetResult().GetId()

	_, err = h.server.GetDefinition(h.otherCtx(T), &settingspb.GetDefinitionRequest{DefinitionId: id})
	test.ErrorIs(T, err, settings.ErrDefinitionNotFound)

	_, err = h.server.UpdateDefinition(h.otherCtx(T), &settingspb.UpdateDefinitionRequest{
		DefinitionId: id,
		Definition: &settingspb.SettingDefinitionInput{
			Name: digestSetting,
			Kind: settingspb.SettingKind_SETTING_KIND_STRING,
		},
	})
	test.ErrorIs(T, err, settings.ErrDefinitionNotFound)

	_, err = h.server.ListValuesForDefinition(h.otherCtx(T),
		&settingspb.ListValuesForDefinitionRequest{Name: digestSetting})
	test.ErrorIs(T, err, settings.ErrDefinitionNotFound)
}
