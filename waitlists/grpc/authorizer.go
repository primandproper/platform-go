package grpc

import (
	"context"
	"errors"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc/codes"
)

// ErrTargetNotPermitted indicates a caller who may make this call and may not
// make it against the signup this request named.
//
// It is identity/grpc's sentinel rather than a second one of this package's own,
// and deliberately the same value: a consumer has one rule about which rows a
// caller has standing in, and an authorizer written for the directory refuses a
// withdrawal with the answer it already returns. An errors.Is against either
// name matches.
//
// It is never registered as a client-safe sentinel. Its text says the caller was
// refused, and what this surface does with it is answer as though nothing had
// been named — see [Server.authorizeWithdrawal].
var ErrTargetNotPermitted = identitygrpc.ErrTargetNotPermitted

// SignupAuthorizer decides whether whoever is calling may withdraw the signup a
// request named.
//
// # Why it exists
//
// Withdraw is one of the three RPCs on this service that a caller reaches
// without a grant, and it is the only one of the three that names a row. That
// combination is the whole reason this seam is here.
//
// A grant would not have answered it. identity/grpc paid for that finding first
// — a permission on the method said whether this kind of call was allowed at
// all and nothing about whose account — and the version of it here is sharper,
// because the caller frequently holds no grants at all. The person clicking
// unsubscribe in an email has not signed in and, on a pre-launch list, has
// nothing to sign in to.
//
// Nor does the identifier answer it. waitlists mints a signup's id itself and
// the store hands it back from Join; it is a row identifier and not a
// credential, and treating it as one would make "unsubscribe anybody" a matter
// of holding an id that a page has already shown to somebody.
//
// So the question is asked here, from inside the handler, after the request has
// been found well formed and before anything is written. That is the same place
// identity/grpc asks its own, and for the same reason: the consumer's
// authorization interceptor holds the request and the caller's grants, before
// the body is parsed, and has no handle to read a row or redeem a token with.
//
// # Why there is no default
//
// identity/grpc ships MembershipAuthorizer as its default and this ships none,
// because the two are not in the same position. That package owns the membership
// table and can answer its own question; this one has no idea what a consumer
// accepts as proof that the person asking is the person who signed up, and every
// default available to it is wrong in a way nothing reports. One that permits
// everything lets anybody unsubscribe anybody who is on a list. One that refuses
// everything makes the unsubscribe link in every email a dead end, which is
// discovered by somebody who wanted to leave and could not. One that compares the
// signup's contact against a field in the request compares a request field
// against a request field, which is the reading MembershipAuthorizer's own
// documentation rejects.
//
// So it is positional and required, exactly as the principal extractor is, and
// [ErrNilSignupAuthorizer] is what a server built without one is.
//
// # What the usual answer is
//
// An action link. github.com/primandproper/platform-go/v14/links mints a
// single-use, expiring token against a subject, and an unsubscribe URL carries
// one; the consumer's own interceptor redeems it and puts what it named on the
// context, and this authorizer compares that against the signup the request
// names. That is one implementation and this package ships none of it, because
// how a person is asked to prove they are themselves is the consumer's, and a
// deployment whose unsubscribe page sits behind a sign-in answers from
// [Principal] instead.
//
// # What implementations owe
//
// A nil error means permitted. [ErrTargetNotPermitted] means refused. Any other
// error is a failure to decide — a database or a link store that would not
// answer — and reaches the client as codes.Internal, which is what keeps an
// unavailable dependency from reading as a refusal. The three are distinguished
// by errors.Is, so an implementation may wrap the sentinel with context of its
// own and still be refusing.
type SignupAuthorizer interface {
	// AuthorizeWithdrawal is asked before Withdraw moves the signup a request
	// named.
	//
	// The caller is nil for an anonymous request, which is the ordinary case
	// and not an error: it is what an unsubscribe link looks like. The scope is
	// the tenant the request resolved to, so an implementation that reads the
	// row has what the read needs; the identifiers are the request's, unread,
	// because whether the row exists is this method's to find out if its rule
	// needs to.
	AuthorizeWithdrawal(
		ctx context.Context,
		caller Principal,
		scope tenancy.Scope,
		listID, signupID string,
	) error
}

// SignupAuthorizerFunc adapts a function to [SignupAuthorizer], for a consumer
// whose rule is one closure over something they already hold.
type SignupAuthorizerFunc func(
	ctx context.Context,
	caller Principal,
	scope tenancy.Scope,
	listID, signupID string,
) error

var _ SignupAuthorizer = SignupAuthorizerFunc(nil)

// AuthorizeWithdrawal calls f.
func (f SignupAuthorizerFunc) AuthorizeWithdrawal(
	ctx context.Context,
	caller Principal,
	scope tenancy.Scope,
	listID, signupID string,
) error {
	return f(ctx, caller, scope, listID, signupID)
}

// authorizeWithdrawal asks the seam and turns what it said into the status a
// client sees.
//
// A refusal is answered as codes.NotFound with the words this package's own
// absence carries, and that is deliberate rather than a rounding of
// PermissionDenied. The caller here is frequently anonymous and holds an
// identifier somebody handed them; a PermissionDenied on a signup that is not
// theirs and a NotFound on one that does not exist would be two answers a caller
// walking identifiers could tell apart, which is precisely the enumeration this
// service refuses to be. billing/grpc makes the same distinction between its two
// refusal shapes, and this surface only has the one.
//
// Anything else is an authorizer that could not decide, and that is
// codes.Internal — because a refusal is a sentence about the caller and an
// outage is not. Reporting the second as the first tells a consumer their link
// store is fine while it is down, and leaves a dashboard counting server faults
// reading zero through it.
func (s *Server) authorizeWithdrawal(
	ctx context.Context,
	req *request,
	listID, signupID string,
) error {
	err := s.signups.AuthorizeWithdrawal(ctx, req.principal, req.scope, listID, signupID)
	if err == nil {
		return nil
	}

	if errors.Is(err, ErrTargetNotPermitted) {
		return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
			codes.NotFound, "withdrawing waitlist signup %q", signupID)
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
		codes.Internal, "authorizing the withdrawal of waitlist signup %q", signupID)
}
