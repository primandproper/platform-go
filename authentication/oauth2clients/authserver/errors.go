package authserver

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// ErrClientNotRegistered is an authorization request naming a client this
// registry has never issued.
//
// # Why it is a refusal and not a shrug
//
// Both login seams reach the registry after the authorization server has
// already resolved the same client_id through [Store.GetClient] — see
// oauth2server.Server.AuthorizeHandler, which calls it before it consults a
// SubjectResolver and long before it asks a SubjectAuthenticator anything. So
// in a deployment wired as this package's documentation describes, a client_id
// that reaches a seam is one the registry has just answered for and this
// sentinel is unreachable.
//
// It is reachable in exactly one situation, and that situation is a bypass: a
// deployment that wired [NewAuthenticator] or [NewGuardedResolver] but left the
// authorization server on some other oauth2server.Store. Then the protocol half
// resolves its clients from one table and the seams look for them in another,
// every lookup here misses, and every [oauth2clients.Client.Admits] check the
// seams exist to make is skipped — silently, for every request, with a login
// page that works.
//
// Treating the miss as "nothing to check" is what makes that silent. Naming it
// makes the misconfiguration a failed authorization request with a sentinel in
// the logs, which is the failure a deployment can find. The cost in the wired
// case is nothing at all, because the branch cannot be taken.
//
// It is not mapped to a transport. Neither seam answers a consumer's handler:
// the authenticator's error fails the request through the authorization server's
// own protocol error, and the resolver's ends the attempt at the client's
// redirect URI. What a person sees is oauth2server's, and what an operator needs
// is this sentinel beside the client_id in the log line both seams record.
var ErrClientNotRegistered = platformerrors.New("oauth2 client is not in this registry")

// ErrScopelessSubject is an inner resolver that answered with a subject and an
// undecided registry to read it in.
//
// See [ScopedSubjectResolver] for why the scope comes back from the resolver
// rather than off the request. The zero tenancy.Scope is the absence of a
// decision rather than tenancy.Global(), which the type is built to keep apart,
// and this is the refusal that keeps them apart here: a resolver that forgot to
// name a registry must not have its silence read as the global one, because the
// global registry admits any subject and the omission would widen every check
// this package makes into no check at all.
//
// A resolver that means the global registry says so — tenancy.Global() — and is
// admitted.
var ErrScopelessSubject = platformerrors.New("subject resolver named no registry for the subject it resolved")

// ErrNilSignInService is [NewAuthenticator] built over no signin.Service.
//
// It exists because the borrowed sentinel pointed at the wrong argument. The
// other two nil checks in that constructor are oauth2clients' own — the
// registry store and the database client genuinely are the registry's, and a
// deployment told "nil oauth2 client store" has something in the argument list
// to go looking for. The sign-in service is not the registry's and there is no
// oauth2 client service in the constructor at all, so borrowing
// oauth2clients.ErrNilService sent whoever read it looking for a thing that was
// never asked for.
//
// The wording names the login form rather than stopping at "sign-in service",
// because authentication/signin/grpc has a sentinel for the same missing
// argument in front of a different door. Two sentinels worded identically carry
// the same cockroachdb mark and errors/grpc matches a decoded error by mark, so
// the seam each one guards has to be in the sentence.
//
// It is not mapped to a transport, for the reason [ErrClientNotRegistered]
// gives and one more: this is a wiring failure at construction, so nobody is
// holding a request when it happens.
var ErrNilSignInService = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
	"nil sign-in service behind the authorization server's login form")
