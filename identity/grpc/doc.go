/*
Package grpc is identity on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/identity]'s Service and Store, the
converters between the generated messages and its types, a typed client, and the
default permission fragment a consumer composes into its policy.

It is imported as identitygrpc.

# Why this module ships a transport at all

The module README's "Transports" section drew a line: a component that owns data
ships a store and stops, because a library that shipped /api/v1/users would be
versioning your API on its own cadence, in types your proto does not have, under
a scoping rule it guessed. This package is on the far side of that line, and it
is there because all three of those became false rather than because the line
was wrong.

The cadence is the domain tier's own, now that the primitives are leaving for
primitives-go — nothing else rides on a release this package is in. The types are
shipped: identity.proto is in this module, generated into identitypb here and
into a consumer's Swift, TypeScript and Kotlin from the same file, exactly as
filtering.proto already works. And the scope is not guessed, because
tenancy.Scope exists and this package binds it off the caller rather than off a
request field.

What stays the consumer's is what was always genuinely theirs, and each is a
seam here rather than a decision: who is calling ([Principal]), what each method
requires ([Permissions], declared in one call by [Require]), what else happens on
a write (identity.Hooks, inside the transaction), and whatever columns are their
own.

[github.com/primandproper/platform-go/v14/identity/config] assembles all three
layers from environment configuration and registers them with an injector, which
is the shorter of the two mounts below.

# The shape

Twenty-eight RPCs. Fifteen writes, each exactly one call into identity.Service,
which is one transaction with the consumer's hooks inside it. Thirteen reads on
identity.Store, on the client's reader — twelve of them one call, and the one
that lists what the caller has been sent reading the caller's row first for the
address it will not take from the request. No method here orchestrates
anything: it converts, calls one thing, and converts back. Anything that had to
happen atomically happened a layer down, where the transaction is.

# Mounting it

	srv, err := identitycfg.NewServer(ctx, cfg, client, svc, store, principalFromContext,
		identitycfg.WithPillars(pillars))

	reqs, err := identitygrpc.Require(authzgrpc.NewRequirements()).Build()

	errormappers.Register()   // or service.Register, which makes this call

	grpcServer, err := grpcserver.NewGRPCServer(ctx, cfg,
		[]grpc.UnaryServerInterceptor{
			authn,                                  // yours: puts the Principal on the context
			enforcer.UnaryServerInterceptor(),      // authorization/grpc, over the requirements above
			grpcerrors.UnaryErrorEncodingInterceptor(),
		},
		nil,
		[]grpcserver.RegistrationFunc{srv.RegisterOn},
	)

The errormappers.Register call is not optional and is not made here. Without it
every sentinel this service returns arrives as codes.Unknown — a taken username
included — because the mapping lives beside the sentinels in identity and
nothing installs itself into a process-wide registry by being linked in. See
[github.com/primandproper/platform-go/v14/errormappers].

# What is absent

No credential RPCs, no password on a registration, no avatar, and no scope on
any message. Each absence is deliberate and the reason is in identity.proto's
own documentation, which is the file a consumer generating a client in another
language actually reads.
*/
package grpc

//platform:transport resource surface: the four nouns and their lifecycle — over `identity.Service` and `identity.Store`
