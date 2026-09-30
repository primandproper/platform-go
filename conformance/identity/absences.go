package identity

import (
	"context"
	"path"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// absentTarget is one call naming a row, asked about a row that does not
// exist.
type absentTarget struct {
	// mint makes the caller, minted to make call, and a real target that
	// caller may name through it.
	mint func(t *testing.T, s *conformance.Session, call string) (caller *conformance.Subject, existing string)
	// do makes the call against id.
	do func(ctx context.Context, caller *conformance.Subject, id string) error
	// absent asserts the refusal of an identifier nobody wrote.
	absent func(t *testing.T, err error)
	call   string
}

// absences asserts that a call naming something that does not exist is
// refused as absent (or forbidden), and never answered or failed as a fault.
func absences(t *testing.T, s *conformance.Session) {
	t.Helper()

	// A call naming a user reaches the store, which finds nobody: there is no
	// rule in front of an operator's writes about a user, so absent is the
	// only honest answer. A call naming an account or an invitation passes
	// the deployment's rule first, and this module's default refuses a row
	// nobody is a member of as forbidden rather than confirming it does not
	// exist; a rule that admits operators everywhere reads it and finds
	// nothing. Both are notYours.
	absentUser := func(what string) func(t *testing.T, err error) {
		return func(t *testing.T, err error) {
			t.Helper()

			must.Error(t, err, must.Sprintf("%s naming a user nobody registered was answered", what))
			test.EqOp(t, codes.NotFound, status.Code(err),
				test.Sprintf("%s naming a user nobody registered was refused as something other than absent", what))
		}
	}
	absentRow := func(what string) func(t *testing.T, err error) {
		return func(t *testing.T, err error) {
			t.Helper()
			notYours(t, err, what+" naming a row nobody wrote")
		}
	}

	// The real targets. A user is a fresh one in the operator's directory; an
	// account is one the caller opens for the purpose, because the archival
	// row archives it and the account a caller was minted with may be one a
	// subject's operators share.
	aUser := func(t *testing.T, s *conformance.Session, call string) (*conformance.Subject, string) {
		t.Helper()

		user := s.Subject(t)
		operator := s.Subject(t, conformance.Making(call), conformance.InTenant(surface, user.ScopeFor(surface)))

		return operator, user.UserID
	}
	anAccount := func(t *testing.T, s *conformance.Session, call string) (*conformance.Subject, string) {
		t.Helper()

		caller := s.Subject(t, conformance.Making(call, createAccount))

		return caller, openAccount(t, s, caller).GetId()
	}
	anInvitation := func(t *testing.T, s *conformance.Session, call string) (*conformance.Subject, string) {
		t.Helper()

		role, _ := membershipRoles(s)
		caller := s.Subject(t, conformance.Making(call, invite))

		return caller, sendInvitation(t, caller, freshEmail(), role).GetId()
	}

	targets := []*absentTarget{
		{
			call:   archiveUser,
			mint:   aUser,
			absent: absentUser("an archival"),
			do: func(ctx context.Context, caller *conformance.Subject, id string) error {
				_, err := caller.Surfaces.Identity.ArchiveUser(ctx, &identitypb.ArchiveUserRequest{UserId: id})
				return err
			},
		},
		{
			call:   updateUserAccountStatus,
			mint:   aUser,
			absent: absentUser("an account status change"),
			do: func(ctx context.Context, caller *conformance.Subject, id string) error {
				_, err := caller.Surfaces.Identity.UpdateUserAccountStatus(ctx, &identitypb.UpdateUserAccountStatusRequest{
					UserId:      id,
					Status:      identitypb.AccountStatus_ACCOUNT_STATUS_BANNED,
					Explanation: "a target that may not exist",
				})
				return err
			},
		},
		{
			call:   setUserServiceRoles,
			mint:   aUser,
			absent: absentUser("a service role change"),
			do: func(ctx context.Context, caller *conformance.Subject, id string) error {
				_, err := caller.Surfaces.Identity.SetUserServiceRoles(ctx, &identitypb.SetUserServiceRolesRequest{UserId: id})
				return err
			},
		},
		{
			call:   setUserRequiresPasswordChange,
			mint:   aUser,
			absent: absentUser("a forced password change"),
			do: func(ctx context.Context, caller *conformance.Subject, id string) error {
				_, err := caller.Surfaces.Identity.SetUserRequiresPasswordChange(ctx,
					&identitypb.SetUserRequiresPasswordChangeRequest{UserId: id, RequiresPasswordChange: new(false)})
				return err
			},
		},
		{
			call:   getAccount,
			mint:   anAccount,
			absent: absentRow("an account read"),
			do: func(ctx context.Context, caller *conformance.Subject, id string) error {
				_, err := caller.Surfaces.Identity.GetAccount(ctx, &identitypb.GetAccountRequest{AccountId: id})
				return err
			},
		},
		{
			call:   updateAccount,
			mint:   anAccount,
			absent: absentRow("an account update"),
			do: func(ctx context.Context, caller *conformance.Subject, id string) error {
				// With an input, or the refusal is of the empty request.
				_, err := caller.Surfaces.Identity.UpdateAccount(ctx, &identitypb.UpdateAccountRequest{
					AccountId: id,
					Input:     &identitypb.AccountUpdateInput{Name: new("Renamed")},
				})
				return err
			},
		},
		{
			call:   archiveAccount,
			mint:   anAccount,
			absent: absentRow("an account archival"),
			do: func(ctx context.Context, caller *conformance.Subject, id string) error {
				_, err := caller.Surfaces.Identity.ArchiveAccount(ctx, &identitypb.ArchiveAccountRequest{AccountId: id})
				return err
			},
		},
		{
			call:   getInvitation,
			mint:   anInvitation,
			absent: absentRow("an invitation read"),
			do: func(ctx context.Context, caller *conformance.Subject, id string) error {
				_, err := caller.Surfaces.Identity.GetInvitation(ctx, &identitypb.GetInvitationRequest{InvitationId: id})
				return err
			},
		},
	}

	for _, target := range targets {
		t.Run(path.Base(target.call)+" on a target that does not exist is refused as absent, never as a fault", func(t *testing.T) {
			t.Parallel()

			caller, existing := target.mint(t, s, target.call)
			ctx := caller.Context(t.Context())

			// The positive control: the same caller naming a row that exists
			// is not told it is absent. Not "answered": an owner's archival
			// may be refused as a precondition, and that is a refusal of a
			// real target.
			err := target.do(ctx, caller, existing)
			test.NotEqOp(t, codes.NotFound, status.Code(err),
				test.Sprint("the same call on a real target was refused as absent; the refusal below proves nothing"))

			target.absent(t, target.do(ctx, caller, identifiers.New()))
		})
	}
}
