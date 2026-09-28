package audit

import (
	"github.com/primandproper/platform-go/v14/callers"
)

// PrincipalActor is the Actor for a request a principal made: its user, as an
// ActorUser, and — when somebody else is acting through that user — the
// operator as Impersonator, read off [callers.Delegated].
//
// It is the helper every surface recording an entry for a request reaches for,
// and a consumer's own hooks reach for it too, so that an impersonated request
// is recorded the same way whoever writes the entry. Written by hand, the
// natural spelling is Actor{ID: p.UserID()}, which is exactly the entry that says
// the subject did it.
//
// The address is left to the caller, because a principal does not carry one;
// set IP on the result where the transport knows it. A nil principal is the
// zero Actor, which Record refuses with ErrEmptyActor — a request with nobody
// on it is not an entry to file under nobody.
func PrincipalActor(p callers.Principal) Actor {
	if p == nil {
		return Actor{}
	}

	return Actor{
		ID:           p.UserID(),
		Type:         ActorUser,
		Impersonator: callers.DelegatedActor(p),
	}
}
