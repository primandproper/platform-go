package settings

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func definitions(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a definition comes back as it was stored", func(t *testing.T) {
		t.Parallel()

		op := operator(t, s, s.Subject(t))
		c := names()

		definition := define(t, op, &settingspb.SettingDefinitionInput{
			Name:         c.digest,
			Description:  "how often we mail you",
			Kind:         settingspb.SettingKind_SETTING_KIND_STRING,
			DefaultValue: new(optionWeekly),
			Enumeration:  []string{optionWeekly, optionDaily, optionNever},
		})

		test.NotEq(t, "", definition.GetId(), test.Sprint("the row was stored without an identifier"))
		test.EqOp(t, c.digest, definition.GetName())
		test.EqOp(t, settingspb.SettingKind_SETTING_KIND_STRING, definition.GetKind())
		test.EqOp(t, optionWeekly, definition.GetDefaultValue())

		// Sorted, which is the store reading an enumeration as a set rather
		// than a sequence arriving unchanged.
		test.Eq(t, []string{optionDaily, optionNever, optionWeekly}, definition.GetEnumeration())

		// The database's clock, read back by the write. A response carrying
		// the epoch here is a console rendering "defined in 1970", and no
		// finer comparison is made because one dialect keeps whole seconds.
		must.NotNil(t, definition.GetCreatedAt())
		test.Positive(t, definition.GetCreatedAt().AsTime().Unix())

		// Nobody has edited it and nobody has retired it, and both stay unset
		// rather than becoming the zero timestamp.
		test.Nil(t, definition.GetLastUpdatedAt())
		test.Nil(t, definition.GetArchivedAt())
	})

	// The doctrine the package is built on, checked on the wire: a text
	// setting defaulting to "" answers every subject who has not chosen, and
	// one with no default answers none of them.
	t.Run("a default of no text is a default, and no default is not", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsUser(t, caller)
		op := operator(t, s, caller)
		c := names()

		none := define(t, op, &settingspb.SettingDefinitionInput{
			Name: c.channel,
			Kind: settingspb.SettingKind_SETTING_KIND_STRING,
		})
		test.Nil(t, none.DefaultValue, test.Sprint("a setting with no default came back with one"))

		empty := define(t, op, &settingspb.SettingDefinitionInput{
			Name:         c.digest,
			Kind:         settingspb.SettingKind_SETTING_KIND_STRING,
			DefaultValue: new(""),
		})
		must.NotNil(t, empty.DefaultValue, must.Sprint("a default of the empty string came back as no default"))
		test.EqOp(t, "", empty.GetDefaultValue())

		// And the difference answers: a subject who has not chosen resolves to
		// the empty default and to nothing at all for the other, which is the
		// whole of what the difference is for.
		ctx := caller.Context(t.Context())

		resolved, err := caller.Surfaces.Settings.Resolve(ctx,
			&settingspb.ResolveRequest{Subject: self(caller), Name: c.digest})
		must.NoError(t, err)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, resolved.GetResolution().GetSource())

		resolved, err = caller.Surfaces.Settings.Resolve(ctx,
			&settingspb.ResolveRequest{Subject: self(caller), Name: c.channel})
		must.NoError(t, err)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_UNSET, resolved.GetResolution().GetSource())
	})

	t.Run("a create naming no definition is refused as a bad request", func(t *testing.T) {
		t.Parallel()

		op := operator(t, s, s.Subject(t))

		_, err := op.Surfaces.Settings.CreateDefinition(op.Context(t.Context()), &settingspb.CreateDefinitionRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	// The converter reads the unspecified case as no kind rather than guessing
	// one of four, and the store is what refuses it.
	t.Run("a create naming no kind is refused as a bad request", func(t *testing.T) {
		t.Parallel()

		op := operator(t, s, s.Subject(t))

		_, err := op.Surfaces.Settings.CreateDefinition(op.Context(t.Context()), &settingspb.CreateDefinitionRequest{
			Definition: &settingspb.SettingDefinitionInput{Name: names().digest},
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("a name already defined is refused as taken, and says so", func(t *testing.T) {
		t.Parallel()

		op := operator(t, s, s.Subject(t))
		c := names()
		define(t, op, &settingspb.SettingDefinitionInput{Name: c.digest, Kind: settingspb.SettingKind_SETTING_KIND_STRING})

		_, err := op.Surfaces.Settings.CreateDefinition(op.Context(t.Context()), &settingspb.CreateDefinitionRequest{
			Definition: &settingspb.SettingDefinitionInput{Name: c.digest, Kind: settingspb.SettingKind_SETTING_KIND_BOOLEAN},
		})
		must.Error(t, err)
		test.EqOp(t, codes.AlreadyExists, status.Code(err))

		// The wording is registered as client-safe, because AlreadyExists alone
		// does not tell a taken name from a taken identifier.
		test.StrContains(t, status.Convert(err).Message(), "already defined")
	})

	// A console holds an id and application code holds a name, and both are
	// on the surface because neither should have to look the other up first.
	t.Run("a definition read by name and by id is the same row", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		op := operator(t, s, caller)

		found := byName(t, op, c.digest)

		byID, err := op.Surfaces.Settings.GetDefinition(op.Context(t.Context()),
			&settingspb.GetDefinitionRequest{DefinitionId: found.GetId()})
		must.NoError(t, err)
		test.EqOp(t, found.GetId(), byID.GetResult().GetId())
		test.EqOp(t, c.digest, byID.GetResult().GetName())
	})

	// A response assembled from the request would carry the epoch where
	// last_updated_at belongs; what answers is the store's read-back on the
	// transaction that wrote.
	t.Run("an update answers with the row it wrote", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		op := operator(t, s, caller)
		existing := byName(t, op, c.digest)

		response, err := op.Surfaces.Settings.UpdateDefinition(op.Context(t.Context()), &settingspb.UpdateDefinitionRequest{
			DefinitionId: existing.GetId(),
			Definition: &settingspb.SettingDefinitionInput{
				Name:         c.digest,
				Description:  "how often we mail you, revised",
				Kind:         settingspb.SettingKind_SETTING_KIND_STRING,
				DefaultValue: new(optionNever),
				Enumeration:  []string{optionDaily, optionNever, optionWeekly},
			},
		})
		must.NoError(t, err)

		updated := response.GetResult()
		must.NotNil(t, updated)
		test.EqOp(t, existing.GetId(), updated.GetId())
		test.EqOp(t, "how often we mail you, revised", updated.GetDescription())
		test.EqOp(t, optionNever, updated.GetDefaultValue())

		must.NotNil(t, updated.GetLastUpdatedAt(), must.Sprint("the edit answered with no edit time"))
		test.Positive(t, updated.GetLastUpdatedAt().AsTime().Unix())
	})

	// The rule the store exists to own, reaching a client as something they
	// can act on: the request is well formed and it is the stored values that
	// refuse it, so FailedPrecondition rather than InvalidArgument.
	t.Run("an edit that would strand a stored value is refused and changes nothing", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		op := operator(t, s, caller)
		set(t, caller, c.digest, stringValue(optionDaily))

		_, err := op.Surfaces.Settings.UpdateDefinition(op.Context(t.Context()), &settingspb.UpdateDefinitionRequest{
			DefinitionId: byName(t, op, c.digest).GetId(),
			Definition: &settingspb.SettingDefinitionInput{
				Name:        c.digest,
				Kind:        settingspb.SettingKind_SETTING_KIND_STRING,
				Enumeration: []string{optionNever, optionWeekly},
			},
		})
		must.Error(t, err)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))

		// Client-safe, because clearing the value it names is the task the
		// administrator has to do before the edit can land.
		test.StrContains(t, status.Convert(err).Message(), "strand")

		// The value is still what it was: the edit was refused rather than
		// applied over rows it would have broken.
		value, err := caller.Surfaces.Settings.GetValue(caller.Context(t.Context()),
			&settingspb.GetValueRequest{Subject: self(caller), Name: c.digest})
		must.NoError(t, err)
		test.EqOp(t, optionDaily, value.GetResult().GetRaw())
		test.Eq(t, []string{optionDaily, optionNever, optionWeekly}, byName(t, op, c.digest).GetEnumeration())
	})

	// Archiving is not erasure, and the name stays claimed: freeing it would
	// let a second definition inherit the rows written for the first.
	t.Run("archiving retires a setting from every read and keeps its name claimed", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		op := operator(t, s, caller)
		set(t, caller, c.digest, stringValue(optionDaily))

		ctx := op.Context(t.Context())
		retired := byName(t, op, c.digest)

		// The positive control for the listing below: the setting is there
		// before it is retired, beside one that stays.
		before, err := op.Surfaces.Settings.ListDefinitions(ctx, &settingspb.ListDefinitionsRequest{})
		must.NoError(t, err)
		must.SliceContains(t, definitionNames(before.GetResults()), c.digest)

		_, err = op.Surfaces.Settings.ArchiveDefinition(ctx,
			&settingspb.ArchiveDefinitionRequest{DefinitionId: retired.GetId()})
		must.NoError(t, err)

		_, err = op.Surfaces.Settings.GetDefinitionByName(ctx, &settingspb.GetDefinitionByNameRequest{Name: c.digest})
		must.Error(t, err, must.Sprint("a retired setting was readable by name"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = op.Surfaces.Settings.GetDefinition(ctx, &settingspb.GetDefinitionRequest{DefinitionId: retired.GetId()})
		must.Error(t, err, must.Sprint("a retired setting was readable by id"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		after, err := op.Surfaces.Settings.ListDefinitions(ctx, &settingspb.ListDefinitionsRequest{})
		must.NoError(t, err)
		test.SliceNotContains(t, definitionNames(after.GetResults()), c.digest,
			test.Sprint("a retired setting is still in a listing that did not ask for retired rows"))
		test.SliceContains(t, definitionNames(after.GetResults()), c.compact)

		_, err = op.Surfaces.Settings.CreateDefinition(ctx, &settingspb.CreateDefinitionRequest{
			Definition: &settingspb.SettingDefinitionInput{Name: c.digest, Kind: settingspb.SettingKind_SETTING_KIND_STRING},
		})
		must.Error(t, err, must.Sprint("a retired setting's name was free to define again"))
		test.EqOp(t, codes.AlreadyExists, status.Code(err))
	})

	// Whether the retired rows come back is a grant — the one that retires
	// them — and a caller who asked without it gets the live page rather than
	// an error, which is the half a client notices.
	t.Run("asking for retired settings is never refused", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		include := true

		res, err := caller.Surfaces.Settings.ListDefinitions(caller.Context(t.Context()),
			&settingspb.ListDefinitionsRequest{Filter: &filteringpb.QueryFilter{IncludeArchived: &include}})
		must.NoError(t, err)
		test.SliceContains(t, definitionNames(res.GetResults()), c.compact)
	})

	// The one value-side read no subject authorizer gates, because it names a
	// setting rather than a subject and answers every subject's row — the read
	// an administrator makes before narrowing an enumeration.
	t.Run("the values for a definition are every subject's answer", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		other := colleague(t, s, caller)
		op := operator(t, s, caller)

		set(t, caller, c.digest, stringValue(optionDaily))
		set(t, other, c.digest, stringValue(optionNever))

		response, err := op.Surfaces.Settings.ListValuesForDefinition(op.Context(t.Context()),
			&settingspb.ListValuesForDefinitionRequest{Name: c.digest})
		must.NoError(t, err)

		answers := map[string]string{}
		for _, value := range response.GetResults() {
			answers[value.GetSubject().GetId()] = value.GetRaw()
		}

		test.EqOp(t, optionDaily, answers[caller.UserID], test.Sprint("the caller's own answer was missing"))
		test.EqOp(t, optionNever, answers[other.UserID], test.Sprint("a colleague's answer was missing"))
	})
}
