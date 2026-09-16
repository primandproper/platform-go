package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// There used to be a test here asserting that this package's
// ErrTargetNotPermitted and identity/grpc's were the same value. Both were
// aliases of one sentinel and both are gone: the refusal is
// callers.ErrTargetNotPermitted, spelled once for every surface, so what that
// test pinned is now the type checker's and the assertion had become a
// tautology.

// TestAnAuthorizerMayWrapTheSentinelAndStillBeRefusing is what lets an
// implementation say which rule refused without becoming an outage.
//
// The three outcomes are told apart with errors.Is rather than by equality, so
// a consumer wrapping the sentinel with context of their own is still refusing
// and still answers codes.PermissionDenied.
func TestAnAuthorizerMayWrapTheSentinelAndStillBeRefusing(T *testing.T) {
	T.Parallel()

	h := newHarness(T, settingsgrpc.SubjectAuthorizerFunc(
		func(_ context.Context, _ callers.Principal, subject settings.Subject) error {
			return platformerrors.Wrapf(callers.ErrTargetNotPermitted,
				"no membership joins the caller to %s %q", subject.Type, subject.ID)
		},
	))
	h.seedCatalog(T, testScope)

	_, err := h.server.Resolve(h.ctx(T), &settingspb.ResolveRequest{
		Subject: subjectOf(testSubject),
		Name:    digestSetting,
	})
	must.Error(T, err)

	test.ErrorIs(T, err, callers.ErrTargetNotPermitted)
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
		func(_ context.Context, _ callers.Principal, subject settings.Subject) error {
			asked = append(asked, subject)

			return callers.ErrTargetNotPermitted
		},
	))
	h.seedCatalog(T, testScope)

	_, err := h.server.GetValue(h.ctx(T), &settingspb.GetValueRequest{Name: digestSetting})
	must.Error(T, err)
	test.EqOp(T, codes.PermissionDenied, status.Code(err))

	must.SliceLen(T, 1, asked)
	test.EqOp(T, settings.Subject{}, asked[0])
}

// TestTheAuthorizerIsGivenThePrincipalWhole is why the seam takes a
// callers.Principal rather than the one field this package would have picked.
//
// A rule that needs the active account — an administrator editing the settings
// of the account they are signed into — must not have had that fact discarded
// on the way.
func TestTheAuthorizerIsGivenThePrincipalWhole(T *testing.T) {
	T.Parallel()

	h := newHarness(T, settingsgrpc.SubjectAuthorizerFunc(
		func(_ context.Context, caller callers.Principal, subject settings.Subject) error {
			if subject.Type == settings.SubjectAccount && subject.ID == caller.ActiveAccountID() {
				return nil
			}

			return callers.ErrTargetNotPermitted
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
		callers.ErrTargetNotPermitted)
}
