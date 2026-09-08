/*
Package grpc serves the administered OAuth2 client registry over gRPC.

It is the transport and not the policy, which is the bargain
authentication/oauth2clients states one layer down and identity/grpc states in
the same words: who is calling is an interface a consumer's own authentication
interceptor satisfies, and what each method requires is a default map a consumer
composes into their own.

	srv, _ := oauth2clientsgrpc.NewServer(svc, store, db, extractPrincipal)
	// []grpcserver.RegistrationFunc{srv.RegisterOn}

# Four RPCs, all of them permissioned

Create, get, list and archive, each acting on any registration in the caller's
registry and each behind a grant. There is no method here a caller reaches
without one, which is the property [Permissions] and [Require] are shaped around:
every method this service declares is in that map.

An earlier revision served ten. The other six — a revision method, and a
self-service mirror of all five operations reachable behind no permission at all
— answered no caller in any consumer, and the permissionless five were the
surface in this package with the sharpest threat model. They were removed before
anything consumed the package. The .proto carries the finding and, if a
self-service half is ever wanted, the shape it has to take.

What survives the removal is the arrangement rather than the transport: a
registration still carries an owner, [oauth2clients.Client.Admits] still refuses
one person's credential to another, and oauth2clients.Store still lists by owner.
A deployment that wants those over gRPC writes that surface itself.

# What comes off the principal, and what a request may say

The registry, always, and it is never read off a request field: a scope a client
could name is a cross-tenant read hiding behind one. Nor is an owner, which no
request here carries at all — one a client could name is a credential minted in
somebody else's name. So the requests here name a row id and nothing about whose
it is, and there is no ScopeResolver on this surface — unlike
authentication/signin/grpc, where the caller has not become a principal yet.

# Errors

This package registers nothing. oauth2clients.HTTPMapper and
oauth2clients.GRPCMapper live beside the sentinels they map, and installing them
is the composition root's one call — errormappers.Register, which service.Register
makes for a service built from a service.Config and a service assembled by hand
makes itself.

Every failure here is one grpcerrors.PrepareAndLogGRPCStatus with codes.Internal
as the *default*. The encoding interceptor re-runs the registered mappers over
the preserved chain, so the mapper wins over the guess made at the call site,
which is why no handler on this surface switches on a sentinel.
*/
package grpc

//platform:transport resource surface: an administered OAuth2 client registry — over `oauth2clients.Service` and `oauth2clients.Store`
