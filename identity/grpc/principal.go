package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/tenancy"
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
// read this service performs — a user, their memberships, and the account a
// request is against — and it is an answer, where this is the question. The
// server resolves one from the other in GetPrincipal.
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

// PrincipalExtractor resolves a Principal off a request context, reporting
// whether there was one.
//
// The consumer supplies it, and the false return is the honest answer for an
// unauthenticated call rather than an error type this package would have to
// define. Every RPC on this service needs one — there is no anonymous read
// here, because a read with no principal has no scope to filter on — so a false
// is codes.Unauthenticated and the RPC stops.
//
// This mirrors authorization/grpc's GrantsExtractor and idempotency/grpc's
// WithPrincipalExtractor: platform names the shape, the consumer names the type.
type PrincipalExtractor func(ctx context.Context) (Principal, bool)
