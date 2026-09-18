package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The suite's stand-in for the half of a consumer's wiring that says what the
// caller may do, and the three reads' answer to a client that asked for the
// settings somebody retired and the answers somebody cleared.
//
// The extractor is what a deployment hands both this surface and
// primitives-go's authorization/grpc enforcer. It is not the enforcer: nothing
// in these tests checks whether the method may be called at all, because that
// check is an interceptor's and runs before the handler. What is under test is
// the second question — which rows the answer may contain — and it is asked
// inside the handler, where the filter is.
//
// This service retires two nouns under two grants, so every case below names
// which one it is about.

// grantsKey is where this suite puts the caller's authority.
type grantsKey struct{}

// withGrants narrows a request context to exactly the permissions named, which
// is how a test describes a caller who may read and may not retire.
func withGrants(ctx context.Context, perms ...authorization.Permission) context.Context {
	return context.WithValue(ctx, grantsKey{}, authorization.NewGrants(authorization.NewPermissionSet(perms...)))
}

// extractGrants is the authorization.GrantsExtractor every harness here is built
// with.
//
// A context nothing narrowed carries every grant, because the rest of this suite
// is about what a handler does and not about what a policy allows — a default of
// "nothing" would make every existing test a test of this file. A test that cares
// says so with [withGrants].
func extractGrants(ctx context.Context) (authorization.Grants, bool) {
	grants, ok := ctx.Value(grantsKey{}).(authorization.Grants)
	if !ok {
		return authorization.AllowAll(), true
	}

	return grants, true
}

// includeArchived is the filter a client sets to ask for the retired rows.
func includeArchived() *filteringpb.QueryFilter {
	include := true

	return &filteringpb.QueryFilter{IncludeArchived: &include}
}

// archiveDefinition retires a seeded setting directly through the store, so the
// row a test is about was withdrawn without going through the surface the test
// is about.
func (h *harness) archiveDefinition(tb testing.TB, scope tenancy.Scope, definitionID string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		return h.store.ArchiveDefinition(tb.Context(), tx, scope, definitionID)
	}))
}

// clearValue takes a seeded answer back the same way.
func (h *harness) clearValue(tb testing.TB, scope tenancy.Scope, subject settings.Subject, name string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.ClearValue(tb.Context(), tx, scope, subject, name)

		return err
	}))
}

// definitionNames and valueNames are what every assertion below compares,
// because which rows came back is the whole of the question.
func definitionNames(results []*settingspb.SettingDefinition) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.GetName())
	}

	return out
}

func valueIDs(results []*settingspb.SettingValue) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.GetId())
	}

	return out
}

// TestIncludeArchivedIsAGrantAndNotAField is the ruling, executed, on all three
// paged reads.
func TestIncludeArchivedIsAGrantAndNotAField(T *testing.T) {
	T.Parallel()

	T.Run("ListDefinitions hides the retired settings from a caller who cannot archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)
		live := h.seed(t, testScope, &settings.Definition{Name: compactSetting, Kind: settings.KindBool})
		retired := h.seed(t, testScope, &settings.Definition{Name: channelSetting, Kind: settings.KindString})
		h.archiveDefinition(t, testScope, retired.ID)

		res, err := h.server.ListDefinitions(
			withGrants(h.ctx(t), settingsgrpc.PermissionReadDefinitions),
			&settingspb.ListDefinitionsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		test.Eq(t, []string{live.Name}, definitionNames(res.GetResults()))
	})

	T.Run("ListDefinitions answers the retired settings to a caller who can archive", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)
		live := h.seed(t, testScope, &settings.Definition{Name: compactSetting, Kind: settings.KindBool})
		retired := h.seed(t, testScope, &settings.Definition{Name: channelSetting, Kind: settings.KindString})
		h.archiveDefinition(t, testScope, retired.ID)

		res, err := h.server.ListDefinitions(
			withGrants(h.ctx(t),
				settingsgrpc.PermissionReadDefinitions, settingsgrpc.PermissionArchiveDefinitions),
			&settingspb.ListDefinitionsRequest{Filter: includeArchived()})
		must.NoError(t, err)

		got := definitionNames(res.GetResults())
		test.SliceContains(t, got, live.Name)
		test.SliceContains(t, got, retired.Name)
	})

	// A value is not retired in this package's vocabulary, it is cleared — so
	// the grant is the one that clears it.
	T.Run("ListValuesForSubject hides the cleared answers from a caller who cannot write", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		h.seedValue(t, testScope, testSubject, digestSetting, "daily")
		h.seedValue(t, testScope, testSubject, channelSetting, "email")
		h.clearValue(t, testScope, testSubject, channelSetting)

		res, err := h.server.ListValuesForSubject(
			withGrants(h.ctx(t), settingsgrpc.PermissionReadValues),
			&settingspb.ListValuesForSubjectRequest{
				Subject: subjectOf(testSubject),
				Filter:  includeArchived(),
			})
		must.NoError(t, err)

		test.SliceLen(t, 1, res.GetResults())
	})

	T.Run("ListValuesForSubject answers the cleared answers to a caller who can write", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		h.seedValue(t, testScope, testSubject, digestSetting, "daily")
		h.seedValue(t, testScope, testSubject, channelSetting, "email")
		h.clearValue(t, testScope, testSubject, channelSetting)

		res, err := h.server.ListValuesForSubject(
			withGrants(h.ctx(t), settingsgrpc.PermissionReadValues, settingsgrpc.PermissionWriteValues),
			&settingspb.ListValuesForSubjectRequest{
				Subject: subjectOf(testSubject),
				Filter:  includeArchived(),
			})
		must.NoError(t, err)

		test.SliceLen(t, 2, res.GetResults())
	})

	T.Run("ListValuesForDefinition hides the cleared answers from a caller who cannot write", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		h.seedValue(t, testScope, testSubject, digestSetting, "daily")
		h.seedValue(t, testScope, accountSubject, digestSetting, "never")
		h.clearValue(t, testScope, accountSubject, digestSetting)

		res, err := h.server.ListValuesForDefinition(
			withGrants(h.ctx(t), settingsgrpc.PermissionReadAllValues),
			&settingspb.ListValuesForDefinitionRequest{
				Name:   digestSetting,
				Filter: includeArchived(),
			})
		must.NoError(t, err)

		test.SliceLen(t, 1, valueIDs(res.GetResults()))
	})

	T.Run("ListValuesForDefinition answers the cleared answers to a caller who can write", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		h.seedValue(t, testScope, testSubject, digestSetting, "daily")
		h.seedValue(t, testScope, accountSubject, digestSetting, "never")
		h.clearValue(t, testScope, accountSubject, digestSetting)

		res, err := h.server.ListValuesForDefinition(
			withGrants(h.ctx(t), settingsgrpc.PermissionReadAllValues, settingsgrpc.PermissionWriteValues),
			&settingspb.ListValuesForDefinitionRequest{
				Name:   digestSetting,
				Filter: includeArchived(),
			})
		must.NoError(t, err)

		test.SliceLen(t, 2, valueIDs(res.GetResults()))
	})

	// The two grants are separate, which is what makes them two: holding the one
	// that retires a setting buys nothing on a page of values.
	T.Run("the definition grant does not reach the cleared answers", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)
		h.seedValue(t, testScope, testSubject, digestSetting, "daily")
		h.seedValue(t, testScope, testSubject, channelSetting, "email")
		h.clearValue(t, testScope, testSubject, channelSetting)

		res, err := h.server.ListValuesForSubject(
			withGrants(h.ctx(t),
				settingsgrpc.PermissionReadValues, settingsgrpc.PermissionArchiveDefinitions),
			&settingspb.ListValuesForSubjectRequest{
				Subject: subjectOf(testSubject),
				Filter:  includeArchived(),
			})
		must.NoError(t, err)

		test.SliceLen(t, 1, res.GetResults())
	})

	// The narrowing is not a refusal, which is the half a caller notices: the
	// read succeeds and answers with what they were entitled to ask for.
	T.Run("clearing the field is not an error", func(t *testing.T) {
		t.Parallel()

		h := newSeededHarness(t)

		_, err := h.server.ListDefinitions(
			withGrants(h.ctx(t), settingsgrpc.PermissionReadDefinitions),
			&settingspb.ListDefinitionsRequest{Filter: includeArchived()})

		must.NoError(t, err)
	})

	// The fail-closed default. A consumer who wired no extractor cannot be told
	// apart from one whose caller holds nothing, so the surface answers the same
	// way rather than guessing.
	T.Run("a server built with no grants extractor clears the field", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil, settingsgrpc.WithGrantsExtractor(nil))
		live := h.seed(t, testScope, &settings.Definition{Name: compactSetting, Kind: settings.KindBool})
		retired := h.seed(t, testScope, &settings.Definition{Name: channelSetting, Kind: settings.KindString})
		h.archiveDefinition(t, testScope, retired.ID)

		res, err := h.server.ListDefinitions(h.ctx(t), &settingspb.ListDefinitionsRequest{
			Filter: includeArchived(),
		})
		must.NoError(t, err)

		test.Eq(t, []string{live.Name}, definitionNames(res.GetResults()))
	})

	// An extractor that reports no authority is the interceptor's "no grants
	// could be determined", and it is a denial everywhere else in the stack.
	T.Run("an extractor reporting no authority clears the field", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil, settingsgrpc.WithGrantsExtractor(
			func(context.Context) (authorization.Grants, bool) { return authorization.AllowAll(), false }))

		live := h.seed(t, testScope, &settings.Definition{Name: compactSetting, Kind: settings.KindBool})
		retired := h.seed(t, testScope, &settings.Definition{Name: channelSetting, Kind: settings.KindString})
		h.archiveDefinition(t, testScope, retired.ID)

		res, err := h.server.ListDefinitions(h.ctx(t), &settingspb.ListDefinitionsRequest{
			Filter: includeArchived(),
		})
		must.NoError(t, err)

		test.Eq(t, []string{live.Name}, definitionNames(res.GetResults()))
	})

	// A filter that never asked is left alone, which is what stops the
	// confinement from being a rewrite of everybody's page.
	T.Run("a filter that did not ask is untouched", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, nil)
		live := h.seed(t, testScope, &settings.Definition{Name: compactSetting, Kind: settings.KindBool})
		retired := h.seed(t, testScope, &settings.Definition{Name: channelSetting, Kind: settings.KindString})
		h.archiveDefinition(t, testScope, retired.ID)

		res, err := h.server.ListDefinitions(
			withGrants(h.ctx(t),
				settingsgrpc.PermissionReadDefinitions, settingsgrpc.PermissionArchiveDefinitions),
			&settingspb.ListDefinitionsRequest{})
		must.NoError(t, err)

		test.Eq(t, []string{live.Name}, definitionNames(res.GetResults()))
	})
}
