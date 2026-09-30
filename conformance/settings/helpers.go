package settings

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The subject types this surface's own vocabulary names. A deployment may
// declare more; these two are the ones settings ships.
// The three options the enumerated setting's catalog declares, weekly being
// its default.
const (
	optionDaily  = "daily"
	optionNever  = "never"
	optionWeekly = "weekly"
)

const (
	subjectUser    = "user"
	subjectAccount = "account"
)

// maxPages bounds a walk through a catalog this suite does not own. A
// deployment may serve settings from one tenant every caller shares, so the
// definition an assertion is looking for may not be on the first page — and a
// walk with no bound is a test that never ends against a cursor that never
// does.
const maxPages = 100

// catalog is one setting of each shape these assertions need, named for the
// test that defines them.
//
// The names are minted rather than fixed because a deployment declares its own
// catalog at boot, and a suite asserting against "notifications.digest" in a
// consumer's tenant would be asserting against their setting rather than one it
// made. The five are deliberately not uniform, as the surface's own suite has
// them: the string setting enumerates its values and has a default, the
// boolean has a default, the integer has none — the third resolution case — the
// float has a default outside any enumeration, and the second string setting
// has neither, which is the only way to tell "chose nothing" from "chose the
// empty string".
type catalog struct {
	digest    string
	compact   string
	retention string
	ratio     string
	channel   string
}

func names() catalog {
	suffix := identifiers.New()

	return catalog{
		digest:    "conformance.digest." + suffix,
		compact:   "conformance.compact." + suffix,
		retention: "conformance.retention." + suffix,
		ratio:     "conformance.ratio." + suffix,
		channel:   "conformance.channel." + suffix,
	}
}

// define adds a setting to op's catalog through the surface.
//
// Defining a setting is a deployment's decision in the sense a database column
// is, so op is minted naming CreateDefinition: an administrator where the
// subject reserves it, and an ordinary caller where it does not. That makes the
// reservation the deployment's declaration, and the only one: a deployment
// that refuses the ordinary caller a grant inside the handler without having
// named the call in Seams.OperatorMethods has contradicted what it declared,
// and fails here saying so rather than skipping — a skip decided by the
// server's answer is one a deployment refusing every definition would pass.
func define(t *testing.T, op *conformance.Subject, input *settingspb.SettingDefinitionInput) *settingspb.SettingDefinition {
	t.Helper()

	response, err := op.Surfaces.Settings.CreateDefinition(op.Context(t.Context()),
		&settingspb.CreateDefinitionRequest{Definition: input})
	if status.Code(err) == codes.PermissionDenied {
		t.Fatalf("conformance: defining setting %q was refused (%v) to a caller minted for it; "+
			"a deployment that keeps CreateDefinition from its members names it in Seams.OperatorMethods", input.GetName(), err)
	}

	must.NoError(t, err, must.Sprintf("defining setting %q", input.GetName()))
	must.NotNil(t, response.GetResult(), must.Sprint("a definition was stored and none came back"))

	return response.GetResult()
}

// defineCatalog defines all five of c's settings in op's tenant.
func defineCatalog(t *testing.T, op *conformance.Subject, c *catalog) {
	t.Helper()

	define(t, op, &settingspb.SettingDefinitionInput{
		Name:         c.digest,
		Kind:         settingspb.SettingKind_SETTING_KIND_STRING,
		DefaultValue: new(optionWeekly),
		Enumeration:  []string{optionDaily, optionNever, optionWeekly},
	})
	define(t, op, &settingspb.SettingDefinitionInput{
		Name:         c.compact,
		Kind:         settingspb.SettingKind_SETTING_KIND_BOOLEAN,
		DefaultValue: new("false"),
	})
	define(t, op, &settingspb.SettingDefinitionInput{
		Name: c.retention,
		Kind: settingspb.SettingKind_SETTING_KIND_INTEGER,
	})
	define(t, op, &settingspb.SettingDefinitionInput{
		Name:         c.ratio,
		Kind:         settingspb.SettingKind_SETTING_KIND_FLOAT,
		DefaultValue: new("1.5"),
	})
	define(t, op, &settingspb.SettingDefinitionInput{
		Name: c.channel,
		Kind: settingspb.SettingKind_SETTING_KIND_STRING,
	})
}

// seeded is a fresh caller with the five settings defined in its tenant, which
// is where most of these assertions start. The caller is minted with opts.
func seeded(t *testing.T, s *conformance.Session, opts ...conformance.SubjectOption) (*conformance.Subject, catalog) {
	t.Helper()

	caller := s.Subject(t, opts...)
	needsUser(t, caller)

	c := names()
	defineCatalog(t, s.Subject(t, conformance.Making(createDefinition), conformance.InTenant(surface, caller.ScopeFor(surface))), &c)

	return caller, c
}

// notYours asserts err refuses what a caller named in another tenant as a
// thing that is not theirs: absent or forbidden.
//
// A tenant wall holds for whoever calls, an operator as much as a member, so
// this is asserted of whatever caller the subject mints for the call. Either
// code is an honest answer, and which one a deployment gives depends on
// whether its rule refuses before it reads or reads and finds nothing; what no
// deployment may answer is the row.
func notYours(t *testing.T, err error, what string) {
	t.Helper()

	must.Error(t, err, must.Sprintf("%s was answered", what))

	code := status.Code(err)
	test.True(t, code == codes.NotFound || code == codes.PermissionDenied,
		test.Sprintf("%s was refused as %s rather than as absent or forbidden", what, code))
}

// needsUser skips unless the subject surfaced the caller's user, which every
// value assertion here names as the subject whose settings they are.
func needsUser(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.UserID == "" {
		conformance.Skip(t, "conformance: this subject does not surface the caller's user identifier")
	}
}

// needsAccount skips unless the subject surfaced the caller's account.
func needsAccount(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.AccountID == "" {
		conformance.Skip(t, "conformance: this subject does not surface the caller's account identifier")
	}
}

// self is the caller as a settings subject: a person acting on their own
// settings, which is the half of this surface every consumer's preferences
// page is.
func self(sub *conformance.Subject) *settingspb.SettingSubject {
	return &settingspb.SettingSubject{Type: subjectUser, Id: sub.UserID}
}

// set answers a setting for the caller, failing the test if it is refused.
func set(t *testing.T, caller *conformance.Subject, name string, value *settingspb.TypedValue) *settingspb.ResolvedSetting {
	t.Helper()

	response, err := caller.Surfaces.Settings.SetValue(caller.Context(t.Context()), &settingspb.SetValueRequest{
		Subject: self(caller),
		Name:    name,
		Value:   value,
	})
	must.NoError(t, err, must.Sprintf("answering setting %q", name))
	must.NotNil(t, response.GetResolution(), must.Sprint("a value was stored and no resolution came back"))

	return response.GetResolution()
}

// resolved is the caller's own resolution of a setting, failing the test if
// it is refused.
func resolved(t *testing.T, caller *conformance.Subject, name string) *settingspb.ResolvedSetting {
	t.Helper()

	response, err := caller.Surfaces.Settings.Resolve(caller.Context(t.Context()),
		&settingspb.ResolveRequest{Subject: self(caller), Name: name})
	must.NoError(t, err, must.Sprintf("resolving setting %q", name))
	must.NotNil(t, response.GetResolution())

	return response.GetResolution()
}

// byName reads a definition the way application code holds one.
func byName(t *testing.T, caller *conformance.Subject, name string) *settingspb.SettingDefinition {
	t.Helper()

	response, err := caller.Surfaces.Settings.GetDefinitionByName(caller.Context(t.Context()),
		&settingspb.GetDefinitionByNameRequest{Name: name})
	must.NoError(t, err, must.Sprintf("reading setting %q by name", name))
	must.NotNil(t, response.GetResult())

	return response.GetResult()
}

// twoDirectories mints two callers and refuses to proceed if the subject put
// them in one tenant, since every confinement assertion here would then
// compare a catalog with itself and pass.
//
// opts are applied to both, and name the calls each makes.
func twoDirectories(t *testing.T, s *conformance.Session, opts ...conformance.SubjectOption) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.TwoTenants(t, surface, opts...)

	return mine, theirs
}

// colleague mints a second caller in of's tenant, minted with opts. A subject
// that cannot put two callers in one tenant declines, and the assertion that
// asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject, opts ...conformance.SubjectOption) *conformance.Subject {
	t.Helper()

	other := s.Subject(t, append(opts, conformance.InTenant(surface, of.ScopeFor(surface)))...)
	needsUser(t, other)

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user"))

	return other
}

// catalogNames walks every page of the catalog caller reaches, asking for
// retired settings as well when includeArchived is set, and returns the names
// it saw.
//
// Every page rather than the first, because a catalog may be shared with the
// rest of the run: once more definitions exist than one page holds, the one an
// assertion just made need not be on page one, and "absent from page one" is
// not "absent" either — which is the half that would pass a retired setting
// still being listed.
func catalogNames(t *testing.T, caller *conformance.Subject, includeArchived bool) []string {
	t.Helper()

	var seen []string

	filter := &filteringpb.QueryFilter{}
	if includeArchived {
		filter.IncludeArchived = &includeArchived
	}

	for range maxPages {
		page, err := caller.Surfaces.Settings.ListDefinitions(caller.Context(t.Context()),
			&settingspb.ListDefinitionsRequest{Filter: filter})
		must.NoError(t, err, must.Sprint("reading the catalog"))

		seen = append(seen, definitionNames(page.GetResults())...)

		next := page.GetPagination().GetCursor()
		if next == "" || len(page.GetResults()) == 0 {
			return seen
		}

		// Cursor has explicit presence, so it is set only once there is one:
		// an empty cursor is a cursor, not the absence of one.
		filter = &filteringpb.QueryFilter{Cursor: &next, IncludeArchived: filter.IncludeArchived}
	}

	t.Fatalf("conformance: the settings catalog was still paging after %d pages", maxPages)

	return nil
}

func definitionNames(results []*settingspb.SettingDefinition) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.GetName())
	}

	return out
}

func stringValue(v string) *settingspb.TypedValue {
	return &settingspb.TypedValue{Value: &settingspb.TypedValue_StringValue{StringValue: v}}
}

func boolValue(v bool) *settingspb.TypedValue {
	return &settingspb.TypedValue{Value: &settingspb.TypedValue_BoolValue{BoolValue: v}}
}

func intValue(v int64) *settingspb.TypedValue {
	return &settingspb.TypedValue{Value: &settingspb.TypedValue_IntValue{IntValue: v}}
}

func floatValue(v float64) *settingspb.TypedValue {
	return &settingspb.TypedValue{Value: &settingspb.TypedValue_FloatValue{FloatValue: v}}
}
