package grpc

import (
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
)

// Principal is who is calling, as this service needs them: a user identifier,
// the account their request is against, and the scope whose catalog they are
// in.
//
// It is an alias to identity/grpc's rather than a second interface, because a
// deployment has one authentication interceptor and one notion of a caller. Two
// interfaces of the same shape would mean a consumer writing one adapter per
// service that happens to need the same three facts.
//
// It is handed whole to the [SubjectAuthorizer], rather than reduced to the one
// field this package would have picked. Whose settings a caller may reach is
// the consumer's rule, and a rule that needs the active account — an
// administrator editing the settings of the account they are signed into — must
// not have had that fact discarded on the way.
type Principal = identitygrpc.Principal

// PrincipalExtractor reads the caller off the context.
//
// The consumer's own authentication interceptor puts a concrete principal
// there; this package defines no session type and never will. It is the same
// alias Principal is, so one extractor serves the directory, sign-in and
// settings.
type PrincipalExtractor = identitygrpc.PrincipalExtractor
