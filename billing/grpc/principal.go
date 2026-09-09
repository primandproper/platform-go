package grpc

import (
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
)

// Principal is who is calling, as this service needs them: a user identifier
// and the scope their request is against.
//
// It is an alias to identity/grpc's rather than a second interface, because a
// deployment has one authentication interceptor and one notion of a caller. Two
// interfaces of the same shape would mean a consumer writing one adapter per
// service that happens to need the same three facts.
type Principal = identitygrpc.Principal

// PrincipalExtractor reads the caller off the context.
//
// The consumer's own authentication interceptor puts a concrete principal
// there; this package defines no session type and never will. It is the same
// alias [Principal] is, so one extractor serves the directory, sign-in, the
// client registry and this ledger.
type PrincipalExtractor = identitygrpc.PrincipalExtractor
