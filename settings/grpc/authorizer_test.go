package grpc_test

import (
	"context"
	"testing"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestTheRefusalIsTheDirectorysOwnSentinel is the decision not to mint a second
// one.
//
// A consumer has one rule about which principals a caller has standing over,
// and an authorizer written for the directory refuses a settings read with the
// answer it already returns. An errors.Is against either name matches, because
// they are the same value.
func TestTheRefusalIsTheDirectorysOwnSentinel(T *testing.T) {
	T.Parallel()

	test.ErrorIs(T, settingsgrpc.ErrTargetNotPermitted, identitygrpc.ErrTargetNotPermitted)
	test.ErrorIs(T, identitygrpc.ErrTargetNotPermitted, settingsgrpc.ErrTargetNotPermitted)
}

// TestAnAuthorizerMayWrapTheSentinelAndStillBeRefusing is what lets an
// implementation say which rule refused without becoming an outage.
//
// The three outcomes are told apart with errors.Is rather than by equality, so
// a consumer wrapping the sentinel with context of their own is still refusing
// and still answers codes.PermissionDenied.
func TestAnAuthorizerMayWrapTheSentinelAndStillBeRefusing(T *testing.T) {
	T.Parallel()

	h := newHarness(T, settingsgrpc.SubjectAuthorizerFunc(
		func(_ context.Context, _ settingsgrpc.Principal, subject settings.Subject) error {
			return platformerrors.Wrapf(settingsgrpc.ErrTargetNotPermitted,
				"no membership joins the caller to %s %q", subject.Type, subject.ID)
		},
	))
	h.seedCatalog(T, testScope)

	_, err := h.server.Resolve(h.ctx(T), &settingspb.ResolveRequest{
		Subject: subjectOf(testSubject),
		Name:    digestSetting,
	})
	must.Error(T, err)

	test.ErrorIs(T, err, settingsgrpc.ErrTargetNotPermitted)
	test.EqOp(T, codes.PermissionDenied, status.Code(err))
}

// TestTheAuthorizerIsAskedWithWhateverTheRequestCarried is the contract the
// seam's documentation states: it runs before the subject has been validated
// against anything, so an implementation is handed the empty subject as
// readily as a real one.
//
// Refusing that one is free — a subject naming no type and no id is nobody's —
// and asking first is what keeps the surface from telling an unauthorized
// caller which subjects are well formed.
func TestTheAuthorizerIsAskedWithWhateverTheRequestCarried(T *testing.T) {
	T.Parallel()

	var asked []settings.Subject

	h := newHarness(T, settingsgrpc.SubjectAuthorizerFunc(
		func(_ context.Context, _ settingsgrpc.Principal, subject settings.Subject) error {
			asked = append(asked, subject)

			return settingsgrpc.ErrTargetNotPermitted
		},
	))
	h.seedCatalog(T, testScope)

	_, err := h.server.GetValue(h.ctx(T), &settingspb.GetValueRequest{Name: digestSetting})
	must.Error(T, err)
	test.EqOp(T, codes.PermissionDenied, status.Code(err))

	must.SliceLen(T, 1, asked)
	test.EqOp(T, settings.Subject{}, asked[0])
}

// TestTheAuthorizerIsGivenThePrincipalWhole is why the seam takes a Principal
// rather than the one field this package would have picked.
//
// A rule that needs the active account — an administrator editing the settings
// of the account they are signed into — must not have had that fact discarded
// on the way.
func TestTheAuthorizerIsGivenThePrincipalWhole(T *testing.T) {
	T.Parallel()

	h := newHarness(T, settingsgrpc.SubjectAuthorizerFunc(
		func(_ context.Context, caller settingsgrpc.Principal, subject settings.Subject) error {
			if subject.Type == settings.SubjectAccount && subject.ID == caller.ActiveAccountID() {
				return nil
			}

			return settingsgrpc.ErrTargetNotPermitted
		},
	))
	h.seedCatalog(T, testScope)

	response, err := h.server.Resolve(h.ctx(T), &settingspb.ResolveRequest{
		Subject: subjectOf(accountSubject),
		Name:    digestSetting,
	})
	must.NoError(T, err)
	test.EqOp(T, settingspb.ValueSource_VALUE_SOURCE_DEFAULT, response.GetResolution().GetSource())
}

// TestSubjectAuthorizerFuncIsTheInterface: the adapter exists so a consumer
// whose rule is one closure over something they already hold does not write a
// type for it.
func TestSubjectAuthorizerFuncIsTheInterface(T *testing.T) {
	T.Parallel()

	var authorizer settingsgrpc.SubjectAuthorizer = settingsgrpc.SubjectAuthorizerFunc(selfOnly)

	test.NoError(T, authorizer.AuthorizeSubject(T.Context(),
		&testPrincipal{userID: testUser, scope: testScope}, testSubject))

	test.ErrorIs(T, authorizer.AuthorizeSubject(T.Context(),
		&testPrincipal{userID: testUser, scope: testScope}, strangeSubject),
		settingsgrpc.ErrTargetNotPermitted)
}
