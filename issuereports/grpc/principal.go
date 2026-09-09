package grpc

import (
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
)

// Principal is who is calling, as this service needs them: a user identifier and
// the tenant their request is against.
//
// It is an alias to identity/grpc's rather than a second interface, because a
// deployment has one authentication interceptor and one notion of a caller. Two
// interfaces of the same shape would mean a consumer writing one adapter per
// service that happens to need the same facts.
//
// The identifier is what a report is filed under. It is the same string
// issuereports.Report.Reporter holds and the same one dataprivacy erases by, so
// a deployment whose principal is not the subject of its own privacy requests
// has a mismatch to fix in its interceptor rather than a conversion to write
// here.
type Principal = identitygrpc.Principal

// PrincipalExtractor reads the caller off the context.
//
// The consumer's own authentication interceptor puts a concrete principal there;
// this package defines no session type and never will. It is the same alias
// Principal is, so one extractor serves the directory, sign-in and this queue.
type PrincipalExtractor = identitygrpc.PrincipalExtractor
