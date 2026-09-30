package identity

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func users(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("registration writes the user, the account and the membership", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(register))
		username := "conf_" + identifiers.New()

		response, err := caller.Surfaces.Identity.Register(caller.Context(t.Context()), &identitypb.RegisterRequest{
			User: &identitypb.UserRegistrationInput{
				Username:     username,
				EmailAddress: freshEmail(),
				FirstName:    "Some",
				LastName:     "Body",
			},
			Account:    &identitypb.AccountCreationInput{Name: "Acme", TimeZone: "UTC"},
			OwnerRoles: []string{s.Roles().Owner},
		})
		must.NoError(t, err)

		registration := response.GetRegistration()
		must.NotNil(t, registration)
		test.NotEqOp(t, "", registration.GetUser().GetId())
		test.EqOp(t, username, registration.GetUser().GetUsername())
		test.EqOp(t, registration.GetUser().GetId(), registration.GetAccount().GetOwnerUserId())
		test.EqOp(t, registration.GetAccount().GetId(), registration.GetMembership().GetBelongsToAccount())
		test.True(t, registration.GetMembership().GetDefaultAccount(),
			test.Sprint("a registrant's only account should be where they land"))
	})

	t.Run("a username collision is AlreadyExists, in the sentinel's own words", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(register))
		username := "conf_" + identifiers.New()

		register := func(email, account string) error {
			_, err := caller.Surfaces.Identity.Register(caller.Context(t.Context()), &identitypb.RegisterRequest{
				User:       &identitypb.UserRegistrationInput{Username: username, EmailAddress: email},
				Account:    &identitypb.AccountCreationInput{Name: account},
				OwnerRoles: []string{s.Roles().Owner},
			})

			return err
		}

		must.NoError(t, register(freshEmail(), "Acme"))

		err := register(freshEmail(), "Acme Two")
		must.Error(t, err)
		test.EqOp(t, codes.AlreadyExists, status.Code(err))

		// The message is the sentinel's, because it is a client-safe one: the
		// person filling in the form is told the name is taken, not that
		// something went wrong.
		test.StrContains(t, status.Convert(err).Message(), "username")
	})

	t.Run("registration with no user or no account is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(register))
		ctx := caller.Context(t.Context())

		_, err := caller.Surfaces.Identity.Register(ctx, &identitypb.RegisterRequest{
			Account: &identitypb.AccountCreationInput{Name: "an account"},
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		_, err = caller.Surfaces.Identity.Register(ctx, &identitypb.RegisterRequest{
			User: &identitypb.UserRegistrationInput{Username: "conf_" + identifiers.New(), EmailAddress: freshEmail()},
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("an absent user is reported as absent", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(getUser))

		_, err := caller.Surfaces.Identity.GetUser(caller.Context(t.Context()),
			&identitypb.GetUserRequest{UserId: identifiers.New()})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("a profile update saves the caller's own row, and leaves the rest", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(getPrincipal, updateProfile))
		before := self(t, caller)

		response, err := caller.Surfaces.Identity.UpdateProfile(caller.Context(t.Context()), &identitypb.UpdateProfileRequest{
			Input: &identitypb.ProfileUpdateInput{FirstName: new("Renamed")},
		})
		must.NoError(t, err)
		test.EqOp(t, "Renamed", response.GetUser().GetFirstName())
		test.EqOp(t, caller.UserID, response.GetUser().GetId())
		test.EqOp(t, before.GetUsername(), response.GetUser().GetUsername())
		test.EqOp(t, before.GetEmailAddress(), response.GetUser().GetEmailAddress())
	})

	t.Run("a profile update clears what it names empty", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(updateProfile))
		ctx := caller.Context(t.Context())

		named, err := caller.Surfaces.Identity.UpdateProfile(ctx, &identitypb.UpdateProfileRequest{
			Input: &identitypb.ProfileUpdateInput{FirstName: new("Some"), LastName: new("Body")},
		})
		must.NoError(t, err)
		test.EqOp(t, "Some", named.GetUser().GetFirstName())
		test.EqOp(t, "Body", named.GetUser().GetLastName())

		cleared, err := caller.Surfaces.Identity.UpdateProfile(ctx, &identitypb.UpdateProfileRequest{
			Input: &identitypb.ProfileUpdateInput{LastName: new("")},
		})
		must.NoError(t, err)
		test.EqOp(t, "Some", cleared.GetUser().GetFirstName(), test.Sprint("a field the request did not name moved"))
		test.EqOp(t, "", cleared.GetUser().GetLastName(), test.Sprint("a field the request named empty was not cleared"))
	})

	t.Run("a profile update with no input is refused", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(updateProfile))

		_, err := caller.Surfaces.Identity.UpdateProfile(caller.Context(t.Context()), &identitypb.UpdateProfileRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("recording agreement stamps every document named, at one moment", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(recordAgreement))

		// A deployment's registration may already have stamped both documents,
		// at one moment, so a present pair proves nothing about this call: the
		// stamps have to be this call's. A second of slack, as SQLite keeps
		// whole seconds.
		asked := time.Now().Add(-time.Second)

		response, err := caller.Surfaces.Identity.RecordAgreement(caller.Context(t.Context()), &identitypb.RecordAgreementRequest{
			Agreements: []identitypb.Agreement{
				identitypb.Agreement_AGREEMENT_TERMS_OF_SERVICE,
				identitypb.Agreement_AGREEMENT_PRIVACY_POLICY,
			},
		})
		must.NoError(t, err)

		accepted := response.GetUser()
		must.NotNil(t, accepted.GetLastAcceptedTermsOfService())
		must.NotNil(t, accepted.GetLastAcceptedPrivacyPolicy())
		test.True(t, accepted.GetLastAcceptedTermsOfService().AsTime().After(asked),
			test.Sprint("the terms of service carry a stamp from before the call that recorded them"))
		test.True(t, accepted.GetLastAcceptedPrivacyPolicy().AsTime().After(asked),
			test.Sprint("the privacy policy carries a stamp from before the call that recorded it"))
		test.EqOp(t, accepted.GetLastAcceptedTermsOfService().AsTime(), accepted.GetLastAcceptedPrivacyPolicy().AsTime(),
			test.Sprint("two documents accepted in one call were stamped at two moments"))
	})

	t.Run("recording agreement to an unset document is refused, and stamps nothing", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(getPrincipal, recordAgreement))

		// A deployment's registration may already have stamped the terms, so
		// "stamps nothing" is the stamp being what it was, not being absent.
		before := self(t, caller).GetLastAcceptedTermsOfService()

		_, err := caller.Surfaces.Identity.RecordAgreement(caller.Context(t.Context()), &identitypb.RecordAgreementRequest{
			Agreements: []identitypb.Agreement{
				identitypb.Agreement_AGREEMENT_TERMS_OF_SERVICE,
				identitypb.Agreement_AGREEMENT_UNSPECIFIED,
			},
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		after := self(t, caller).GetLastAcceptedTermsOfService()
		test.EqOp(t, before == nil, after == nil,
			test.Sprint("a refused list stamped the entries it had read before the bad one"))
		if before != nil && after != nil {
			test.EqOp(t, before.AsTime(), after.AsTime(),
				test.Sprint("a refused list restamped the entries it had read before the bad one"))
		}
	})

	t.Run("a search by username prefix is confined to the caller's directory", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s, conformance.Making(getPrincipal))
		myName, theirName := self(t, mine).GetUsername(), self(t, theirs).GetUsername()

		search := func(caller *conformance.Subject, prefix string) []string {
			page, err := caller.Surfaces.Identity.SearchUsersByUsername(caller.Context(t.Context()),
				&identitypb.SearchUsersByUsernameRequest{Prefix: prefix})
			must.NoError(t, err)
			test.NotNil(t, page.GetPagination(), test.Sprint("a paged read answered with no pagination"))

			ids := make([]string, 0, len(page.GetResults()))
			for _, u := range page.GetResults() {
				ids = append(ids, u.GetId())
			}

			return ids
		}

		// Each finds itself by its own name, which is the control for each
		// failing to find the other by theirs.
		myOperator := s.Subject(t, conformance.Making(searchUsersByUsername), conformance.InTenant(surface, mine.ScopeFor(surface)))
		theirOperator := s.Subject(t, conformance.Making(searchUsersByUsername), conformance.InTenant(surface, theirs.ScopeFor(surface)))
		test.SliceContains(t, search(myOperator, myName), mine.UserID)
		test.SliceContains(t, search(theirOperator, theirName), theirs.UserID)
		test.SliceNotContains(t, search(myOperator, theirName), theirs.UserID,
			test.Sprint("a search reached a neighboring directory"))
	})

	t.Run("service roles are replaced, and can be withdrawn", func(t *testing.T) {
		t.Parallel()

		user := s.Subject(t)
		operator := s.Subject(t, conformance.Making(setUserServiceRoles), conformance.InTenant(surface, user.ScopeFor(surface)))
		ctx := operator.Context(t.Context())

		granted, err := operator.Surfaces.Identity.SetUserServiceRoles(ctx,
			&identitypb.SetUserServiceRolesRequest{UserId: user.UserID, Roles: []string{s.Roles().Service}})
		must.NoError(t, err)
		test.Eq(t, []string{s.Roles().Service}, granted.GetUser().GetServiceRoles())

		withdrawn, err := operator.Surfaces.Identity.SetUserServiceRoles(ctx,
			&identitypb.SetUserServiceRolesRequest{UserId: user.UserID})
		must.NoError(t, err)
		test.SliceEmpty(t, withdrawn.GetUser().GetServiceRoles(),
			test.Sprint("a merging setter cannot revoke, and this one has to"))
	})

	t.Run("a forced password change can be imposed and released", func(t *testing.T) {
		t.Parallel()

		user := s.Subject(t, conformance.Making(getPrincipal))
		operator := s.Subject(t, conformance.Making(setUserRequiresPasswordChange), conformance.InTenant(surface, user.ScopeFor(surface)))
		ctx := operator.Context(t.Context())

		test.False(t, self(t, user).GetRequiresPasswordChange(),
			test.Sprint("a freshly registered user already owed a password change"))

		imposed, err := operator.Surfaces.Identity.SetUserRequiresPasswordChange(ctx,
			&identitypb.SetUserRequiresPasswordChangeRequest{UserId: user.UserID, RequiresPasswordChange: new(true)})
		must.NoError(t, err)
		test.True(t, imposed.GetUser().GetRequiresPasswordChange())

		released, err := operator.Surfaces.Identity.SetUserRequiresPasswordChange(ctx,
			&identitypb.SetUserRequiresPasswordChangeRequest{UserId: user.UserID, RequiresPasswordChange: new(false)})
		must.NoError(t, err)
		test.False(t, released.GetUser().GetRequiresPasswordChange(),
			test.Sprint("an operator could impose a forced change and not withdraw it"))
	})

	t.Run("a forced password change with no instruction is refused, and releases nothing", func(t *testing.T) {
		t.Parallel()

		user := s.Subject(t, conformance.Making(getPrincipal))
		operator := s.Subject(t, conformance.Making(setUserRequiresPasswordChange), conformance.InTenant(surface, user.ScopeFor(surface)))
		ctx := operator.Context(t.Context())

		_, err := operator.Surfaces.Identity.SetUserRequiresPasswordChange(ctx,
			&identitypb.SetUserRequiresPasswordChangeRequest{UserId: user.UserID, RequiresPasswordChange: new(true)})
		must.NoError(t, err)

		_, err = operator.Surfaces.Identity.SetUserRequiresPasswordChange(ctx,
			&identitypb.SetUserRequiresPasswordChangeRequest{UserId: user.UserID})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		test.True(t, self(t, user).GetRequiresPasswordChange(),
			test.Sprint("a request that named no instruction released one anyway"))
	})

	t.Run("an account status change moves the user", func(t *testing.T) {
		t.Parallel()

		user := s.Subject(t)
		operator := s.Subject(t, conformance.Making(updateUserAccountStatus), conformance.InTenant(surface, user.ScopeFor(surface)))

		response, err := operator.Surfaces.Identity.UpdateUserAccountStatus(operator.Context(t.Context()),
			&identitypb.UpdateUserAccountStatusRequest{
				UserId:      user.UserID,
				Status:      identitypb.AccountStatus_ACCOUNT_STATUS_BANNED,
				Explanation: "spam",
			})
		must.NoError(t, err)
		test.EqOp(t, identitypb.AccountStatus_ACCOUNT_STATUS_BANNED, response.GetUser().GetAccountStatus())
		test.EqOp(t, "spam", response.GetUser().GetAccountStatusExplanation())
	})

	t.Run("an account status change naming no status is refused", func(t *testing.T) {
		t.Parallel()

		user := s.Subject(t)
		operator := s.Subject(t, conformance.Making(updateUserAccountStatus), conformance.InTenant(surface, user.ScopeFor(surface)))

		_, err := operator.Surfaces.Identity.UpdateUserAccountStatus(operator.Context(t.Context()),
			&identitypb.UpdateUserAccountStatusRequest{
				UserId: user.UserID,
				Status: identitypb.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED,
			})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("archiving an account's sole owner leaves no account ownerless", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t)
		needsAccount(t, owner)

		archiveOwner(t, s, owner)
	})

	t.Run("archiving an owner whose account has members leaves no account ownerless", func(t *testing.T) {
		t.Parallel()

		owner := s.Subject(t, conformance.Making(invite))
		needsAccount(t, owner)
		member := colleague(t, s, owner, conformance.Making(getPrincipal, acceptInvitation))
		role, _ := membershipRoles(s)
		join(t, s, owner, member, role)

		archiveOwner(t, s, owner)
	})

	t.Run("the principal read answers for the caller", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(getPrincipal))
		needsAccount(t, caller)

		response, err := caller.Surfaces.Identity.GetPrincipal(caller.Context(t.Context()), &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		principal := response.GetPrincipal()
		must.NotNil(t, principal)
		test.EqOp(t, caller.UserID, principal.GetUser().GetId())
		test.EqOp(t, caller.AccountID, principal.GetActiveAccountId())
		test.SliceNotEmpty(t, principal.GetMemberships())
	})

	// The active account comes back with the principal, and it is the account
	// GetAccount reads: the same row, not a projection of it. An owner, because
	// an owner is who may also make the GetAccount this is compared against.
	t.Run("the principal read returns the active account GetAccount reads", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(getPrincipal, getAccount))
		needsAccount(t, caller)

		response, err := caller.Surfaces.Identity.GetPrincipal(caller.Context(t.Context()), &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		found, err := caller.Surfaces.Identity.GetAccount(caller.Context(t.Context()),
			&identitypb.GetAccountRequest{AccountId: caller.AccountID})
		must.NoError(t, err)

		active := response.GetActiveAccount()
		must.NotNil(t, active)
		test.EqOp(t, caller.AccountID, active.GetId())
		test.True(t, proto.Equal(found.GetAccount(), active),
			test.Sprintf("GetPrincipal's active account %v differs from GetAccount's %v", active, found.GetAccount()))
	})

	t.Run("the principal read serves permissions only where the deployment says it does", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(getPrincipal))

		response, err := caller.Surfaces.Identity.GetPrincipal(caller.Context(t.Context()), &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		if s.Seams().PrincipalPermissions {
			test.NotNil(t, response.GetPermissions(),
				test.Sprint("a deployment that serves permissions answered with no permissions field"))
		} else {
			test.Nil(t, response.GetPermissions(),
				test.Sprint("a deployment that declared no permission resolver served a permissions field"))
		}
	})

	// Nothing here knows what a role permits, so the answer is compared with
	// itself: a function of the roles held in the account asked about gives two
	// callers holding the same role there the same set, and gives a member who
	// names that account the set its membership permits rather than the one
	// their own account does.
	t.Run("the principal's permissions follow the account the read resolved", func(t *testing.T) {
		t.Parallel()

		if !s.Seams().PrincipalPermissions {
			t.Skip("conformance: this deployment serves no permissions on the principal read")
		}

		// The second role, because a vocabulary may spell the first as the
		// owner's, and a member holding the owner's role would be answered as
		// the owner is whichever account the read resolved.
		_, role := membershipRoles(s)

		owner := s.Subject(t, conformance.Making(invite, getPrincipal))
		needsAccount(t, owner)
		member := colleague(t, s, owner, conformance.Making(getPrincipal, acceptInvitation))
		needsAccount(t, member)
		peer := colleague(t, s, owner, conformance.Making(getPrincipal, acceptInvitation))

		join(t, s, owner, member, role)
		join(t, s, owner, peer, role)

		permissionsIn := func(caller *conformance.Subject, accountID string) []string {
			t.Helper()

			request := &identitypb.GetPrincipalRequest{}
			if accountID != "" {
				request.ActiveAccountId = &accountID
			}

			response, err := caller.Surfaces.Identity.GetPrincipal(caller.Context(t.Context()), request)
			must.NoError(t, err)
			must.NotNil(t, response.GetPermissions(),
				must.Sprint("a deployment that serves permissions answered with no permissions field"))

			return response.GetPermissions().GetPermissions()
		}

		// Both are owners of the account they registered with.
		test.Eq(t, permissionsIn(owner, ""), permissionsIn(member, member.AccountID),
			test.Sprint("two owners were told different things about their own accounts"))

		// Both hold role in owner's account, and name it.
		test.Eq(t, permissionsIn(peer, owner.AccountID), permissionsIn(member, owner.AccountID),
			test.Sprint("two members holding one role in an account were told different things about it"))
	})

	t.Run("the principal read refuses a caller who has been banned", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t, conformance.Making(getPrincipal))
		operator := s.Subject(t, conformance.Making(updateUserAccountStatus), conformance.InTenant(surface, caller.ScopeFor(surface)))

		// The control: admitted before.
		_, err := caller.Surfaces.Identity.GetPrincipal(caller.Context(t.Context()), &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		_, err = operator.Surfaces.Identity.UpdateUserAccountStatus(operator.Context(t.Context()),
			&identitypb.UpdateUserAccountStatusRequest{
				UserId:      caller.UserID,
				Status:      identitypb.AccountStatus_ACCOUNT_STATUS_BANNED,
				Explanation: "spam",
			})
		must.NoError(t, err)

		_, err = caller.Surfaces.Identity.GetPrincipal(caller.Context(t.Context()), &identitypb.GetPrincipalRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
	})
}
