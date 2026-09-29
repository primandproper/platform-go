package callers

// Delegated is the optional interface a [Principal] answers when somebody other
// than its user is acting through it: an operator signed in as a customer to
// see what they see, a support engineer reproducing a report.
//
// It is the second identity slot, and it is optional for the reason
// [Principal]'s method set is final — a consumer whose session type has no
// notion of delegation keeps compiling, and is never delegated.
//
// UserID stays the subject: whose data the request reads and writes, whose
// directory and account it is against, and who every surface in this module
// files the request under. ActorID is who is really at the keyboard. An empty
// ActorID is a principal that is not delegated, which lets a session type carry
// the method on every value rather than only on the delegated ones.
//
// # Why there are two slots
//
// Impersonation used to be declared not a platform notion, on the reasoning
// that every layer below had room for one identity, and that the only way to
// fit an operator into a request was to put the subject's ID where the actor's
// belonged — which yields a working system, and an audit trail that says the
// subject did it. The objection stands, and it is the reason this interface
// exists rather than a reason for its absence: one slot forces that lie, so a
// deployment with an operator tool was always going to tell it, in its own
// interceptor where nothing here could see. A second slot is what lets the
// request be filed under the subject and still name the person who made it.
//
// What the platform owns is the mechanism: this interface, [ActorOf], the
// audit log recording the actor beside the subject, and signin's
// IssueImpersonationToken minting a token that carries both. What it does not
// own is the policy. Who may act as whom is the deployment's, and nothing in
// this module names a permission for it.
type Delegated interface {
	// ActorID is who is acting through this principal, or empty when nobody
	// is and the user is acting for themselves.
	ActorID() string
}

// ActorOf is who is really acting for a principal: the delegated actor when
// there is one, and the principal's own user otherwise.
//
// It is the answer to "who did this" and not to "whose is this". A surface
// filing a row under its owner still reads UserID — an impersonated write is
// the subject's write — and reaches for ActorOf where the question is
// accountability: a rate limit on a person rather than an account, or a log
// line saying who pressed the button.
func ActorOf(p Principal) string {
	if p == nil {
		return ""
	}

	if actor := DelegatedActor(p); actor != "" {
		return actor
	}

	return p.UserID()
}

// DelegatedActor is the actor a principal is delegated to, or empty when it is
// not delegated — the half of [ActorOf] a surface recording both identities
// needs, because it has to tell "acting for themselves" apart from "acting
// through somebody".
//
// A principal whose ActorID names its own user is not delegated: that is a
// person acting as themselves, and recording them twice would describe an
// impersonation that did not happen.
func DelegatedActor(p Principal) string {
	if p == nil {
		return ""
	}

	d, ok := p.(Delegated)
	if !ok {
		return ""
	}

	if actor := d.ActorID(); actor != p.UserID() {
		return actor
	}

	return ""
}
