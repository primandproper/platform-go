package authserver

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2server"
	"github.com/primandproper/platform-go/v14/database"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	"github.com/primandproper/platform-go/v14/observability"
)

// guard is the registry lookup both paths to an authorization code make, and
// the two things they make it with.
//
// # Why it is one type and not two copies
//
// [Authenticator] and [GuardedResolver] exist because there are two ways to
// reach an authorization code and oauth2clients.Client.Admits has to be on both
// — that is the whole argument of this package. The two seams therefore run the
// same procedure up to the comparison itself: read the client_id the request
// names, resolve it in the registry, and decide whether there is anything to
// compare against.
//
// Written twice, that procedure is the one duplication here where drift is a
// security hole rather than an inconsistency, and it is asymmetric in a way that
// hides it. A change made to one copy leaves the *other* path unguarded, and the
// unguarded path is whichever one the reviewer was not looking at — most likely
// the resolver's, which is the path a deployment's own first-party application
// takes and so the one with the most traffic and the least likely to produce a
// report. The failure is silent by construction: [GuardedResolver] answers a
// refusal as (nil, nil), which is indistinguishable from "this credential is not
// one of mine", so a copy that stopped checking would not error, would not log,
// and would send the caller to a login form that succeeds.
//
// # What it deliberately does not do
//
// It stops short of Admits. That last step is the one place the two seams
// legitimately differ — one refuses with a message a form renders, the other
// declines so the request falls through to that form — and the reasoning for
// each is long and belongs beside the code that makes the choice. See
// [Authenticator.admits] and [GuardedResolver.ResolveSubject].
//
// It also resolves no scope, and cannot. The authenticator reads one off the
// request through its [ScopeResolver] and hands it to signin before the check;
// the resolver takes one back from the consumer's own session. That asymmetry is
// load-bearing rather than incidental — see [ScopedSubjectResolver] — so the
// scope arrives at Admits as an argument each seam already holds.
type guard struct {
	registry oauth2clients.Store
	client   database.Client
}

// registrationFor reads the registration an authorization request names.
//
// A nil registration and a nil error means the check does not apply: the request
// named no client. The authorization server has already refused it, before
// either seam was reached, and duplicating that refusal here would be a second
// place deciding what a malformed request is.
//
// That is the only "leave it alone" answer, and it is deliberately not the same
// branch as an unresolvable client_id. A client_id this registry has never
// issued is [ErrClientNotRegistered] and fails the request: it is unreachable in
// a deployment wired as this package documents — the server's own lookup runs
// through [Store.GetClient] and has already refused an unknown client before
// either seam is asked anything — and in one that is not, it is the
// misconfiguration that would otherwise skip this check on every request without
// saying so. See [ErrClientNotRegistered].
//
// A registry that is broken is neither. It is returned as itself, and both
// callers fail the request on it, because turning "we cannot look you up" into
// "nothing to check" would convert a database that is down into a silent bypass
// on both paths at once.
//
// The database handle is taken for Reader(): /authorize is not inside a
// transaction of the consumer's.
func (g *guard) registrationFor(
	ctx context.Context,
	op observability.Operation,
	req *http.Request,
) (*oauth2clients.Client, error) {
	clientID := req.FormValue(oauth2server.FieldClientID)
	if clientID == "" {
		//nolint:nilnil // No client named is "the check does not apply", which is a registration this procedure does not have and an error it must not invent.
		return nil, nil
	}

	op.Set(clientIDKey, clientID)

	registered, err := g.registry.ResolveClientID(ctx, g.client.Reader(), clientID)
	if err != nil {
		if platformerrors.Is(err, oauth2clients.ErrClientNotFound) {
			return nil, op.Error(platformerrors.Wrapf(ErrClientNotRegistered, "oauth2 client %q", clientID),
				"resolving oauth2 client %q", clientID)
		}

		return nil, op.Error(err, "resolving oauth2 client %q", clientID)
	}

	return registered, nil
}
