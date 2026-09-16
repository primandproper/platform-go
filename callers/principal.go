package callers

import (
	"context"

	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Principal is who is calling, as the consumer's authentication interceptor put
// it on the request context.
//
// It is an interface with three methods and no constructor, because every
// concrete answer to "who is calling" is the consumer's: a session, a bearer
// token's claims, a service account, an impersonation. This package defines no
// session type and never will — the whole reason a directory can be a library
// is that it does not also decide how somebody proved they were themselves.
//
// It is deliberately not [github.com/primandproper/platform-go/v14/identity.Principal],
// which is a different thing with a confusingly similar name: that one is a
// read a service performs — a user, their memberships, and the account a
// request is against — and it is an answer, where this is the question.
// identity/grpc's GetPrincipal resolves one from the other.
//
// # The method set is final
//
// Three methods, and there will not be a fourth. Ten gRPC surfaces in this
// module name this type — identity/grpc, authentication/signin/grpc,
// authentication/oauth2clients/grpc, billing/grpc, comments/grpc,
// issuereports/grpc, notifications/grpc, settings/grpc, waitlists/grpc and
// webhooks/grpc — so a method added here is a method every consumer of every
// gRPC surface in this module has to grow at once, on whatever session type
// they already had. An interface carries no default, so there is no deprecation
// shape available: the break is total and it arrives at compile time in
// somebody else's repository. The three that are here are the three every one
// of those surfaces needs to do anything at all — who is calling, whose
// directory they are in, and which account the call is against.
//
// What the first surface to want a service account flag, a session id or an
// impersonation marker reaches for instead is an optional interface, declared
// where it is needed and type-asserted at the call site:
//
//	if s, ok := caller.(interface{ SessionID() string }); ok {
//		// the caller's session type answers this one; use it
//	}
//
// A consumer whose type already has the method satisfies it by having it, and
// one whose does not keeps compiling and takes the branch that does not need it
// — which is the part the base interface cannot offer, since every method it
// declares is one every implementation owes. The precedent is
// [github.com/primandproper/primitives-go/v2/notifications/async.ConnectionAcceptor]
// and the standard library's http.Flusher; neither is a method the interface
// beside it grew.
//
// This is not the ruling identity/grpc's TargetAuthorizer carries, and the two
// should not be collapsed. That one ships a default a consumer embeds, which is
// a different way of staying additive and is open to it because it has a
// default to embed. This has none and can have none: a principal is the
// consumer's own answer to who is calling, and there is nothing here to inherit
// from.
type Principal interface {
	// UserID is the calling user's identifier in this directory.
	UserID() string

	// Scope is whose directory they are in.
	//
	// It is on the principal rather than in a request message, and that is the
	// load-bearing decision in this package. A scope a client could name is a
	// cross-tenant read hiding behind a request field: the store's every read
	// filters on the scope it is handed, so handing it one the caller chose
	// makes the filter answer to the caller. An application with one directory
	// returns tenancy.Global here and behaves exactly as an unscoped one would.
	Scope() tenancy.Scope

	// ActiveAccountID is the account this request is against, or empty for a
	// caller who named none — in which case the reads that need one resolve the
	// user's default.
	ActiveAccountID() string
}

// PrincipalExtractor resolves a [Principal] off a request context, reporting
// whether there was one.
//
// The consumer supplies it, and the false return is the honest answer for an
// unauthenticated call rather than an error type this package would have to
// define. Almost every RPC in this module needs one — a read with no principal
// has no scope to filter on — so a false is codes.Unauthenticated and the RPC
// stops. The exceptions are declared rather than assumed: three of
// waitlists/grpc's RPCs are a signup page, and three of
// authentication/signin/grpc's are sign-in itself, and both packages say so.
//
// This mirrors primitives-go's authorization/grpc GrantsExtractor and
// idempotency/grpc WithPrincipalExtractor: platform names the shape, the
// consumer names the type.
type PrincipalExtractor func(ctx context.Context) (Principal, bool)
