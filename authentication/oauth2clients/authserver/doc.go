/*
Package authserver joins an administered client registry to the OAuth 2.1
authorization server in authentication/oauth2server.

There are three seams here and they are one decision, which is why they share a
package rather than being three subpackages of one each. Between them they say:
the authorization server's clients come from the registry, and the person it
authorizes has to be one that registry's row admits.

	store, _ := authserver.NewStore(base, registry, db)
	auth, _  := authserver.NewAuthenticator(signIn, registry, db)

	// Only where the deployment has sessions of its own. The inner resolver is
	// the consumer's, and it reports the registry it authenticated the session
	// in alongside the subject — see [ScopedSubjectResolver].
	guard, _ := authserver.NewGuardedResolver(sessions, registry, db)

	srv, _ := oauth2server.NewServer(issuer, store, auth,
	    oauth2server.WithSubjectResolver(guard),
	    oauth2server.WithDynamicRegistration(false))

# Why all three, and not just the store

[NewStore] is the obvious half: an oauth2server.Store whose client lookup reads
this registry. On its own it is not enough, and the gap is not obvious.

oauth2server.Store.GetClient takes no tenancy.Scope — it cannot, because
/authorize names a client before anybody has signed in — so resolving a
registration cannot be an authorization decision. Something later has to compare
the registration against the person, and the only places holding both facts are
the two login seams: the request they are handed carries client_id, and the
subject is what they have just produced. That is
oauth2clients.Client.Admits, and [NewAuthenticator] and [NewGuardedResolver] are
the two places it is called.

[NewGuardedResolver] exists because leaving it out would be a bypass rather than
an omission. oauth2server consults a SubjectResolver *before* the login form,
and short-circuits on a non-nil subject — so a deployment that wires its own
resolver for already-signed-in users, and relies on the authenticator for the
check, has a path to an authorization code that never met the check. Wrapping
the consumer's resolver is what closes it.

Wiring the seams *without* [NewStore] is the third arrangement, and it is the one
that used to fail silently: the protocol half would resolve its clients from one
table while the seams looked for them in another, so every lookup here would miss
and every check would be skipped on every request, behind a login page that
worked. A miss is [ErrClientNotRegistered] now, which cannot be reached at all
when the store is wired — the authorization server resolves the same client_id
through [Store.GetClient] before either seam is asked anything.

# Where each seam's registry comes from

They differ, and the difference is not a style choice.

[Authenticator] resolves a scope off the request through a [ScopeResolver] and
hands it to signin, which checks the credentials *within* it. The scope that
reaches oauth2clients.Client.Admits is therefore one the person just proved
membership of, and a request naming a registry its user is not in fails to sign
in before the registration is ever read.

[GuardedResolver] has no such step, so it does not take a ScopeResolver at all.
Its subject comes out of a session the consumer minted, and a scope read off the
request would be a second fact nothing tied to the first — somebody holding a
valid session in one registry could name another in the request and be admitted
to its administered clients, which admit any subject in them. The scope comes
back from the consumer's resolver instead, beside the subject, which is what
binds the two. See [ScopedSubjectResolver].

# The asymmetry between the two login seams

They answer a refused client differently, and it is not an inconsistency.

The authenticator returns an *oauth2server.LoginError, which re-renders the form
with a message a person can act on. That is right: whoever hit it has just
proven a password, so telling them this application is not registered for their
organization discloses nothing and is the only thing that helps.

The guarded resolver answers (nil, nil) — "not one of mine" — and lets the
request fall through to the form. It does *not* return an error, and that is
forced by oauth2server: Server.resolveSubject turns any resolver error into a
server_error redirect with no form rendered, so a resolver that reported the
mismatch would replace an actionable page with an opaque failure. Falling
through means the person meets the form, signs in, and gets the written answer
from the authenticator instead.

# What this package does not decide

Who may register a client, and in which arrangement — that is
authentication/oauth2clients/grpc's, in front of the store.

Whether /register is served. It should not be, in a deployment using this
registry, and the way to say so is oauth2server.WithDynamicRegistration(false),
which takes the endpoint off the router and out of the discovery document in one
call. [Store.CreateClient] and [Store.DeleteClient] refuse as well, but that is
defense in depth rather than the control: an endpoint that is not routed is a
better answer than one that is routed and refuses.
*/
package authserver
