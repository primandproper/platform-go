package grpc_test

import (
	"context"
	"errors"
	"testing"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestTheSentinelIsTheDirectorysOne is the reason this package mints no second
// name for "the caller may not act on that row": a consumer has one rule about
// standing, and an authorizer written for the directory refuses a withdrawal
// with the answer it already returns.
func TestTheSentinelIsTheDirectorysOne(T *testing.T) {
	T.Parallel()

	test.EqOp(T, identitygrpc.ErrTargetNotPermitted, waitlistsgrpc.ErrTargetNotPermitted)
}

// TestWithdrawAsksTheAuthorizerBeforeItWrites is the ordering the seam depends
// on: a refused withdrawal has to leave the row where it was, not roll one back.
func TestWithdrawAsksTheAuthorizerBeforeItWrites(T *testing.T) {
	T.Parallel()

	h := newHarnessWithAuthorizer(T, waitlistsgrpc.SignupAuthorizerFunc(
		func(context.Context, waitlistsgrpc.Principal, tenancy.Scope, string, string) error {
			return waitlistsgrpc.ErrTargetNotPermitted
		}))

	list := h.seedOpenList(T, testScope)
	signup := h.seedSignup(T, testScope, list.ID, "ada@example.com")

	res, err := h.server.Withdraw(h.anonCtx(T), &waitlistspb.WithdrawRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	test.Nil(T, res)
	must.Error(T, err)

	read, err := h.server.GetSignup(h.ctx(T), &waitlistspb.GetSignupRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	must.NoError(T, err)

	test.EqOp(T, waitlistspb.SignupStatus_SIGNUP_STATUS_WAITING, read.GetResult().GetStatus())
	test.EqOp(T, "ada@example.com", read.GetResult().GetContact())
}

// TestARefusedWithdrawalReadsAsAnAbsence is the decision this surface makes that
// the directory's does not, and it is deliberate rather than a rounding of
// PermissionDenied.
//
// The caller here is frequently anonymous and holds an identifier somebody
// handed them. A PermissionDenied on a signup that is not theirs and a NotFound
// on one that does not exist would be two answers a caller walking identifiers
// could tell apart, which is precisely the enumeration this service refuses to
// be — so a refusal and an absence are answered the same way.
func TestARefusedWithdrawalReadsAsAnAbsence(T *testing.T) {
	T.Parallel()

	h := newHarnessWithAuthorizer(T, waitlistsgrpc.SignupAuthorizerFunc(
		func(context.Context, waitlistsgrpc.Principal, tenancy.Scope, string, string) error {
			return waitlistsgrpc.ErrTargetNotPermitted
		}))

	list := h.seedOpenList(T, testScope)
	signup := h.seedSignup(T, testScope, list.ID, "ada@example.com")

	refused, err := h.server.Withdraw(h.anonCtx(T), &waitlistspb.WithdrawRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	test.Nil(T, refused)
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))

	// A permitted caller naming an identifier that is not there gets the same
	// code, which is what "the same way" means.
	permitting := newHarness(T)
	absent, err := permitting.server.Withdraw(permitting.anonCtx(T), &waitlistspb.WithdrawRequest{
		ListId: list.ID, SignupId: "no-such-signup",
	})
	test.Nil(T, absent)
	must.Error(T, err)
	test.EqOp(T, codes.NotFound, status.Code(err))
}

// TestAnUndecidedAuthorizerIsAServerFault is the other half of what an
// implementation owes: a database that would not answer is not a refusal.
//
// Reporting the second as the first tells a consumer to widen their policy while
// their link store is down, and leaves a dashboard counting server faults
// reading zero through it.
func TestAnUndecidedAuthorizerIsAServerFault(T *testing.T) {
	T.Parallel()

	unavailable := errors.New("the link store would not answer")

	h := newHarnessWithAuthorizer(T, waitlistsgrpc.SignupAuthorizerFunc(
		func(context.Context, waitlistsgrpc.Principal, tenancy.Scope, string, string) error {
			return unavailable
		}))

	list := h.seedOpenList(T, testScope)
	signup := h.seedSignup(T, testScope, list.ID, "ada@example.com")

	res, err := h.server.Withdraw(h.anonCtx(T), &waitlistspb.WithdrawRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	test.Nil(T, res)
	must.Error(T, err)

	test.EqOp(T, codes.Internal, status.Code(err))
	test.ErrorIs(T, err, unavailable)
}

// TestAWrappedSentinelStillRefuses covers what the seam's documentation promises
// an implementation: the three answers are distinguished by errors.Is, so an
// implementation may wrap the sentinel with context of its own.
func TestAWrappedSentinelStillRefuses(T *testing.T) {
	T.Parallel()

	h := newHarnessWithAuthorizer(T, waitlistsgrpc.SignupAuthorizerFunc(
		func(_ context.Context, _ waitlistsgrpc.Principal, _ tenancy.Scope, _, signupID string) error {
			return platformerrors.Wrapf(waitlistsgrpc.ErrTargetNotPermitted,
				"the link named a different signup than %q", signupID)
		}))

	list := h.seedOpenList(T, testScope)
	signup := h.seedSignup(T, testScope, list.ID, "ada@example.com")

	res, err := h.server.Withdraw(h.anonCtx(T), &waitlistspb.WithdrawRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	test.Nil(T, res)
	must.Error(T, err)

	test.EqOp(T, codes.NotFound, status.Code(err))
}

// TestTheAuthorizerIsHandedWhatItNeedsToDecide pins the arguments, because they
// are the whole of what an implementation has to work with: the caller, which is
// nil for the anonymous request an unsubscribe link is, the tenant the request
// resolved to, and the two identifiers it named.
func TestTheAuthorizerIsHandedWhatItNeedsToDecide(T *testing.T) {
	T.Parallel()

	var (
		seenCaller     waitlistsgrpc.Principal
		seenScope      tenancy.Scope
		seenList       string
		seenSignup     string
		timesConsulted int
	)

	h := newHarnessWithAuthorizer(T, waitlistsgrpc.SignupAuthorizerFunc(
		func(_ context.Context, caller waitlistsgrpc.Principal, scope tenancy.Scope, listID, signupID string) error {
			seenCaller, seenScope, seenList, seenSignup = caller, scope, listID, signupID
			timesConsulted++

			return nil
		}))

	list := h.seedOpenList(T, testScope)
	signup := h.seedSignup(T, testScope, list.ID, "ada@example.com")

	_, err := h.server.Withdraw(h.anonCtx(T), &waitlistspb.WithdrawRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	must.NoError(T, err)

	test.EqOp(T, 1, timesConsulted)
	test.Nil(T, seenCaller)
	test.EqOp(T, testScope, seenScope)
	test.EqOp(T, list.ID, seenList)
	test.EqOp(T, signup.ID, seenSignup)
}

// TestASignedInCallerReachesTheAuthorizer is the deployment whose unsubscribe
// page sits behind a sign-in: the principal is handed over rather than dropped,
// so such a consumer answers from the caller instead of from a token.
func TestASignedInCallerReachesTheAuthorizer(T *testing.T) {
	T.Parallel()

	var seen string

	h := newHarnessWithAuthorizer(T, waitlistsgrpc.SignupAuthorizerFunc(
		func(_ context.Context, caller waitlistsgrpc.Principal, _ tenancy.Scope, _, _ string) error {
			if caller != nil {
				seen = caller.UserID()
			}

			return nil
		}))

	list := h.seedOpenList(T, testScope)
	signup := h.seedSignup(T, testScope, list.ID, "ada@example.com")

	_, err := h.server.Withdraw(h.ctx(T), &waitlistspb.WithdrawRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	must.NoError(T, err)

	test.EqOp(T, testUser, seen)
}

// TestNoOtherRPCConsultsTheAuthorizer keeps the seam narrow. It is the answer to
// one question — may this caller withdraw this signup — and a surface that
// consulted it elsewhere would be one where a consumer's rule about unsubscribe
// links silently gated a console.
func TestNoOtherRPCConsultsTheAuthorizer(T *testing.T) {
	T.Parallel()

	var consulted int

	h := newHarnessWithAuthorizer(T, waitlistsgrpc.SignupAuthorizerFunc(
		func(context.Context, waitlistsgrpc.Principal, tenancy.Scope, string, string) error {
			consulted++

			return nil
		}))

	list := h.seedOpenList(T, testScope)
	signup := h.seedSignup(T, testScope, list.ID, "ada@example.com")

	_, err := h.server.GetSignup(h.ctx(T), &waitlistspb.GetSignupRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	must.NoError(T, err)

	_, err = h.server.Invite(h.ctx(T), &waitlistspb.InviteRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	must.NoError(T, err)

	_, err = h.server.ArchiveSignup(h.ctx(T), &waitlistspb.ArchiveSignupRequest{
		ListId: list.ID, SignupId: signup.ID,
	})
	must.NoError(T, err)

	test.EqOp(T, 0, consulted)
}

// TestTheSentinelIsNeverClientSafe is why this file asserts something about
// waitlists' registration rather than only about this package.
//
// Half of what the surface does with the refusal is answer as though the row
// were absent, and a sentinel registered as safe to quote would put "the caller
// may not act on the named target" on that answer — telling a caller walking
// identifiers exactly what the NotFound was hiding.
func TestTheSentinelIsNeverClientSafe(T *testing.T) {
	T.Parallel()

	for _, safe := range waitlists.ClientSafeSentinels {
		test.NotEqOp(T, waitlistsgrpc.ErrTargetNotPermitted, safe)
	}
}
