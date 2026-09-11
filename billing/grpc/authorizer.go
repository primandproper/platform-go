package grpc

import (
	"context"
	"errors"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc/codes"
)

// ErrTargetNotPermitted indicates a caller who may make this call and may not
// make it against the account this request named or this row belongs to.
//
// It is identity/grpc's sentinel rather than a second one of this package's own,
// and deliberately the same value: a consumer has one rule about which accounts
// a caller has standing in, and an authorizer written for the directory refuses
// the ledger with the answer it already returns. An errors.Is against either
// name matches.
//
// It is never registered as a client-safe sentinel, on either surface. Its text
// says the caller was refused, and half of what this package does with it is
// answer as though the row were absent — see the two refusal shapes at the
// bottom of this file.
var ErrTargetNotPermitted = identitygrpc.ErrTargetNotPermitted

// AccountAuthorizer decides whether the caller may act on the account a request
// named, or on the account a row belongs to.
//
// # Why it exists
//
// Seven of this service's RPCs are somebody's own money. Four name an account in
// the request — the per-account subscription, current-subscription, purchase and
// ledger pages — and three name a row that belongs to one. The permission
// fragment in front of them is a grant on the method: a holder of
// billing.transactions.read may call ListTransactionsForAccount, and nothing in
// a per-method check says whose. That is the whole of the reason this seam
// exists, and it is the finding the directory paid for first — a grant on the
// method made a member's management right directory-wide until something asked
// the second question. Here the rows are money.
//
// The consumer's authorization interceptor cannot answer it. It holds the
// request and the caller's grants, before the body is parsed, and has no handle
// to read a membership with — so the question is asked from inside the handler,
// where the store and the client already are.
//
// It is not tenancy. The scope comes off the [Principal] and every store read
// filters on it, so nothing here crosses a deployment; this is the check inside
// one.
//
// # Why there is no default
//
// identity/grpc ships MembershipAuthorizer as its default and this ships none,
// because the two are not in the same position. That package owns the membership
// table and can answer the question itself; this one has no idea what makes an
// account somebody's, and every default available to it is wrong in a way
// nothing reports. One that permits everything hands one customer's ledger to
// another. One that refuses everything makes six RPCs answer as though nothing
// existed, which is discovered as a mystery rather than as a wiring failure. One
// that compares the account id in the request against Principal.ActiveAccountID
// compares a request field against a request field, which is the reading
// MembershipAuthorizer's own documentation rejects.
//
// So it is positional and required, exactly as the principal extractor is, and
// [ErrNilAccountAuthorizer] is what a server built without one is.
//
// A consumer already running identity/grpc passes its MembershipAuthorizer
// straight in — the method set matches, [Principal] is the same alias, and
// [ErrTargetNotPermitted] is the same value — so the common case is one argument
// rather than an implementation.
//
// # What implementations owe
//
// A nil error means permitted. [ErrTargetNotPermitted] means refused. Any other
// error is a failure to decide — a database that would not answer — and reaches
// the client as codes.Internal, which is what keeps an unavailable store from
// reading as a refusal. The three are distinguished by errors.Is, so an
// implementation may wrap the sentinel with context of its own and still be
// refusing.
type AccountAuthorizer interface {
	// AuthorizeAccount is asked before an RPC reads an account's own rows, and
	// after a keyed read has resolved which account a row belongs to.
	AuthorizeAccount(ctx context.Context, caller Principal, accountID string) error
}

// AccountAuthorizerFunc adapts a function to [AccountAuthorizer], for a consumer
// whose rule is one closure over something they already hold.
type AccountAuthorizerFunc func(ctx context.Context, caller Principal, accountID string) error

var _ AccountAuthorizer = AccountAuthorizerFunc(nil)

// AuthorizeAccount calls f.
func (f AccountAuthorizerFunc) AuthorizeAccount(ctx context.Context, caller Principal, accountID string) error {
	return f(ctx, caller, accountID)
}

// The two ways this surface asks the question, and the one thing that differs
// between them: what a refusal looks like to the client.
//
// They are helpers rather than five lines per method because the code is the
// part that can be got wrong quietly, and there are two of them.

// authorizeNamedAccount gates an RPC whose request named the account, and
// answers a refusal with codes.PermissionDenied.
//
// The caller learns nothing from it: an account they have no standing in and an
// account nobody has are both refused by the same rule and answered the same
// way, so the code cannot be used to tell which account ids exist. That is
// identity/grpc's reading of the same question, and it holds here because the
// check happens before any read.
func (s *Server) authorizeNamedAccount(
	ctx context.Context,
	req *request,
	accountID string,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	return s.authorize(ctx, req, accountID, codes.PermissionDenied, descriptionFmt, descriptionArgs...)
}

// authorizeRowOwner gates an RPC that read a row and then asked whose it is, and
// answers a refusal with codes.NotFound.
//
// This is where this surface diverges from identity/grpc, and the divergence is
// the point rather than an inconsistency. There the question is asked ahead of
// the read, so a refusal is all the server can say. Here the read has already
// happened, and answering PermissionDenied would tell a caller walking
// transaction ids exactly which of them are real — the absent row says NotFound
// and the forbidden one would say something else. So both say NotFound, and a
// ledger id is worth no more to somebody guessing than it was before they sent
// it.
//
// The chain returned is still the refusal, which is what the log and the span
// record. Only the status the client reads is the absence, and
// [ErrTargetNotPermitted] is not client-safe, so its wording does not travel
// with it.
func (s *Server) authorizeRowOwner(
	ctx context.Context,
	req *request,
	accountID string,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	return s.authorize(ctx, req, accountID, codes.NotFound, descriptionFmt, descriptionArgs...)
}

// authorize asks the seam and turns what it said into a status: the refusal code
// its caller chose, and codes.Internal for an authorizer that could not decide.
//
// The second is not a detail. A refusal is a sentence about the caller and an
// outage is not; reporting the second as the first tells a consumer to widen
// their policy while their database is down, and leaves a dashboard counting
// server faults reading zero through it.
func (s *Server) authorize(
	ctx context.Context,
	req *request,
	accountID string,
	refused codes.Code,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	err := s.targets.AuthorizeAccount(ctx, req.principal, accountID)
	if err == nil {
		return nil
	}

	code := codes.Internal
	if errors.Is(err, ErrTargetNotPermitted) {
		code = refused
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), code,
		descriptionFmt, descriptionArgs...)
}
