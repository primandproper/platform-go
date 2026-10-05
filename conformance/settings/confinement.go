package settings

import (
	"testing"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/settings/settingspb"

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

		making := []string{
			createDefinition,
			listDefinitions,
		}
		myOperator := s.Subject(t, conformance.Making(making...), conformance.InTenant(surface, mine.ScopeFor(surface)))
		theirOperator := s.Subject(t, conformance.Making(making...), conformance.InTenant(surface, theirs.ScopeFor(surface)))
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

		making := []string{
			createDefinition,
			getDefinition,
			getDefinitionByName,
			listValuesForDefinition,
			updateDefinition,
		}
		myOperator := s.Subject(t, conformance.Making(making...), conformance.InTenant(surface, mine.ScopeFor(surface)))
		theirOperator := s.Subject(t, conformance.Making(making...), conformance.InTenant(surface, theirs.ScopeFor(surface)))
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

		mine, theirs := twoDirectories(t, s, conformance.Making(setValue, resolve, resolveAll))
		needsUser(t, mine)
		needsUser(t, theirs)

		c := names()
		defineCatalog(t, s.Subject(t, conformance.Making(createDefinition), conformance.InTenant(surface, mine.ScopeFor(surface))), &c)
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

	// The per-person half of the assertion above, which a catalog shared by
	// the whole deployment still owes: a colleague reads the same definition,
	// and resolving it answers for them rather than for whoever answered it
	// first. Nothing about it is a tenant wall, so it holds in a directory
	// every caller shares.
	t.Run("a colleague resolving a setting the caller answered gets their own resolution, not the caller's value", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(setValue, resolve, resolveAll))
		needsUser(t, caller)
		other := colleague(t, s, caller, conformance.Making(setValue, resolve, resolveAll))

		c := names()
		defineCatalog(t, s.Subject(t, conformance.Making(createDefinition), conformance.InTenant(surface, caller.ScopeFor(surface))), &c)
		set(t, caller, c.digest, stringValue(optionDaily))

		// The positive control: the caller's answer is stored and is what the
		// caller resolves, so the colleague's resolution below is compared
		// against a value that exists.
		chose := resolved(t, caller, c.digest)
		must.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, chose.GetSource(),
			must.Sprint("this caller's own answer did not resolve as theirs; the comparison below proves nothing"))
		must.EqOp(t, optionDaily, chose.GetTypedValue().GetStringValue())

		// The colleague has answered nothing, so they get the definition's
		// default.
		theirs := resolved(t, other, c.digest)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, theirs.GetSource(),
			test.Sprint("a colleague who answered nothing resolved somebody else's answer"))
		test.EqOp(t, optionWeekly, theirs.GetTypedValue().GetStringValue())

		all, err := other.Surfaces.Settings.ResolveAll(other.Context(t.Context()),
			&settingspb.ResolveAllRequest{Subject: self(other)})
		must.NoError(t, err)

		for _, resolution := range all.GetResolutions() {
			if resolution.GetDefinition().GetName() == c.digest {
				test.NotEqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, resolution.GetSource(),
					test.Sprint("a colleague's settings page carried the caller's answer"))
			}
		}

		// And the mirror image: the colleague answering does not move the
		// caller's.
		set(t, other, c.digest, stringValue(optionNever))

		chose = resolved(t, caller, c.digest)
		test.EqOp(t, settingspb.ValueSource_VALUE_SOURCE_SUBJECT, chose.GetSource())
		test.EqOp(t, optionDaily, chose.GetTypedValue().GetStringValue(),
			test.Sprint("a colleague's answer replaced the caller's"))
	})

	// The half of authorization a grant on the method cannot reach. The
	// question these ask is not whether the caller may call the method but
	// whose settings they named, and a colleague in the same tenant is
	// somebody else — the scope is shared, so this is the authorizer's refusal
	// and nobody else's.
	t.Run("every value call naming somebody else's settings is refused", func(t *testing.T) {
		t.Parallel()

		caller, c := seeded(t, s, conformance.Making(setValue, getValue, listValuesForSubject, resolve, resolveAll, clearValue), conformance.AsMember())
		other := colleague(t, s, caller, conformance.Making(setValue, getValue))
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

	// An account in a neighboring directory is behind the tenant wall, and the
	// wall holds for whoever the subject mints to resolve settings: this
	// module's rule refuses it before anything is read, and a deployment whose
	// operators may resolve any account reads the neighbor's directory as empty.
	t.Run("an account in a neighboring directory is not the caller's to resolve", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s, conformance.Making(resolve))
		needsUser(t, mine)
		needsAccount(t, theirs)

		c := names()
		defineCatalog(t, s.Subject(t, conformance.Making(createDefinition), conformance.InTenant(surface, mine.ScopeFor(surface))), &c)

		// The positive control: this caller's own resolution is answered.
		_, err := mine.Surfaces.Settings.Resolve(mine.Context(t.Context()),
			&settingspb.ResolveRequest{Subject: self(mine), Name: c.digest})
		must.NoError(t, err)

		_, err = mine.Surfaces.Settings.Resolve(mine.Context(t.Context()), &settingspb.ResolveRequest{
			Subject: &settingspb.SettingSubject{Type: subjectAccount, Id: theirs.AccountID},
			Name:    c.digest,
		})
		notYours(t, err, "a neighboring directory's account's settings")
	})

	// The same question inside one directory, where no wall stands between
	// the two accounts and the refusal is the deployment's SubjectAuthorizer's.
	// This module ships no default for it, and a deployment that resolves no
	// account subjects at all would refuse this too — which it says in
	// Seams.AccountSettingsUnresolved, and this skips. Everywhere else the
	// caller's own account is asked first and must be answered, which is what
	// makes the refusal about membership.
	t.Run("an account the caller holds no membership in is not the caller's to resolve", func(t *testing.T) {
		t.Parallel()

		if s.Seams().AccountSettingsUnresolved {
			conformance.Skip(t, "conformance: this subject resolves no account subjects (Seams.AccountSettingsUnresolved), "+
				"so a refused stranger's account says nothing about membership")
		}

		caller := s.Subject(t, conformance.Making(resolve), conformance.AsMember())
		needsUser(t, caller)
		needsAccount(t, caller)
		stranger := colleague(t, s, caller)
		needsAccount(t, stranger)

		c := names()
		defineCatalog(t, s.Subject(t, conformance.Making(createDefinition), conformance.InTenant(surface, caller.ScopeFor(surface))), &c)

		// The positive control: the caller's own resolution is answered.
		_, err := caller.Surfaces.Settings.Resolve(caller.Context(t.Context()),
			&settingspb.ResolveRequest{Subject: self(caller), Name: c.digest})
		must.NoError(t, err, must.Sprint("this caller cannot resolve its own setting; the refusal below proves nothing"))

		_, err = caller.Surfaces.Settings.Resolve(caller.Context(t.Context()), &settingspb.ResolveRequest{
			Subject: &settingspb.SettingSubject{Type: subjectAccount, Id: caller.AccountID},
			Name:    c.digest,
		})
		must.NoError(t, err, must.Sprint("this caller cannot resolve its own account's setting, and the subject does not "+
			"declare Seams.AccountSettingsUnresolved; the refusal below would prove nothing"))

		_, err = caller.Surfaces.Settings.Resolve(caller.Context(t.Context()), &settingspb.ResolveRequest{
			Subject: &settingspb.SettingSubject{Type: subjectAccount, Id: stranger.AccountID},
			Name:    c.digest,
		})
		notYours(t, err, "an account the caller holds no membership in")
	})
}

// gatedCall is one RPC a subject authorizer gates, made against whichever
// subject it is handed.
type gatedCall struct {
	call func(t *testing.T, caller *conformance.Subject, subject *settingspb.SettingSubject) error
	rpc  string
}

// subjectCalls are the RPCs a subject authorizer gates.
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
