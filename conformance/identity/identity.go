package identity

import (
	"testing"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "identity"

// The calls this suite makes, as the names a caller is minted to make them by.
const (
	acceptInvitation               = identitypb.IdentityService_AcceptInvitation_FullMethodName
	archiveAccount                 = identitypb.IdentityService_ArchiveAccount_FullMethodName
	archiveUser                    = identitypb.IdentityService_ArchiveUser_FullMethodName
	cancelInvitation               = identitypb.IdentityService_CancelInvitation_FullMethodName
	createAccount                  = identitypb.IdentityService_CreateAccount_FullMethodName
	getAccount                     = identitypb.IdentityService_GetAccount_FullMethodName
	getInvitation                  = identitypb.IdentityService_GetInvitation_FullMethodName
	getMembership                  = identitypb.IdentityService_GetMembership_FullMethodName
	getPrincipal                   = identitypb.IdentityService_GetPrincipal_FullMethodName
	getUser                        = identitypb.IdentityService_GetUser_FullMethodName
	invite                         = identitypb.IdentityService_Invite_FullMethodName
	listAccountMembers             = identitypb.IdentityService_ListAccountMembers_FullMethodName
	listAccounts                   = identitypb.IdentityService_ListAccounts_FullMethodName
	listAccountsForUser            = identitypb.IdentityService_ListAccountsForUser_FullMethodName
	listInvitationsForEmailAddress = identitypb.IdentityService_ListInvitationsForEmailAddress_FullMethodName
	listInvitationsFromUser        = identitypb.IdentityService_ListInvitationsFromUser_FullMethodName
	listMembershipsForUser         = identitypb.IdentityService_ListMembershipsForUser_FullMethodName
	listUsers                      = identitypb.IdentityService_ListUsers_FullMethodName
	recordAgreement                = identitypb.IdentityService_RecordAgreement_FullMethodName
	rejectInvitation               = identitypb.IdentityService_RejectInvitation_FullMethodName
	removeMembership               = identitypb.IdentityService_RemoveMembership_FullMethodName
	searchUsersByUsername          = identitypb.IdentityService_SearchUsersByUsername_FullMethodName
	setDefaultAccount              = identitypb.IdentityService_SetDefaultAccount_FullMethodName
	setMembershipRoles             = identitypb.IdentityService_SetMembershipRoles_FullMethodName
	setUserRequiresPasswordChange  = identitypb.IdentityService_SetUserRequiresPasswordChange_FullMethodName
	setUserServiceRoles            = identitypb.IdentityService_SetUserServiceRoles_FullMethodName
	transferAccountOwnership       = identitypb.IdentityService_TransferAccountOwnership_FullMethodName
	updateAccount                  = identitypb.IdentityService_UpdateAccount_FullMethodName
	updateProfile                  = identitypb.IdentityService_UpdateProfile_FullMethodName
	updateUserAccountStatus        = identitypb.IdentityService_UpdateUserAccountStatus_FullMethodName
)

// Suite is the identity surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
		Mounted: func(s conformance.Surfaces) bool { return s.Identity != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("accounts", func(t *testing.T) {
		t.Parallel()
		accounts(t, s)
	})
	t.Run("memberships", func(t *testing.T) {
		t.Parallel()
		memberships(t, s)
	})
	t.Run("invitations", func(t *testing.T) {
		t.Parallel()
		invitations(t, s)
	})
	t.Run("users", func(t *testing.T) {
		t.Parallel()
		users(t, s)
	})
	t.Run("absences", func(t *testing.T) {
		t.Parallel()
		absences(t, s)
	})

	t.Run("a read by id is scoped to the caller's directory", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)

		// Reading a user by id is the directory's read, made by whoever the
		// subject mints for it in each directory.
		making := []string{
			getUser,
		}
		myOperator := s.Subject(t, conformance.Making(making...), conformance.InTenant(surface, mine.ScopeFor(surface)))
		theirOperator := s.Subject(t, conformance.Making(making...), conformance.InTenant(surface, theirs.ScopeFor(surface)))

		// The positive control. "The neighbor's user is absent" is also true of
		// a read that reaches no directory at all, so this is what makes the
		// refusal below mean confinement rather than breakage.
		found, err := myOperator.Surfaces.Identity.GetUser(myOperator.Context(t.Context()),
			&identitypb.GetUserRequest{UserId: mine.UserID})
		must.NoError(t, err, must.Sprint("this directory's operator cannot read a user in it; the absence below proves nothing"))
		test.EqOp(t, mine.UserID, found.GetUser().GetId())

		// Absent rather than forbidden, which is what it is from here and is
		// the answer that is not an oracle: a refusal would confirm the
		// identifier names somebody.
		_, err = myOperator.Surfaces.Identity.GetUser(myOperator.Context(t.Context()),
			&identitypb.GetUserRequest{UserId: theirs.UserID})
		must.Error(t, err, must.Sprint("a neighboring directory's user was readable"))
		test.EqOp(t, codes.NotFound, status.Code(err),
			test.Sprint("a neighboring directory's user was refused as something other than absent"))

		// And the mirror image, which is what rules out a rule that happens to
		// favor whichever caller was made first.
		found, err = theirOperator.Surfaces.Identity.GetUser(theirOperator.Context(t.Context()),
			&identitypb.GetUserRequest{UserId: theirs.UserID})
		must.NoError(t, err)
		test.EqOp(t, theirs.UserID, found.GetUser().GetId())
	})

	t.Run("a listing pages the caller's directory only", func(t *testing.T) {
		t.Parallel()

		mine, theirs := twoDirectories(t, s)
		operator := s.Subject(t, conformance.Making(listUsers), conformance.InTenant(surface, mine.ScopeFor(surface)))

		page, err := operator.Surfaces.Identity.ListUsers(operator.Context(t.Context()),
			&identitypb.ListUsersRequest{})
		must.NoError(t, err)

		ids := make([]string, 0, len(page.GetResults()))
		for _, user := range page.GetResults() {
			ids = append(ids, user.GetId())
		}

		// Presence and absence of two known users, never a count: this listing
		// may run against a database the suite does not own.
		test.SliceContains(t, ids, mine.UserID,
			test.Sprint("this caller's own user was missing from its directory listing"))
		test.SliceNotContains(t, ids, theirs.UserID,
			test.Sprint("a neighboring directory's user reached this listing"))

		test.NotNil(t, page.GetPagination(),
			test.Sprint("a paged read answered with no pagination"))
	})

	// The end-to-end half of what the schema already forbids, and the reason it
	// is worth asserting twice: a field the proto does not declare cannot be
	// rendered by this module's converter, but nothing stops a deployment
	// putting its own projection in front of this surface.
	t.Run("a read never renders a credential", func(t *testing.T) {
		t.Parallel()

		credentialed := s.Seams().Actions.Credentialed
		s.NeedsAction(t, credentialed != nil, "credentialed")

		mine := s.Subject(t)

		marker, err := credentialed(mine.Context(t.Context()), mine.ScopeFor(surface), mine.UserID)
		must.NoError(t, err, must.Sprint("giving this caller a stored secret"))
		must.StrNotEqFold(t, "", marker,
			must.Sprint("the credentialed action reported no fragment to search for, so this assertion would pass against any response"))

		// Through the directory's read, the one most likely to be projected by
		// something in front of this surface.
		operator := s.Subject(t, conformance.Making(getUser), conformance.InTenant(surface, mine.ScopeFor(surface)))

		found, err := operator.Surfaces.Identity.GetUser(operator.Context(t.Context()),
			&identitypb.GetUserRequest{UserId: mine.UserID})
		must.NoError(t, err)

		// The whole rendered message rather than a named field: a credential
		// that arrived in a metadata map, an error detail or a field added
		// later is the case a field-by-field check would miss.
		test.StrNotContains(t, found.GetUser().String(), marker,
			test.Sprint("a stored credential was rendered to a client"))
	})
}

// twoDirectories mints two callers and refuses to proceed if the subject put
// them in one tenant.
//
// The check is not paranoia about the seam. A subject whose NewSubject ignores
// its request and hands back one caller twice would make every confinement
// assertion here compare a directory with itself, and all of them would pass.
//
// opts are applied to both, and name the calls each makes.
func twoDirectories(t *testing.T, s *conformance.Session, opts ...conformance.SubjectOption) (mine, theirs *conformance.Subject) {
	t.Helper()

	mine, theirs = s.TwoTenants(t, surface, opts...)

	must.StrNotEqFold(t, mine.UserID, theirs.UserID,
		must.Sprint("the subject minted two callers as one user"))

	return mine, theirs
}
