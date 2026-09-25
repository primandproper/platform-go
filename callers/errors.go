package callers

import (
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// ErrTargetNotPermitted indicates a caller who may make this call and may not
// make it against the row, the account or the person this request named.
//
// It is the answer to the half of authorization a per-method permission cannot
// reach. primitives-go's authorization/grpc decides from the full method name
// and the caller's grants, both of which it has before the request body is
// parsed; whether the identifier on that body names something this caller has
// any standing in is a different question, asked inside the handler where the
// store already is.
//
// It is one value for every surface rather than one per package, and that is
// why it is here rather than in any of them. A consumer has one rule about
// which rows a caller has standing in, so an authorizer written for the
// directory refuses a ledger, a comment, a report, a setting and a signup with
// the answer it already returns, and an errors.Is against it from any surface
// matches.
//
// It is a sentinel of its own rather than a wrap of a platform one, deliberately
// unmapped on either transport: every surface that raises it answers it with a
// status at the call site, so a mapper would be a second decision about a
// refusal whose code is already chosen. Wrapping a package's own
// not-found sentinel would be worse than useless — that one maps to a code that
// says the row is absent, so a refusal would reach a client as an absence and a
// caller walking identifiers could tell the two apart by watching which refusal
// said which.
//
// It is never registered as a client-safe sentinel, on any surface. Its text
// says the caller was refused, and several of the surfaces that raise it answer
// as though the row were absent instead — each says which of the two shapes it
// takes, and why, beside the handler that takes it.
var ErrTargetNotPermitted = platformerrors.New("the caller may not act on the named target")

// ErrNoPrincipal indicates a request that arrived with nobody on it, at a seam
// that needed somebody.
//
// It is the refusal behind every seam derived from a PrincipalExtractor rather
// than handed one — a scope, a subject, an owner, a caller identifier — which is
// how a composition root gives a surface the one fact it wants about a caller
// instead of the whole principal. Those surfaces answer a resolver's failure
// with a code of their own choosing, because a resolver the consumer wrote can
// fail for reasons that are the request's fault; a resolver that failed because
// there was no caller at all is a different answer, and it is the same answer
// everywhere: authenticate and ask again.
//
// So unlike ErrTargetNotPermitted it is mapped, Unauthenticated on gRPC and 401
// on HTTP, and the mapper outranks the code a surface falls back to. A derived
// seam wraps it; errors.Is from any surface matches.
var ErrNoPrincipal = platformerrors.New("the request carries no caller")
