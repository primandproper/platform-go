package grpc

import (
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
)

// Principal is who is calling, as this service needs them: a user identifier
// and the tenant their request is against.
//
// It is an alias to identity/grpc's rather than a second interface, because a
// deployment has one authentication interceptor and one notion of a caller. Two
// interfaces of the same shape would mean a consumer writing one adapter per
// service that happens to need the same facts.
//
// This surface reads both halves it uses, and the user identifier is the one
// that matters most here. It is what [Server.CreateComment] writes into
// Comment.Author, so "who said this" is answered by the connection rather than
// by a request field somebody could put anybody's name in, and it is what an
// edit, an archive and a by-author page are compared against before the caller
// is asked to justify reaching somebody else's words.
type Principal = identitygrpc.Principal

// PrincipalExtractor reads the caller off the context.
//
// The consumer's own authentication interceptor puts a concrete principal
// there; this package defines no session type and never will. It is the same
// alias Principal is, so one extractor serves the directory, sign-in and this.
type PrincipalExtractor = identitygrpc.PrincipalExtractor
