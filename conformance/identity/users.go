package identity

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func users(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("registration writes the user, the account and the membership", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		username := "conf_" + identifiers.New()

		response, err := caller.Surfaces.Identity.Register(caller.Context(t.Context()), &identitypb.RegisterRequest{
			User: &identitypb.UserRegistrationInput{
				Username:     username,
				EmailAddress: freshEmail(),
				FirstName:    "Some",
				LastName:     "Body",
			},
			Account:    &identitypb.AccountCreationInput{Name: "Acme", TimeZone: "UTC"},
			OwnerRoles: []string{"owner"},
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

		caller := s.Subject(t)
		username := "conf_" + identifiers.New()

		register := func(email, account string) error {
			_, err := caller.Surfaces.Identity.Register(caller.Context(t.Context()), &identitypb.RegisterRequest{
				User:       &identitypb.UserRegistrationInput{Username: username, EmailAddress: email},
				Account:    &identitypb.AccountCreationInput{Name: account},
				OwnerRoles: []string{"owner"},
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

		caller := s.Subject(t)
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

		caller := s.Subject(t)

		_, err := caller.Surfaces.Identity.GetUser(caller.Context(t.Context()),
			&identitypb.GetUserRequest{UserId: identifiers.New()})
		must.Error(t, err)
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	t.Run("a profile update saves the caller's own row, and leaves the rest", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
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

		caller := s.Subject(t)
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

		caller := s.Subject(t)

		_, err := caller.Surfaces.Identity.UpdateProfile(caller.Context(t.Context()), &identitypb.UpdateProfileRequest{})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("recording agreement stamps every document named, at one moment", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

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
		test.EqOp(t, accepted.GetLastAcceptedTermsOfService().AsTime(), accepted.GetLastAcceptedPrivacyPolicy().AsTime(),
			test.Sprint("two documents accepted in one call were stamped at two moments"))
	})

	t.Run("recording agreement to an unset document is refused, and stamps nothing", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)

		_, err := caller.Surfaces.Identity.RecordAgreement(caller.Context(t.Context()), &identitypb.RecordAgreementRequest{
			Agreements: []identitypb.Agreement{
				identitypb.Agreement_AGREEMENT_TERMS_OF_SERVICE,
				identitypb.Agreement_AGREEMENT_UNSPECIFIED,
			},
		})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		test.Nil(t, self(t, caller).GetLastAcceptedTermsOfService(),
			test.Sprint("a refused list stamped the entries it had read before the bad one"))
	})

	t.Run("a search by username prefix is confined to the caller's directory", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
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
		test.SliceContains(t, search(mine, myName), mine.UserID)
		test.SliceContains(t, search(theirs, theirName), theirs.UserID)
		test.SliceNotContains(t, search(mine, theirName), theirs.UserID,
			test.Sprint("a search reached a neighboring directory"))
	})

	t.Run("service roles are replaced, and can be withdrawn", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		user := colleague(t, s, operator)
		ctx := operator.Context(t.Context())

		granted, err := operator.Surfaces.Identity.SetUserServiceRoles(ctx,
			&identitypb.SetUserServiceRolesRequest{UserId: user.UserID, Roles: []string{"service_admin"}})
		must.NoError(t, err)
		test.Eq(t, []string{"service_admin"}, granted.GetUser().GetServiceRoles())

		withdrawn, err := operator.Surfaces.Identity.SetUserServiceRoles(ctx,
			&identitypb.SetUserServiceRolesRequest{UserId: user.UserID})
		must.NoError(t, err)
		test.SliceEmpty(t, withdrawn.GetUser().GetServiceRoles(),
			test.Sprint("a merging setter cannot revoke, and this one has to"))
	})

	t.Run("a forced password change can be imposed and released", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		user := colleague(t, s, operator)
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

		operator := s.Subject(t)
		user := colleague(t, s, operator)
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

		operator := s.Subject(t)
		user := colleague(t, s, operator)

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

		operator := s.Subject(t)
		user := colleague(t, s, operator)

		_, err := operator.Surfaces.Identity.UpdateUserAccountStatus(operator.Context(t.Context()),
			&identitypb.UpdateUserAccountStatusRequest{
				UserId: user.UserID,
				Status: identitypb.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED,
			})
		must.Error(t, err)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("the last owner of an account cannot be archived", func(t *testing.T) {
		t.Parallel()

		operator := s.Subject(t)
		owner := colleague(t, s, operator)

		_, err := operator.Surfaces.Identity.ArchiveUser(operator.Context(t.Context()),
			&identitypb.ArchiveUserRequest{UserId: owner.UserID})
		must.Error(t, err)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})

	t.Run("the principal read answers for the caller", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		needsAccount(t, caller)

		response, err := caller.Surfaces.Identity.GetPrincipal(caller.Context(t.Context()), &identitypb.GetPrincipalRequest{})
		must.NoError(t, err)

		principal := response.GetPrincipal()
		must.NotNil(t, principal)
		test.EqOp(t, caller.UserID, principal.GetUser().GetId())
		test.EqOp(t, caller.AccountID, principal.GetActiveAccountId())
		test.SliceNotEmpty(t, principal.GetMemberships())
	})

	t.Run("the principal read refuses a caller who has been banned", func(t *testing.T) {
		t.Parallel()

		caller := s.Subject(t)
		operator := colleague(t, s, caller)

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
