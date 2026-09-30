package grpc_test

import (
	"context"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// operatorID is the caller the administrative tests make their requests as.
// Nobody in the directory has it, which is the point: the RPCs are about the
// user they name, and the caller is only ever the actor.
const operatorID = "operator_1"

// revocationHooks records what the sign-in service reported ending.
type revocationHooks struct {
	signin.NoopHooks

	revocations []*signin.Revocation

	mu sync.Mutex
}

func (h *revocationHooks) AfterRevokeSignIns(_ context.Context, _ database.Tx, _ tenancy.Scope, r *signin.Revocation) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.revocations = append(h.revocations, r)

	return nil
}

func (h *revocationHooks) heard() []*signin.Revocation {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]*signin.Revocation(nil), h.revocations...)
}

// newAdministrationHarness is a refresh harness whose service reports what it
// ends to hooks.
func newAdministrationHarness(t *testing.T) (*harness, *revocationHooks) {
	t.Helper()

	hooks := &revocationHooks{}

	return newRefreshHarness(t, []signin.ServiceOption{signin.WithHooks(hooks)}), hooks
}

// liveFamilies is the families the harness's user still holds, read through
// the service rather than the RPC under test.
func (h *harness) liveFamilies(t *testing.T) []string {
	t.Helper()

	signIns, err := h.svc.ListSignIns(t.Context(), testScope, h.user.ID, 0)
	must.NoError(t, err)

	out := make([]string, 0, len(signIns))
	for _, signIn := range signIns {
		out = append(out, signIn.FamilyID)
	}

	return out
}

func TestServer_ListSignInsForUser(T *testing.T) {
	T.Parallel()

	T.Run("lists the named user's logins, marking none current", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		phone := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		// From inside one of the user's own logins, so that a server marking
		// the caller's family current would have one to mark.
		listed, err := h.admin.ListSignInsForUser(asUserIn(h.rootCtx, operatorID, laptop.GetFamilyId()),
			&signinpb.ListSignInsForUserRequest{UserId: h.user.ID})
		must.NoError(t, err)
		must.SliceLen(t, 2, listed.GetSignIns())

		families := map[string]bool{}
		for _, signIn := range listed.GetSignIns() {
			families[signIn.GetFamilyId()] = signIn.GetCurrent()

			test.EqOp(t, h.accountID, signIn.GetActiveAccountId())
		}

		test.Eq(t, map[string]bool{phone.GetFamilyId(): false, laptop.GetFamilyId(): false}, families)
	})

	T.Run("honors a limit", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		h.signInAsJane(t)
		h.signInAsJane(t)

		listed, err := h.admin.ListSignInsForUser(asUser(h.rootCtx, operatorID),
			&signinpb.ListSignInsForUserRequest{UserId: h.user.ID, Limit: 1})
		must.NoError(t, err)
		test.SliceLen(t, 1, listed.GetSignIns())
	})

	T.Run("a request naming nobody is invalid", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		_, err := h.admin.ListSignInsForUser(asUser(h.rootCtx, operatorID), &signinpb.ListSignInsForUserRequest{})
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		_, err := h.admin.ListSignInsForUser(h.rootCtx, &signinpb.ListSignInsForUserRequest{UserId: h.user.ID})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestServer_EndSignInForUser(T *testing.T) {
	T.Parallel()

	T.Run("ends the named login, reported as the operator's act", func(t *testing.T) {
		t.Parallel()

		h, hooks := newAdministrationHarness(t)

		phone := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		_, err := h.admin.EndSignInForUser(asUser(h.rootCtx, operatorID), &signinpb.EndSignInForUserRequest{
			UserId:   h.user.ID,
			FamilyId: phone.GetFamilyId(),
		})
		must.NoError(t, err)

		test.Eq(t, []string{laptop.GetFamilyId()}, h.liveFamilies(t))

		_, err = h.client.ExchangeRefreshToken(h.rootCtx,
			&signinpb.ExchangeRefreshTokenRequest{RefreshToken: phone.GetRefreshToken()})
		test.EqOp(t, codes.Unauthenticated, status.Code(err), test.Sprint("the ended login still refreshes"))

		heard := hooks.heard()
		must.SliceLen(t, 1, heard)
		test.EqOp(t, signin.RevocationOperator, heard[0].Reason)
		test.EqOp(t, h.user.ID, heard[0].SubjectID)
		test.EqOp(t, operatorID, heard[0].ActorID)
		test.Eq(t, []string{phone.GetFamilyId()}, heard[0].FamilyIDs)
	})

	// The user is part of the key: a family named against the wrong person is
	// nobody's to end, and is answered the way a family that never existed is.
	T.Run("a family that is not the named user's ends nothing", func(t *testing.T) {
		t.Parallel()

		h, hooks := newAdministrationHarness(t)

		phone := h.signInAsJane(t)

		_, err := h.admin.EndSignInForUser(asUser(h.rootCtx, operatorID), &signinpb.EndSignInForUserRequest{
			UserId:   "somebody_else",
			FamilyId: phone.GetFamilyId(),
		})
		must.NoError(t, err)

		test.Eq(t, []string{phone.GetFamilyId()}, h.liveFamilies(t))
		test.SliceEmpty(t, hooks.heard())
	})

	T.Run("a request naming no user is invalid, and ends nothing", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		phone := h.signInAsJane(t)

		_, err := h.admin.EndSignInForUser(asUser(h.rootCtx, operatorID),
			&signinpb.EndSignInForUserRequest{FamilyId: phone.GetFamilyId()})
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		test.Eq(t, []string{phone.GetFamilyId()}, h.liveFamilies(t))
	})

	T.Run("a request naming no login is invalid", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		_, err := h.admin.EndSignInForUser(asUser(h.rootCtx, operatorID),
			&signinpb.EndSignInForUserRequest{UserId: h.user.ID})
		test.ErrorIs(t, err, signin.ErrEmptyFamilyID)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		_, err := h.admin.EndSignInForUser(h.rootCtx,
			&signinpb.EndSignInForUserRequest{UserId: h.user.ID, FamilyId: "family"})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

func TestServer_EndAllSignInsForUser(T *testing.T) {
	T.Parallel()

	T.Run("ends every login the named user holds, reported as the operator's act", func(t *testing.T) {
		t.Parallel()

		h, hooks := newAdministrationHarness(t)

		phone := h.signInAsJane(t)
		laptop := h.signInAsJane(t)

		_, err := h.admin.EndAllSignInsForUser(asUser(h.rootCtx, operatorID),
			&signinpb.EndAllSignInsForUserRequest{UserId: h.user.ID})
		must.NoError(t, err)

		test.SliceEmpty(t, h.liveFamilies(t))

		heard := hooks.heard()
		must.SliceLen(t, 1, heard)
		test.EqOp(t, signin.RevocationOperator, heard[0].Reason)
		test.EqOp(t, h.user.ID, heard[0].SubjectID)
		test.EqOp(t, operatorID, heard[0].ActorID)
		test.SliceContainsAll(t, []string{phone.GetFamilyId(), laptop.GetFamilyId()}, heard[0].FamilyIDs)
	})

	T.Run("a request naming nobody is invalid", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		_, err := h.admin.EndAllSignInsForUser(asUser(h.rootCtx, operatorID), &signinpb.EndAllSignInsForUserRequest{})
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("an anonymous caller is refused", func(t *testing.T) {
		t.Parallel()

		h, _ := newAdministrationHarness(t)

		_, err := h.admin.EndAllSignInsForUser(h.rootCtx, &signinpb.EndAllSignInsForUserRequest{UserId: h.user.ID})
		test.ErrorIs(t, err, signingrpc.ErrNoPrincipal)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}
