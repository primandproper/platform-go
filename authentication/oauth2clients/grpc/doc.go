/*
Package grpc serves the administered OAuth2 client registry over gRPC.

It is the transport and not the policy, which is the bargain
authentication/oauth2clients states one layer down and identity/grpc states in
the same words: who is calling is an interface a consumer's own authentication
interceptor satisfies, and what each method requires is a default map a consumer
composes into their own.

	srv, _ := oauth2clientsgrpc.NewServer(svc, store, db, extractPrincipal)
	// []grpcserver.RegistrationFunc{srv.RegisterOn}

# Ten RPCs, two halves

Five administered methods act on any registration in the caller's registry, each
behind a permission. Five self-service methods act only on registrations the
caller owns, and require none — owning the row is the authorization.

They are mirrored methods rather than one set with an ownership field because a
consumer's interceptor gates an RPC by its full method name, before the request
body is parsed. A method whose required grant depended on a field would be one
the enforcer could not gate: it would have to be declared public and gate itself,
which puts an authorization decision somewhere nobody auditing the permission map
can see it. The .proto carries the long form.

# What comes off the principal, and what a request may say

The registry, always, and on the self-service half the owner too. Neither is ever
read off a request field: a scope a client could name is a cross-tenant read
hiding behind one, and an owner a client could name is a credential minted in
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

//platform:transport resource surface: an administered OAuth2 client registry and its self-service mirror — over `oauth2clients.Service` and `oauth2clients.Store`
