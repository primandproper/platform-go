package identity

import (
	"testing"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// membershipRoles are the two account roles these assertions grant, from the
// subject's vocabulary. Role names are the consumer's, and nothing here asserts
// what one permits — only that the name granted is the name held.
func membershipRoles(s *conformance.Session) (first, second string) {
	roles := s.Roles().Membership

	return roles[0], roles[1]
}

// colleague mints a second caller in of's directory, minted with opts. A
// subject that cannot put two callers in one tenant declines, and the
// assertion that asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject, opts ...conformance.SubjectOption) *conformance.Subject {
	t.Helper()

	other := s.Subject(t, append(opts, conformance.InTenant(surface, of.ScopeFor(surface)))...)

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user"))

	return other
}

// notYours asserts err refuses what a caller named in another tenant as a
// thing that is not theirs: absent or forbidden.
//
// A tenant wall holds for whoever calls, an operator as much as a member, so
// this is asserted of whatever caller the subject mints for the call. Either
// code is an honest answer, and which one a deployment gives depends on
// whether its rule refuses before it reads or reads and finds nothing; what no
// deployment may answer is the row.
func notYours(t *testing.T, err error, what string) {
	t.Helper()

	must.Error(t, err, must.Sprintf("%s was answered", what))

	code := status.Code(err)
	test.True(t, code == codes.NotFound || code == codes.PermissionDenied,
		test.Sprintf("%s was refused as %s rather than as absent or forbidden", what, code))
}

// needsAccount skips unless the subject surfaced the caller's account, which
// every account-shaped assertion here names.
func needsAccount(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.AccountID == "" {
		conformance.Skip(t, "conformance: this subject does not surface the caller's account identifier")
	}
}

// self reads a caller's own user, which is how a suite learns the parts of it
// the subject did not report — an email address, a username.
//
// Through GetPrincipal rather than GetUser. GetUser is the directory's read,
// and a caller reading themselves through it would be asserting that whoever
// reads their own user may read the directory. GetPrincipal is the self-service
// read, and sub names it among the calls it was minted to make.
func self(t *testing.T, sub *conformance.Subject) *identitypb.User {
	t.Helper()

	found, err := sub.Surfaces.Identity.GetPrincipal(sub.Context(t.Context()), &identitypb.GetPrincipalRequest{})
	must.NoError(t, err, must.Sprint("a caller could not read its own principal"))

	return found.GetPrincipal().GetUser()
}

// freshEmail is an address nobody registered, for invitations that are about
// the invitation rather than about who receives it.
func freshEmail() string { return identifiers.New() + "@conformance.invalid" }

// sendInvitation sends an invitation into sender's account.
func sendInvitation(t *testing.T, sender *conformance.Subject, toEmail string, roles ...string) *identitypb.Invitation {
	t.Helper()

	needsAccount(t, sender)

	response, err := sender.Surfaces.Identity.Invite(sender.Context(t.Context()), &identitypb.InviteRequest{
		AccountId: sender.AccountID,
		ToEmail:   toEmail,
		ToName:    "Some Body",
		Note:      "come and join us",
		Roles:     roles,
	})
	must.NoError(t, err, must.Sprint("sending an invitation"))
	must.NotNil(t, response.GetInvitation())

	return response.GetInvitation()
}

// tokenFor is what the deployment delivered for an invitation, or a skip where
// the subject cannot say.
func tokenFor(t *testing.T, s *conformance.Session, sender *conformance.Subject, invitationID string) string {
	t.Helper()

	delivered := s.Seams().Actions.InvitationToken
	s.NeedsAction(t, delivered != nil, "invitation token")

	token, err := delivered(t.Context(), sender.ScopeFor(surface), invitationID)
	must.NoError(t, err, must.Sprint("reading the token the deployment delivered"))
	must.StrNotEqFold(t, "", token, must.Sprint("the deployment delivered an empty token"))

	return token
}

// join makes member a member of owner's account the way a product does: an
// invitation to the member's own address, and the member accepting it with the
// token the deployment delivered.
func join(t *testing.T, s *conformance.Session, owner, member *conformance.Subject, roles ...string) *identitypb.Membership {
	t.Helper()

	invitation := sendInvitation(t, owner, self(t, member).GetEmailAddress(), roles...)

	accepted, err := member.Surfaces.Identity.AcceptInvitation(member.Context(t.Context()),
		&identitypb.AcceptInvitationRequest{
			InvitationId: invitation.GetId(),
			Token:        tokenFor(t, s, owner, invitation.GetId()),
		})
	must.NoError(t, err, must.Sprint("accepting an invitation with the token the deployment delivered"))

	return accepted.GetAcceptance().GetMembership()
}

// memberIDs are the users on an account's roster.
func memberIDs(t *testing.T, caller *conformance.Subject, accountID string) []string {
	t.Helper()

	roster, err := caller.Surfaces.Identity.ListAccountMembers(caller.Context(t.Context()),
		&identitypb.ListAccountMembersRequest{AccountId: accountID})
	must.NoError(t, err)

	out := make([]string, 0, len(roster.GetResults()))
	for _, m := range roster.GetResults() {
		out = append(out, m.GetMembership().GetBelongsToUser())
	}

	return out
}

func accountIDs(accounts []*identitypb.Account) []string {
	out := make([]string, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, a.GetId())
	}

	return out
}

// archiveOwner archives an account owner as an operator and asserts the
// invariant the archival is answerable to: no account is left answering to a
// user every scoped read now reports as absent.
//
// The invariant rather than the mechanism, because the mechanism is the
// deployment's. This module's store refuses the archival with
// FailedPrecondition and leaves the owner to transfer or close what they hold
// first; a deployment may instead settle it on the owner's behalf — hand each
// account to a remaining member, close the ones nobody else is in — and archive
// the owner in the same transaction. Both leave every account owned by somebody
// who exists or closed, and an assertion that demanded the refusal was
// asserting this module's default against a deployment that had deliberately
// chosen a different one. What still fails is the archival that neither refused
// nor settled.
func archiveOwner(t *testing.T, s *conformance.Session, owner *conformance.Subject) {
	t.Helper()

	operator := s.Subject(t, conformance.Making(archiveUser, listAccounts), conformance.InTenant(surface, owner.ScopeFor(surface)))

	owned := ownedBy(directoryAccounts(t, operator), owner.UserID)
	must.SliceContains(t, owned, owner.AccountID,
		must.Sprint("the directory listing does not show the owner owning their account; the assertion below proves nothing"))

	_, err := operator.Surfaces.Identity.ArchiveUser(operator.Context(t.Context()),
		&identitypb.ArchiveUserRequest{UserId: owner.UserID})
	if err != nil {
		test.EqOp(t, codes.FailedPrecondition, status.Code(err),
			test.Sprint("an owner's archival was refused, but not as an unmet precondition"))
		test.Eq(t, owned, ownedBy(directoryAccounts(t, operator), owner.UserID),
			test.Sprint("a refused archival changed what the owner owns"))

		return
	}

	// A listed account is a live one, so an account the archived owner still
	// owns is one that was neither closed nor handed on.
	test.SliceEmpty(t, ownedBy(directoryAccounts(t, operator), owner.UserID),
		test.Sprint("an owner was archived and left accounts that are neither closed nor owned by anybody else"))
}

// maxPages bounds a walk through a directory this suite does not own. A
// deployment may keep one directory every account in the run shares, so the
// account an assertion is looking for may not be on the first page — and a walk
// with no bound is a test that never ends against a cursor that never does.
const maxPages = 100

// directoryAccounts walks every page of the operator's listing of their
// directory.
//
// Every page rather than the first, because a directory may be shared with the
// rest of the run: once more accounts exist than one page holds, the owner's
// need not be on page one, and "owns nothing on page one" is not "owns nothing"
// either — which is the half that would pass an archival that left an account
// answering to nobody.
func directoryAccounts(t *testing.T, operator *conformance.Subject) []*identitypb.Account {
	t.Helper()

	return walkAccounts(t, operator, nil)
}

// closedAccountsToo is directoryAccounts asked with include_archived set, which
// is the walk a console makes to show the accounts somebody closed.
func closedAccountsToo(t *testing.T, operator *conformance.Subject) []*identitypb.Account {
	t.Helper()

	include := true

	return walkAccounts(t, operator, &include)
}

// walkAccounts is the walk behind both, sending includeArchived on every page.
func walkAccounts(t *testing.T, operator *conformance.Subject, includeArchived *bool) []*identitypb.Account {
	t.Helper()

	var (
		seen   []*identitypb.Account
		filter *filteringpb.QueryFilter
	)

	// The first page names no filter at all unless there is something to ask,
	// so the ordinary walk is the request a client that set nothing sends.
	if includeArchived != nil {
		filter = &filteringpb.QueryFilter{IncludeArchived: includeArchived}
	}

	for range maxPages {
		page, err := operator.Surfaces.Identity.ListAccounts(operator.Context(t.Context()),
			&identitypb.ListAccountsRequest{Filter: filter})
		must.NoError(t, err, must.Sprint("reading the directory's accounts"))

		seen = append(seen, page.GetResults()...)

		next := page.GetPagination().GetCursor()
		if next == "" || len(page.GetResults()) == 0 {
			return seen
		}

		// Cursor has explicit presence, so it is set only once there is one:
		// an empty cursor is a cursor, not the absence of one.
		filter = &filteringpb.QueryFilter{Cursor: &next, IncludeArchived: includeArchived}
	}

	t.Fatalf("conformance: the account directory was still paging after %d pages", maxPages)

	return nil
}

// ownedBy are the identifiers of the listed accounts userID owns.
func ownedBy(listed []*identitypb.Account, userID string) []string {
	var out []string

	for _, a := range listed {
		if a.GetOwnerUserId() == userID {
			out = append(out, a.GetId())
		}
	}

	return out
}

// landing reads userID's memberships through caller, and answers the accounts
// userID holds and the ones marked as where they land.
//
// Through ListMembershipsForUser rather than the principal read, because the
// principal resolves its active account off the caller's credential, and a
// credential minted before a default moved may still name where it was.
func landing(t *testing.T, caller *conformance.Subject, userID string) (held, defaults []string) {
	t.Helper()

	memberships, err := caller.Surfaces.Identity.ListMembershipsForUser(caller.Context(t.Context()),
		&identitypb.ListMembershipsForUserRequest{UserId: userID})
	must.NoError(t, err)

	for _, m := range memberships.GetResults() {
		held = append(held, m.GetBelongsToAccount())

		if m.GetDefaultAccount() {
			defaults = append(defaults, m.GetBelongsToAccount())
		}
	}

	return held, defaults
}

// openAccount opens a second account for caller, owned by them, which is what
// an assertion that archives or rewrites an account acts on: never the account
// the caller was minted with, which a subject whose operator is one shared
// person would be taking from everybody else in the run.
func openAccount(t *testing.T, s *conformance.Session, caller *conformance.Subject) *identitypb.Account {
	t.Helper()

	response, err := caller.Surfaces.Identity.CreateAccount(caller.Context(t.Context()), &identitypb.CreateAccountRequest{
		Name:       "conf_" + identifiers.New(),
		OwnerRoles: []string{s.Roles().Owner},
	})
	must.NoError(t, err, must.Sprint("opening a second account"))
	must.NotNil(t, response.GetAccount())

	return response.GetAccount()
}
