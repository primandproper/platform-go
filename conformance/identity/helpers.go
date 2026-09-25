package identity

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test/must"
)

// roleSupport is the membership role these assertions grant. Role names are
// the consumer's, and nothing here asserts what one permits — only that the
// name granted is the name held.
const roleSupport = "support"

// colleague mints a second caller in of's directory. A subject that cannot put
// two callers in one tenant declines, and the assertion that asked skips.
func colleague(t *testing.T, s *conformance.Session, of *conformance.Subject) *conformance.Subject {
	t.Helper()

	other := s.Subject(t, conformance.InTenant(of.Scope))

	must.StrNotEqFold(t, of.UserID, other.UserID,
		must.Sprint("the subject minted a colleague as the same user"))

	return other
}

// needsAccount skips unless the subject surfaced the caller's account, which
// every account-shaped assertion here names.
func needsAccount(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.AccountID == "" {
		t.Skip("conformance: this subject does not surface the caller's account identifier")
	}
}

// self reads a caller's own user, which is how a suite learns the parts of it
// the subject did not report — an email address, a username.
func self(t *testing.T, sub *conformance.Subject) *identitypb.User {
	t.Helper()

	found, err := sub.Surfaces.Identity.GetUser(sub.Context(t.Context()),
		&identitypb.GetUserRequest{UserId: sub.UserID})
	must.NoError(t, err, must.Sprint("a caller could not read its own user"))

	return found.GetUser()
}

// freshEmail is an address nobody registered, for invitations that are about
// the invitation rather than about who receives it.
func freshEmail() string { return identifiers.New() + "@conformance.invalid" }

// invite sends an invitation into sender's account.
func invite(t *testing.T, sender *conformance.Subject, toEmail string, roles ...string) *identitypb.Invitation {
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

	token, err := delivered(t.Context(), sender.Scope, invitationID)
	must.NoError(t, err, must.Sprint("reading the token the deployment delivered"))
	must.StrNotEqFold(t, "", token, must.Sprint("the deployment delivered an empty token"))

	return token
}

// join makes member a member of owner's account the way a product does: an
// invitation to the member's own address, and the member accepting it with the
// token the deployment delivered.
func join(t *testing.T, s *conformance.Session, owner, member *conformance.Subject, roles ...string) *identitypb.Membership {
	t.Helper()

	invitation := invite(t, owner, self(t, member).GetEmailAddress(), roles...)

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
