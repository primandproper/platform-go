package grpc

import (
	"context"
	"errors"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/settings"

	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"

	"google.golang.org/grpc/codes"
)

// ErrTargetNotPermitted indicates a caller who may make this call and may not
// make it about the subject this request named.
//
// It is identity/grpc's sentinel rather than a second one of this package's
// own, and deliberately the same value: a consumer has one rule about which
// principals a caller has standing over, and an authorizer written for the
// directory refuses a settings read with the answer it already returns. An
// errors.Is against either name matches.
//
// It is never registered as a client-safe sentinel. Every RPC here answers it
// with codes.PermissionDenied at the call site, so it needs no mapper, and its
// text is about the caller rather than about anything they can correct.
var ErrTargetNotPermitted = identitygrpc.ErrTargetNotPermitted

// SubjectAuthorizer decides whether the caller may act on the subject a request
// named.
//
// # Why it exists
//
// Six of this service's thirteen RPCs take a [settings.Subject] from the
// request body — set, get, clear, list, resolve and resolve-all — and the
// permission fragment in front of them is a grant on the method. A holder of
// settings.values.write may call SetValue, and nothing in a per-method check
// says whose settings. Within one tenant that would make "may change their own
// notification preferences" into "may change anybody's", which is the finding
// the directory paid for first and the reason identity/grpc grew a
// TargetAuthorizer: a grant on the method is one half of authorization and the
// row is the other.
//
// The consumer's authorization interceptor cannot answer it. It holds the
// request and the caller's grants, before the body is parsed, and no handle to
// resolve a principal with — so the question is asked from inside the handler,
// after the request has been found well formed and before anything reads or
// writes a row.
//
// It is not tenancy. The scope comes off the [Principal] and every store
// statement binds it, so nothing here crosses a deployment; this is the check
// inside one.
//
// # Why there is no default
//
// identity/grpc ships MembershipAuthorizer as its default and this ships none,
// because the two are not in the same position. That package owns the
// membership table and can answer its own question; this one is handed a
// subject type it may never have heard of. settings.SubjectType is a bare
// string with two suggested constants precisely so that a deployment whose
// settings hang off a device, a workspace or an API client can say so, and a
// default here would be a rule about an open vocabulary.
//
// Every available default is wrong in a way nothing reports. One that permits
// everything hands one person's preferences to another. One that permits only
// SubjectUser matching the caller is right for one member of an open set and
// silently closed for the rest, including SubjectAccount, which every
// administrative settings screen writes. One that compares the account in the
// request against Principal.ActiveAccountID compares a request field against a
// request field, which is the reading MembershipAuthorizer's own documentation
// rejects.
//
// So it is positional and required, exactly as the principal extractor is, and
// [ErrNilSubjectAuthorizer] is what a server built without one is. The
// self-service rule is two lines with [SubjectAuthorizerFunc]:
//
//	settingsgrpc.SubjectAuthorizerFunc(func(_ context.Context, caller settingsgrpc.Principal, subject settings.Subject) error {
//		if subject.Type == settings.SubjectUser && subject.ID == caller.UserID() {
//			return nil
//		}
//
//		return settingsgrpc.ErrTargetNotPermitted
//	})
//
// and a deployment that also administers account-wide settings adds the arm
// that asks its own membership table, which is the same handle
// identity/grpc's MembershipAuthorizer reads.
//
// # What implementations owe
//
// A nil error means permitted. [ErrTargetNotPermitted] means refused, and
// reaches the client as codes.PermissionDenied. Any other error is a failure to
// decide — a database that would not answer — and reaches the client as
// codes.Internal, which is what keeps an unavailable store from reading as a
// refusal. The three are distinguished by errors.Is, so an implementation may
// wrap the sentinel with context of its own and still be refusing.
//
// It is asked before the subject has been validated against anything, so an
// implementation is handed whatever the request carried, the empty subject
// included. Refusing that one is free: a subject naming no type and no id is
// nobody's, and settings.Subject.Validate refuses it a moment later either way.
type SubjectAuthorizer interface {
	// AuthorizeSubject is asked before an RPC reads or writes the settings of
	// the subject a request named.
	AuthorizeSubject(ctx context.Context, caller Principal, subject settings.Subject) error
}

// SubjectAuthorizerFunc adapts a function to [SubjectAuthorizer], for a
// consumer whose rule is one closure over something they already hold.
type SubjectAuthorizerFunc func(ctx context.Context, caller Principal, subject settings.Subject) error

var _ SubjectAuthorizer = SubjectAuthorizerFunc(nil)

// AuthorizeSubject calls f.
func (f SubjectAuthorizerFunc) AuthorizeSubject(
	ctx context.Context,
	caller Principal,
	subject settings.Subject,
) error {
	return f(ctx, caller, subject)
}

// authorizeSubject asks the consumer's rule and turns what it said into the
// status a client sees.
//
// It is a helper rather than five lines in each of six methods because the code
// is the part that can be got wrong quietly, and there are two of them. A
// refusal is codes.PermissionDenied. Anything else is an authorizer that could
// not decide, and it is codes.Internal, because a refusal is a sentence about
// the caller and an outage is not: reporting the second as the first tells a
// consumer to widen their policy while their database is down, and leaves a
// dashboard counting server faults reading zero through it.
//
// The caller learns nothing from a refusal about whether the subject exists.
// The check happens before any read, and a subject nobody has ever stored a
// value for is refused by exactly the same rule as one that belongs to somebody
// else.
func (s *Server) authorizeSubject(ctx context.Context, req *request, subject settings.Subject) error {
	err := s.subjects.AuthorizeSubject(ctx, req.caller, subject)
	if err == nil {
		return nil
	}

	code := codes.Internal
	if errors.Is(err, ErrTargetNotPermitted) {
		code = codes.PermissionDenied
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), code,
		"authorizing the caller against %s %q", subject.Type, subject.ID)
}
