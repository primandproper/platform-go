package settings

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"

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

// operator is whoever writes the catalog in of's tenant.
//
// The catalog half of this surface is an administrator's — defining a setting
// is a deployment's decision in the sense a database column is — so where the
// subject can mint an administrator into the tenant, that is who defines. Where
// it cannot, the caller itself is asked, which is what the assembled subject
// answers: it enforces no method grants, so its ordinary caller may define. A
// deployment that enforces them and mints no administrator is caught by
// [define], which skips rather than asserting against a refusal the deployment
// was right to make.
func operator(t *testing.T, s *conformance.Session, of *conformance.Subject) *conformance.Subject {
	t.Helper()

	admin, err := s.Seams().NewSubject(t.Context(), conformance.AsAdmin(), conformance.InTenant(of.Scope))

	switch {
	case platformerrors.Is(err, conformance.ErrSubjectUnsupported):
		return of
	case err != nil:
		t.Fatalf("conformance: minting an administrator into a caller's tenant: %v", err)
	case admin == nil:
		t.Fatal("conformance: the subject factory returned no administrator and no error")
	}

	return admin
}

// define adds a setting to op's catalog through the surface, skipping where the
// deployment does not let op do that.
func define(t *testing.T, op *conformance.Subject, input *settingspb.SettingDefinitionInput) *settingspb.SettingDefinition {
	t.Helper()

	response, err := op.Surfaces.Settings.CreateDefinition(op.Context(t.Context()),
		&settingspb.CreateDefinitionRequest{Definition: input})
	if status.Code(err) == codes.PermissionDenied {
		t.Skip("conformance: this caller may not define a setting and the subject mints no administrator who may; " +
			"the assertion needs a setting it defined")
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
// is where most of these assertions start.
func seeded(t *testing.T, s *conformance.Session) (*conformance.Subject, catalog) {
	t.Helper()

	caller := s.Subject(t)
	needsUser(t, caller)

	c := names()
	defineCatalog(t, operator(t, s, caller), &c)

	return caller, c
}

// needsUser skips unless the subject surfaced the caller's user, which every
// value assertion here names as the subject whose settings they are.
func needsUser(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.UserID == "" {
		t.Skip("conformance: this subject does not surface the caller's user identifier")
	}
}

// needsAccount skips unless the subject surfaced the caller's account.
func needsAccount(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.AccountID == "" {
		t.Skip("conformance: this subject does not surface the caller's account identifier")
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
func twoDirectories(t *testing.T, s *conformance.Session) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.Subject(t), s.Subject(t)

	must.StrNotEqFold(t, mine.Scope.String(), theirs.Scope.String(),
		must.Sprint("the subject minted two callers in one tenant; the confinement this asserts cannot be observed"))

	return mine, theirs
}

// colleague mints a second caller in of's tenant. A subject that cannot put two
// callers in one tenant declines, and the assertion that asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject) *conformance.Subject {
	t.Helper()

	other := s.Subject(t, conformance.InTenant(of.Scope))
	needsUser(t, other)

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user"))

	return other
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
