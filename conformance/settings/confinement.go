package settings

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func confinement(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("a catalog listing pages the caller's tenant only", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
		ours, neighbor := names(), names()

		myOperator, theirOperator := operator(t, s, mine), operator(t, s, theirs)
		define(t, myOperator, &settingspb.SettingDefinitionInput{Name: ours.compact, Kind: settingspb.SettingKind_SETTING_KIND_BOOLEAN})
		define(t, theirOperator, &settingspb.SettingDefinitionInput{Name: neighbor.compact, Kind: settingspb.SettingKind_SETTING_KIND_BOOLEAN})

		page, err := myOperator.Surfaces.Settings.ListDefinitions(myOperator.Context(t.Context()),
			&settingspb.ListDefinitionsRequest{})
		must.NoError(t, err)

		// Presence and absence of two named settings, never a count: this
		// listing may run against a database the suite does not own.
		got := definitionNames(page.GetResults())
		test.SliceContains(t, got, ours.compact,
			test.Sprint("this tenant's own setting was missing from its catalog"))
		test.SliceNotContains(t, got, neighbor.compact,
			test.Sprint("a neighboring tenant's setting reached this catalog"))
		test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

		// And the mirror image, which rules out a rule that favors whichever
		// caller was made first.
		page, err = theirOperator.Surfaces.Settings.ListDefinitions(theirOperator.Context(t.Context()),
			&settingspb.ListDefinitionsRequest{})
		must.NoError(t, err)
		test.SliceContains(t, definitionNames(page.GetResults()), neighbor.compact)
		test.SliceNotContains(t, definitionNames(page.GetResults()), ours.compact)
	})

	// Asked with a real identifier from the wrong tenant. The scope is bound
	// into the statement rather than checked in front of it, so a neighbor's
	// definition is not refused, it is absent — which is also the answer that
	// is not an oracle for which identifiers exist.
	t.Run("a neighbor's definition is absent to every catalog call that names it", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
		c := names()

		myOperator, theirOperator := operator(t, s, mine), operator(t, s, theirs)
		define(t, myOperator, &settingspb.SettingDefinitionInput{Name: c.digest, Kind: settingspb.SettingKind_SETTING_KIND_STRING})
		id := byName(t, myOperator, c.digest).GetId()

		// The positive control. Every absence below is also what a catalog
		// reaching nothing at all would answer.
		found, err := myOperator.Surfaces.Settings.GetDefinition(myOperator.Context(t.Context()),
			&settingspb.GetDefinitionRequest{DefinitionId: id})
		must.NoError(t, err, must.Sprint("this tenant cannot read its own definition; the absences below prove nothing"))
		must.EqOp(t, id, found.GetResult().GetId())

		ctx := theirOperator.Context(t.Context())

		_, err = theirOperator.Surfaces.Settings.GetDefinition(ctx, &settingspb.GetDefinitionRequest{DefinitionId: id})
		must.Error(t, err, must.Sprint("a neighboring tenant's definition was readable by id"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = theirOperator.Surfaces.Settings.GetDefinitionByName(ctx, &settingspb.GetDefinitionByNameRequest{Name: c.digest})
		must.Error(t, err, must.Sprint("a neighboring tenant's definition was readable by name"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = theirOperator.Surfaces.Settings.UpdateDefinition(ctx, &settingspb.UpdateDefinitionRequest{
			DefinitionId: id,
			Definition: &settingspb.SettingDefinitionInput{
				Name:        c.digest,
				Description: "rewritten from next door",
				Kind:        settingspb.SettingKind_SETTING_KIND_STRING,
			},
		})
		must.Error(t, err, must.Sprint("a neighboring tenant's definition was rewritable by id"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = theirOperator.Surfaces.Settings.ListValuesForDefinition(ctx,
			&settingspb.ListValuesForDefinitionRequest{Name: c.digest})
		must.Error(t, err, must.Sprint("a neighboring tenant's definition answered its values"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		// And the refused rewrite changed nothing.
		test.EqOp(t, "", byName(t, myOperator, c.digest).GetDescription())
	})

	// The neighbor's own authorizer question passes — they are asking about
	// themselves — and the setting still is not there, because it is not in
	// their tenant.
	t.Run("a neighbor resolving the caller's setting finds no such setting", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
		needsUser(t, mine)
		needsUser(t, theirs)

		c := names()
		defineCatalog(t, operator(t, s, mine), &c)
		set(t, mine, c.digest, stringValue(optionDaily))

		// The positive control: the caller reaches its own setting and its
		// own answer through the same client.
		chose, err := mine.Surfaces.Settings.Resolve(mine.Context(t.Context()),
			&settingspb.ResolveRequest{Subject: self(mine), Name: c.digest})
		must.NoError(t, err, must.Sprint("this caller cannot resolve its own setting; the absence below proves nothing"))
		must.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, chose.GetResolution().GetSource())

		_, err = theirs.Surfaces.Settings.Resolve(theirs.Context(t.Context()),
			&settingspb.ResolveRequest{Subject: self(theirs), Name: c.digest})
		must.Error(t, err, must.Sprint("a neighboring tenant's setting resolved"))
		test.EqOp(t, codes.NotFound, status.Code(err))

		all, err := theirs.Surfaces.Settings.ResolveAll(theirs.Context(t.Context()),
			&settingspb.ResolveAllRequest{Subject: self(theirs)})
		must.NoError(t, err)

		for _, resolution := range all.GetResolutions() {
			test.NotEq(t, c.digest, resolution.GetDefinition().GetName(),
				test.Sprint("a neighboring tenant's catalog reached this settings page"))
		}
	})

	// The half of authorization a grant on the method cannot reach. The
	// question these six ask is not whether the caller may call the method but
	// whose settings they named, and a colleague in the same tenant is
	// somebody else — the scope is shared, so this is the authorizer's refusal
	// and nobody else's.
	t.Run("every value call naming somebody else's settings is refused", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s)
		other := colleague(t, s, caller)
		set(t, other, c.digest, stringValue(optionNever))

		for _, gated := range subjectCalls(c.digest) {
			rpc, call := gated.rpc, gated.call

			// The positive control, per RPC: the caller makes the same call on
			// its own settings. Without it, a surface refusing everybody would
			// pass every refusal below.
			must.NoError(t, call(t, caller, self(caller)),
				must.Sprintf("%s refused the caller its own settings; the refusal below proves nothing", rpc))

			err := call(t, caller, self(other))
			must.Error(t, err, must.Sprintf("%s reached a colleague's settings", rpc))
			test.EqOp(t, codes.PermissionDenied, status.Code(err), test.Sprintf("%s answered with the wrong refusal", rpc))
		}

		// And the colleague's answer is still theirs: the refused write and
		// clear touched nothing.
		value, err := other.Surfaces.Settings.GetValue(other.Context(t.Context()),
			&settingspb.GetValueRequest{Subject: self(other), Name: c.digest})
		must.NoError(t, err)
		test.EqOp(t, optionNever, value.GetResult().GetRaw())
	})

	// The refusal is decided before anything is read, so a subject nobody has
	// stored a value for is refused exactly as one who has. A caller cannot
	// learn which subjects exist by watching the codes.
	t.Run("an account the caller is not in is refused before anything is read", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
		needsUser(t, mine)
		needsAccount(t, theirs)

		c := names()
		defineCatalog(t, operator(t, s, mine), &c)

		// The positive control: this caller's own resolution is answered.
		_, err := mine.Surfaces.Settings.Resolve(mine.Context(t.Context()),
			&settingspb.ResolveRequest{Subject: self(mine), Name: c.digest})
		must.NoError(t, err)

		_, err = mine.Surfaces.Settings.Resolve(mine.Context(t.Context()), &settingspb.ResolveRequest{
			Subject: &settingspb.SettingSubject{Type: subjectAccount, Id: theirs.AccountID},
			Name:    c.digest,
		})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})
}

// gatedCall is one RPC a subject authorizer gates, made against whichever
// subject it is handed.
type gatedCall struct {
	call func(t *testing.T, caller *conformance.Subject, subject *settingspb.SettingSubject) error
	rpc  string
}

// subjectCalls are the six RPCs a subject authorizer gates.
//
// In order rather than a map, because the positive controls run against the
// caller's own settings and depend on it: the value the first call stores is
// what the reads find, and the clear comes last so nothing after it looks for
// a value it took back.
func subjectCalls(name string) []gatedCall {
	return []gatedCall{
		{rpc: "SetValue", call: func(t *testing.T, caller *conformance.Subject, subject *settingspb.SettingSubject) error {
			t.Helper()

			_, err := caller.Surfaces.Settings.SetValue(caller.Context(t.Context()),
				&settingspb.SetValueRequest{Subject: subject, Name: name, Value: stringValue(optionDaily)})

			return err
		}},
		{rpc: "GetValue", call: func(t *testing.T, caller *conformance.Subject, subject *settingspb.SettingSubject) error {
			t.Helper()

			_, err := caller.Surfaces.Settings.GetValue(caller.Context(t.Context()),
				&settingspb.GetValueRequest{Subject: subject, Name: name})

			return err
		}},
		{rpc: "ListValuesForSubject", call: func(t *testing.T, caller *conformance.Subject, subject *settingspb.SettingSubject) error {
			t.Helper()

			_, err := caller.Surfaces.Settings.ListValuesForSubject(caller.Context(t.Context()),
				&settingspb.ListValuesForSubjectRequest{Subject: subject})

			return err
		}},
		{rpc: "Resolve", call: func(t *testing.T, caller *conformance.Subject, subject *settingspb.SettingSubject) error {
			t.Helper()

			_, err := caller.Surfaces.Settings.Resolve(caller.Context(t.Context()),
				&settingspb.ResolveRequest{Subject: subject, Name: name})

			return err
		}},
		{rpc: "ResolveAll", call: func(t *testing.T, caller *conformance.Subject, subject *settingspb.SettingSubject) error {
			t.Helper()

			_, err := caller.Surfaces.Settings.ResolveAll(caller.Context(t.Context()),
				&settingspb.ResolveAllRequest{Subject: subject})

			return err
		}},
		{rpc: "ClearValue", call: func(t *testing.T, caller *conformance.Subject, subject *settingspb.SettingSubject) error {
			t.Helper()

			_, err := caller.Surfaces.Settings.ClearValue(caller.Context(t.Context()),
				&settingspb.ClearValueRequest{Subject: subject, Name: name})

			return err
		}},
	}
}
