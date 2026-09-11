package oauth2clients

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Admits reports whether this registration may be used to authorize the named
// subject.
//
// It is the whole of the cross-tenant guarantee, and it is a method on the
// entity rather than a rule inside either seam so that the two seams cannot
// disagree about it. Both of them — the SubjectAuthenticator that runs the login
// form and the SubjectResolver that answers for a request already carrying a
// credential — call this and nothing else.
//
// # Why it lives here and not in the store
//
// The store's lookup by client_id takes no tenancy.Scope, because it is what
// resolves one: client_id is server-minted and globally unique, and the row is
// the only thing that knows which registry the client is in. That read is the
// machinery carve-out the tenancy convention names, and the price of the
// carve-out is that resolving a registration cannot itself be an authorization
// decision — it has no subject to decide about.
//
// This is where the subject arrives. /authorize carries client_id in the request
// the seams are handed, and the subject is what they have just authenticated, so
// they are the one place in the system holding both facts. Nothing earlier can
// make this check and nothing later is early enough: by /token the code has
// already been minted.
//
// # The rules
//
// A registration in the global registry admits any subject. That is the
// administered arrangement — an operator minted this client to speak for the
// service, on behalf of whoever signs in — and it is not a check being skipped:
// it is the row saying it belongs to no registry in particular.
//
// A registration naming a registry admits only subjects in it, and answers
// [ErrClientScopeMismatch] otherwise.
//
// A registration naming an owner admits only that person, and answers
// [ErrClientOwnerMismatch] otherwise. A registration naming none admits any
// subject its registry already admitted.
//
// The two are checked in that order because the registry is the coarser fact
// and the message for it is the one a person in the wrong organization can act
// on.
func (c *Client) Admits(scope tenancy.Scope, userID string) error {
	if c == nil {
		return ErrNilClient
	}

	if !c.Scope.IsGlobal() && c.Scope != scope {
		return platformerrors.Wrapf(ErrClientScopeMismatch,
			"oauth2 client %q is registered to another scope", c.ClientID)
	}

	if !c.Administered() && c.BelongsToUser != userID {
		return platformerrors.Wrapf(ErrClientOwnerMismatch,
			"oauth2 client %q belongs to another user", c.ClientID)
	}

	return nil
}
