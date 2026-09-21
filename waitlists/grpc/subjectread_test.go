package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ownSubjectOnly is the two-line self-service rule the authorizer's
// documentation describes: a caller may read their own signups and nobody
// else's.
func ownSubjectOnly() waitlistsgrpc.SignupAuthorizer {
	return waitlistsgrpc.SignupAuthorizerFuncs{
		Withdrawal: func(context.Context, callers.Principal, tenancy.Scope, string, string) error {
			return nil
		},
		SubjectRead: func(
			_ context.Context, caller callers.Principal, _ tenancy.Scope, subject waitlists.Subject,
		) error {
			if caller != nil && subject.Type == waitlists.SubjectUser && subject.ID == caller.UserID() {
				return nil
			}

			return callers.ErrTargetNotPermitted
		},
	}
}

// TestListSignupsForSubject_Authorized is the half PermissionReadSignups could
// not give back.
//
// That grant covers four reads, the sharpest being GetSignupByContact — an
// oracle over every address in the tenant — so a deployment grants it narrowly
// and correctly, and the one safe read went with it. Whose signups these are is
// a per-request question a grant on the method cannot answer.
func TestListSignupsForSubject_Authorized(T *testing.T) {
	T.Parallel()

	T.Run("a member reads their own", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithAuthorizer(t, ownSubjectOnly())

		_, err := h.server.ListSignupsForSubject(h.ctx(t), &waitlistspb.ListSignupsForSubjectRequest{
			Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: testUser},
		})
		must.NoError(t, err)
	})

	T.Run("and cannot read somebody else's by naming them", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithAuthorizer(t, ownSubjectOnly())

		_, err := h.server.ListSignupsForSubject(h.ctx(t), &waitlistspb.ListSignupsForSubjectRequest{
			Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: "somebody_else"},
		})
		must.Error(t, err)

		// NotFound and not PermissionDenied: a refusal naming the permission
		// would confirm the subject is one this tenant knows about, which is
		// the disclosure the grant exists to prevent.
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	// The fail-closed reading of a deployment that answered one question and not
	// the other: they have not decided, and an undecided question is not a
	// permitted one.
	//
	// A nil field is where that omission is visible. There is no one-closure
	// adapter any more — there was, and it satisfied the interface while
	// silently refusing the half it could not carry, which is a second way for
	// a consumer not to notice a question exists. This finding came from the
	// first way.
	T.Run("an unanswered read rule refuses", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithAuthorizer(t, waitlistsgrpc.SignupAuthorizerFuncs{
			Withdrawal: func(context.Context, callers.Principal, tenancy.Scope, string, string) error {
				return nil
			},
		})

		_, err := h.server.ListSignupsForSubject(h.ctx(t), &waitlistspb.ListSignupsForSubjectRequest{
			Subject: &waitlistspb.SignupSubject{Type: string(waitlists.SubjectUser), Id: testUser},
		})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}
