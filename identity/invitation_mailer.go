package identity

import (
	"context"
)

// InvitationMail is what an InvitationMailer is handed once an invitation has
// been issued and committed.
//
// The token travels beside the invitation rather than on it. Invitation is
// redacted — the address to send to, the name to greet, the sender, the note
// and the roles are all here, and Token and TokenDigest are not — so a mailer
// that logs, queues or renders the invitation it was given does not carry the
// secret along by accident. Token is the one field that goes in the link.
type InvitationMail struct {
	// Invitation is the row Service.Invite wrote, redacted.
	Invitation *Invitation

	// Token is the secret the invitation's link carries, which the caller
	// minted and the column holds only the digest of. Nothing else hands it
	// over once Invite has returned.
	Token string
}

// InvitationMailer delivers the message an invitation exists to send.
//
// It is the one place a configured Service sends an invitation's token, and
// that is the reason it exists rather than the hook alone. An invitation token
// joins an account, and a hook's argument goes wherever a consumer's events go
// — an outbox, a webhook subscriber, an analytics vendor. With a mailer
// configured, Hooks.AfterInvite receives the invitation without its token, and
// the secret reaches the invitee's mailbox and nothing else.
//
// It is called after the transaction that wrote the invitation has committed,
// and never from inside it, for the reason passwordreset's Mailer is: a send
// from inside the callback would be a link delivered for an invitation that
// then rolled back, and no retry can un-send it. It is called once per
// invitation.
//
// An error from it fails Service.Invite, and by then the invitation is
// committed. An invitation whose link was never delivered is a request that
// accomplished nothing, so the caller is told; their retry issues a second
// invitation beside the first rather than replacing it, and the addressee may
// answer either.
type InvitationMailer interface {
	SendInvitation(ctx context.Context, mail *InvitationMail) error
}

// InvitationMailerFunc adapts a function to InvitationMailer.
type InvitationMailerFunc func(ctx context.Context, mail *InvitationMail) error

// SendInvitation implements InvitationMailer.
func (f InvitationMailerFunc) SendInvitation(ctx context.Context, mail *InvitationMail) error {
	return f(ctx, mail)
}
