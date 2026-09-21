package grpc

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/waitlists"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc/codes"
)

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
// [callers.Principal] instead.
//
// # What implementations owe
//
// A nil error means permitted. [callers.ErrTargetNotPermitted] means refused.
// Any other error is a failure to decide — a database or a link store that
// would not answer — and reaches the client as codes.Internal, which is what
// keeps an unavailable dependency from reading as a refusal. The three are
// distinguished by errors.Is, so an implementation may wrap the sentinel with
// context of its own and still be refusing.
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
		caller callers.Principal,
		scope tenancy.Scope,
		listID, signupID string,
	) error

	// AuthorizeSubjectRead is asked before ListSignupsForSubject pages the
	// signups of the subject a request named.
	//
	// It exists because [PermissionReadSignups] cannot answer the question. That
	// grant covers four reads at once, and the sharpest of them —
	// [Server.GetSignupByContact] — is an oracle over every address in the
	// tenant, so a deployment grants it narrowly and correctly. The cost was
	// that the one safe read went with it: a member asking where they are in a
	// queue is asking about themselves, and there was no way to permit that
	// without also permitting them to name somebody else. A grant on the method
	// says this caller may make this kind of call; whose signups these are is
	// this method's, asked after the subject has been read and before any row
	// is.
	//
	// The caller is never nil here, unlike AuthorizeWithdrawal: this read is not
	// in [PublicMethods] and an anonymous request does not reach it.
	//
	// A refusal discloses nothing. It is decided before the read, so a subject
	// nobody has ever signed up is refused by exactly the rule one belonging to
	// somebody else is, and the client cannot tell the two apart.
	AuthorizeSubjectRead(
		ctx context.Context,
		caller callers.Principal,
		scope tenancy.Scope,
		subject waitlists.Subject,
	) error
}

// SignupAuthorizerFuncs adapts a closure per question to [SignupAuthorizer],
// for a consumer whose rules are closures over something they already hold.
//
// A nil field refuses. An unanswered question is not a permitted one, and a
// struct literal is where that omission is visible: a deployment that answers
// only Withdrawal has written down that it did not decide the read, in its own
// code, where a reviewer sees it.
//
// There is deliberately no one-closure adapter beside this. There was, and it
// satisfied the interface by refusing the half it could not carry — which made
// a consumer who used it and then called ListSignupsForSubject discover the
// refusal at runtime, from a type that looked complete. The finding that
// produced AuthorizeSubjectRead was itself somebody not noticing that one grant
// covered four reads, so a second way not to notice was the wrong thing to
// ship. Answering one question now means writing one field and leaving the
// other, which is the same amount of typing and says what it is.
type SignupAuthorizerFuncs struct {
	// Withdrawal answers AuthorizeWithdrawal.
	Withdrawal func(ctx context.Context, caller callers.Principal, scope tenancy.Scope, listID, signupID string) error

	// SubjectRead answers AuthorizeSubjectRead. The self-service rule is two
	// lines: permit when the subject names the caller, refuse otherwise.
	SubjectRead func(ctx context.Context, caller callers.Principal, scope tenancy.Scope, subject waitlists.Subject) error
}

var _ SignupAuthorizer = SignupAuthorizerFuncs{}

// AuthorizeWithdrawal calls Withdrawal, or refuses when it is nil.
func (f SignupAuthorizerFuncs) AuthorizeWithdrawal(
	ctx context.Context,
	caller callers.Principal,
	scope tenancy.Scope,
	listID, signupID string,
) error {
	if f.Withdrawal == nil {
		return callers.ErrTargetNotPermitted
	}

	return f.Withdrawal(ctx, caller, scope, listID, signupID)
}

// AuthorizeSubjectRead calls SubjectRead, or refuses when it is nil.
func (f SignupAuthorizerFuncs) AuthorizeSubjectRead(
	ctx context.Context,
	caller callers.Principal,
	scope tenancy.Scope,
	subject waitlists.Subject,
) error {
	if f.SubjectRead == nil {
		return callers.ErrTargetNotPermitted
	}

	return f.SubjectRead(ctx, caller, scope, subject)
}

// authorizeSubjectRead asks the seam and turns what it said into the status a
// client sees.
//
// NotFound rather than PermissionDenied, matching authorizeWithdrawal below and
// for the same reason: a refusal that said "permission denied" would confirm
// that the subject named is one this tenant knows about, which is the disclosure
// the grant on this method already exists to prevent.
func (s *Server) authorizeSubjectRead(ctx context.Context, req *request, subject waitlists.Subject) error {
	err := s.signups.AuthorizeSubjectRead(ctx, req.principal, req.scope, subject)
	if err == nil {
		return nil
	}

	if errors.Is(err, callers.ErrTargetNotPermitted) {
		return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
			codes.NotFound, "reading the signups of %s %q", subject.Type, subject.ID)
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
		codes.Internal, "authorizing the read of %s %q's signups", subject.Type, subject.ID)
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

	if errors.Is(err, callers.ErrTargetNotPermitted) {
		return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
			codes.NotFound, "withdrawing waitlist signup %q", signupID)
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
		codes.Internal, "authorizing the withdrawal of waitlist signup %q", signupID)
}
