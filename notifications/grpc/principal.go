package grpc

import (
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
)

// Principal is who is calling, as this service needs them: a user identifier
// and the directory their request is against.
//
// It is an alias to identity/grpc's rather than a second interface, because a
// deployment has one authentication interceptor and one notion of a caller. Two
// interfaces of the same shape would mean a consumer writing one adapter per
// service that happens to need the same facts.
//
// Both of the facts this surface reads off it are load-bearing, and the second
// one more than anywhere else in the module. Scope decides which directory; the
// user identifier decides whose inbox, and it is the whole of the row-level
// authorization on this surface — see the package documentation.
type Principal = identitygrpc.Principal

// PrincipalExtractor reads the caller off the context.
//
// The consumer's own authentication interceptor puts a concrete principal
// there; this package defines no session type and never will. It is the same
// alias Principal is, so one extractor serves the directory, sign-in and this
// service.
type PrincipalExtractor = identitygrpc.PrincipalExtractor
