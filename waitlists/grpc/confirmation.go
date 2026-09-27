package grpc

import (
	"context"
	"errors"
	"slices"

	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/waitlists"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc/codes"
)

// The two links the confirmation loop mints, by the action a links.Minter
// declares them under.
//
// They are this package's names rather than the consumer's because the handler
// that redeems one checks the action before it spends it: a confirmation link
// presented at Unsubscribe is refused unspent, and so is an unsubscribe link
// presented at Confirm. Where each points and how long it lives are the
// consumer's, declared on the minter the way every other action is:
//
//	links.WithAction(waitlistsgrpc.ConfirmAction, links.ActionPolicy{
//		URL: "https://example.com/waitlist/confirm/{token}",
//		TTL: links.Duration(72 * time.Hour),
//	})
//
// An unsubscribe link wants a long lifetime — it sits in somebody's inbox until
// the day they want off the list — and a confirmation link a short one, since
// an address that has not said yes in a few days is one nobody should keep
// waiting on.
const (
	ConfirmAction     links.Action = "waitlist_confirm"
	UnsubscribeAction links.Action = "waitlist_unsubscribe"
)

// The metadata a confirmation-loop link carries beside its subject, which is the
// signup's id.
//
// The list is here because a signup is addressed by its list as well as by its
// id, everywhere in waitlists. The scope is here so that a link minted in one
// tenant is refused on a connection placed in another, rather than redeemed
// against whichever tenant the connection resolved to: it is a comparison, and
// never the source of the scope the statement binds.
const (
	linkListKey  = "waitlist_id"
	linkScopeKey = "scope"
)

// The wiring failures WithConfirmation can be built into, and the one refusal
// both link RPCs give.
var (
	// ErrNilLinks is WithConfirmation given no minter.
	ErrNilLinks = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil links minter for waitlist confirmation")
	// ErrNilConfirmationMailer is WithConfirmation given no mailer.
	//
	// There is no default, for the reason there is no default authorizer: a
	// mailer that sent nothing would be a signup form whose every signup stays
	// pending forever, which is a list that looks like it is filling and never
	// does.
	ErrNilConfirmationMailer = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
		"nil waitlist confirmation mailer")
	// ErrConfirmationActionMissing is a minter that does not declare one of the
	// two actions the loop mints. It is refused at construction rather than at
	// the first signup, which would be a join that committed a pending row and
	// then could not mail anything for it.
	ErrConfirmationActionMissing = platformerrors.New("links minter does not declare a waitlist confirmation action")
	// ErrConfirmationNotConfigured is Confirm or Unsubscribe reached on a server
	// built without WithConfirmation. It is codes.Unimplemented, which is what
	// it is: this deployment mints no such links, so no such link exists.
	ErrConfirmationNotConfigured = platformerrors.New("waitlist confirmation links are not configured")
	// ErrInvalidLink is every way a confirmation or unsubscribe link fails to
	// do what it was minted for: a token nobody minted, one that expired, was
	// spent or was revoked, one minted for the other door or in another
	// tenant, and one whose signup has since moved where the link cannot take
	// it.
	//
	// They are one refusal, answered codes.NotFound, for the reason a refused
	// withdrawal reads as an absent signup: two answers a caller walking tokens
	// could tell apart are an oracle, and the person holding a real link loses
	// nothing by not being told which of these it was. What it was is on the
	// operation, where the deployment can read it.
	ErrInvalidLink = platformerrors.New("invalid waitlist link")
)

// Links is what the confirmation loop needs of an action-link minter: to check
// it can mint the two actions, to mint them, and to look at and spend one.
//
// *links.Minter is the implementation. It is an interface here only so that a
// test can stand in for the minter's store without a database behind it.
type Links interface {
	Actions() []links.Action
	Mint(ctx context.Context, action links.Action, subject links.Subject, opts ...links.MintOption) (*links.Link, error)
	Inspect(ctx context.Context, token links.Token) (*links.Claims, error)
	Redeem(ctx context.Context, token links.Token) (*links.Claims, error)
}

var _ Links = (*links.Minter)(nil)

// ConfirmationMail is what a pending signup's first message is built from.
type ConfirmationMail struct {
	// Signup is the pending signup, as stored, contact included — the address
	// the message goes to is Signup.Contact and nowhere else.
	Signup *waitlists.Signup

	// Confirm is the link that makes the signup a subscription. Deliver its
	// URL; do not log it.
	Confirm *links.Link

	// Unsubscribe is the link that takes the address off the list — the "this
	// was not me" in the message, which suppresses the address whether or not
	// the signup was ever confirmed. Deliver its URL; do not log it.
	Unsubscribe *links.Link
}

// ConfirmationMailer delivers the one message a pending signup is sent.
//
// It is the consumer's, as every send in this module is: waitlists owns a table
// and this package owns a transport, and neither owns a mail provider. What it is
// handed is enough to write the message and nothing it should keep — both links
// are live credentials, and the ids on them are what an audit entry records.
//
// It is called after the signup has committed, never inside the transaction
// that wrote it. A link mailed from inside that transaction is one that may be
// sent for a signup that then rolled back — a URL in somebody's inbox that will
// never confirm anything — and a mail cannot be taken back.
//
// An error is this service's failure rather than a fact about the address, and
// the Join that called it reports it as one. The signup it was for stays
// pending, and the next Join from the address sends a fresh message: an address
// whose signup is still pending is the one address a repeated Join mails. What a
// deployment wanting the send to survive a mail provider's outage writes here is
// an outbox row, and a worker of its own delivers it.
type ConfirmationMailer interface {
	SendConfirmation(ctx context.Context, scope tenancy.Scope, mail *ConfirmationMail) error
}

// WithConfirmation makes the public Join a double opt-in: a signup is written
// pending, a confirmation link and an unsubscribe link are minted through
// minter once it commits, and mailer is handed both. Confirm and Unsubscribe
// are where the two links land.
//
// Absent, Join writes a waiting signup and the two link RPCs answer
// codes.Unimplemented, which is what every deployment had before this option
// existed. Present, both arguments are required and the minter must declare
// ConfirmAction and UnsubscribeAction; NewServer refuses otherwise, because each
// gap is a form that commits signups nothing can ever confirm.
//
// Every Join is held for confirmation, a signed-in caller's included. A
// deployment that already vouches for its callers' addresses and wants their
// signups waiting at once joins them through waitlists.SignupStore.Join, which
// starts a signup waiting unless it is asked not to.
func WithConfirmation(minter Links, mailer ConfirmationMailer) Option {
	return func(s *Server) {
		s.confirming = true
		s.links = minter
		s.confirmations = mailer
	}
}

// MintUnsubscribeLink mints an unsubscribe link for one signup, for a message
// the consumer sends later — an invitation, a reminder, a launch announcement —
// that must carry a way off the list as surely as the confirmation did.
//
// It is the link Unsubscribe redeems, bound to the signup, its list and its
// tenant exactly as the confirmation mail's is. The minter is the one the server
// was built with; opts are passed through, so a message that wants a lifetime
// other than the action's declares it here.
func MintUnsubscribeLink(
	ctx context.Context,
	minter Links,
	scope tenancy.Scope,
	listID, signupID string,
	opts ...links.MintOption,
) (*links.Link, error) {
	if minter == nil {
		return nil, ErrNilLinks
	}

	return mintFor(ctx, minter, UnsubscribeAction, scope, listID, signupID, opts...)
}

// mintFor mints one of the loop's two links against a signup, carrying what the
// redemption compares.
func mintFor(
	ctx context.Context,
	minter Links,
	action links.Action,
	scope tenancy.Scope,
	listID, signupID string,
	opts ...links.MintOption,
) (*links.Link, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}

	// The metadata is appended last, so a caller's own WithMetadata cannot
	// replace the two keys the redemption depends on.
	opts = append(slices.Clone(opts), links.WithMetadata(map[string]string{
		linkListKey:  listID,
		linkScopeKey: scope.Owner(),
	}))

	return minter.Mint(ctx, action, links.Subject(signupID), opts...)
}

// validateConfirmation is NewServer's check of what WithConfirmation was given.
func (s *Server) validateConfirmation() error {
	if !s.confirming {
		return nil
	}

	if s.links == nil {
		return ErrNilLinks
	}

	if s.confirmations == nil {
		return ErrNilConfirmationMailer
	}

	declared := s.links.Actions()
	for _, action := range []links.Action{ConfirmAction, UnsubscribeAction} {
		if !slices.Contains(declared, action) {
			return platformerrors.Wrapf(ErrConfirmationActionMissing, "action %q", action)
		}
	}

	return nil
}

// sendConfirmation mints a pending signup's two links and hands them to the
// consumer's mailer. It runs after the transaction that wrote the signup has
// committed; see ConfirmationMailer.
func (s *Server) sendConfirmation(ctx context.Context, req *request, signup *waitlists.Signup) error {
	confirm, err := mintFor(ctx, s.links, ConfirmAction, req.scope, signup.ListID, signup.ID)
	if err != nil {
		return platformerrors.Wrap(err, "minting a waitlist confirmation link")
	}

	unsubscribe, err := mintFor(ctx, s.links, UnsubscribeAction, req.scope, signup.ListID, signup.ID)
	if err != nil {
		return platformerrors.Wrap(err, "minting a waitlist unsubscribe link")
	}

	req.op.Set(confirmLinkKey, string(confirm.ID)).Set(unsubscribeLinkKey, string(unsubscribe.ID))

	if err = s.confirmations.SendConfirmation(ctx, req.scope, &ConfirmationMail{
		Signup:      signup,
		Confirm:     confirm,
		Unsubscribe: unsubscribe,
	}); err != nil {
		return platformerrors.Wrap(err, "mailing a waitlist confirmation")
	}

	return nil
}

// linkedSignup is the signup a presented link names.
type linkedSignup struct {
	listID, signupID string
}

// readLink reads a confirmation-loop link presented at the door for action,
// without spending it, and reports the signup it names.
//
// Both link RPCs read, write, and only then spend — see spendLink. The action
// and the tenant a link was minted for never change, so checking them here is
// what keeps a link presented at the wrong door, or on a connection placed in
// the wrong tenant, from doing anything at all.
//
// Every refusal is ErrInvalidLink, with what it actually was recorded on the
// operation and nowhere else. A links store that will not answer is not a
// refusal and is reported as the outage it is.
func (s *Server) readLink(
	ctx context.Context,
	req *request,
	action links.Action,
	token string,
) (*linkedSignup, error) {
	claims, err := s.links.Inspect(ctx, links.Token(token))
	if err != nil {
		return nil, s.linkFailure(req, err)
	}

	if refusal := linkMismatch(claims, action, req.scope); refusal != "" {
		return nil, s.refuseLink(req, refusal)
	}

	linked := &linkedSignup{listID: claims.Metadata[linkListKey], signupID: string(claims.Subject)}
	req.op.Set(listKey, linked.listID).Set(signupKey, linked.signupID)

	return linked, nil
}

// spendLink spends a link once the write it authorized has committed.
//
// It comes after the write so that a write failing for a reason that is not a
// refusal (the database went away) leaves the link as it found it, and
// following it again finishes the job. Spending first would burn the link on a
// write that never landed.
//
// That honors a link on Inspect's answer, which the links package calls
// advisory, and it is sound here only because the write carries the single use
// itself. Confirm is a guarded move out of pending that one request wins, and
// the loser finds the signup already waiting and is refused. Unsubscribe
// withdraws, and a second withdrawal changes nothing. So a link that will not
// spend after its write — a second click that raced this one, an outage —
// leaves behind a link that can do nothing again. It is recorded on the
// operation rather than answered, and the caller is told what happened to the
// signup, which is that it moved.
func (s *Server) spendLink(ctx context.Context, req *request, token string) {
	if _, err := s.links.Redeem(ctx, links.Token(token)); err != nil {
		req.op.Set(linkUnspentKey, err.Error())
		req.op.Logger().Error("spending a waitlist link after its write", err)
	}
}

// linkMismatch reports why a link that exists is still not one this door
// honors, or the empty string when it is.
func linkMismatch(claims *links.Claims, action links.Action, scope tenancy.Scope) string {
	switch {
	case claims == nil:
		return "no claims"
	case claims.Action != action:
		return "minted for " + string(claims.Action)
	case claims.Metadata[linkScopeKey] != scope.Owner():
		return "minted in another tenant"
	case claims.Metadata[linkListKey] == "" || claims.Subject == "":
		return "names no signup"
	default:
		return ""
	}
}

// linkFailure sorts an error from the minter into a refusal or an outage.
func (s *Server) linkFailure(req *request, err error) error {
	for _, refusal := range []error{
		links.ErrLinkNotFound,
		links.ErrLinkAlreadyRedeemed,
		links.ErrLinkExpired,
		links.ErrLinkRevoked,
		links.ErrInvalidToken,
	} {
		if errors.Is(err, refusal) {
			return s.refuseLink(req, refusal.Error())
		}
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(),
		codes.Internal, "redeeming a waitlist link")
}

// refuseLink is the one refusal both link RPCs give, with the reason on the
// operation.
//
// The status is built from ErrInvalidLink alone rather than from a chain
// carrying what went wrong, because the encoding interceptor re-runs the
// registered mappers over the preserved chain: a chain holding links'
// ErrLinkExpired or waitlists' ErrWrongStatus would have its code overruled by
// that sentinel's mapper, and the one answer would become several.
func (s *Server) refuseLink(req *request, reason string) error {
	req.op.Set(linkRefusalKey, reason)

	return grpcerrors.PrepareAndLogGRPCStatus(ErrInvalidLink, req.op.Logger(), req.op.Span(),
		codes.NotFound, "redeeming a waitlist link")
}

// linkNamesAMovedSignup reports whether a store refusal means the signup a link
// names has moved where the link cannot take it — confirmed already, withdrawn,
// or archived — which the caller is told as ErrInvalidLink.
func linkNamesAMovedSignup(err error) bool {
	return errors.Is(err, waitlists.ErrWrongStatus) ||
		errors.Is(err, waitlists.ErrAlreadyWithdrawn) ||
		errors.Is(err, waitlists.ErrSignupNotFound)
}

// notConfirming is the answer both link RPCs give on a server built without
// WithConfirmation.
func (s *Server) notConfirming(req *request, method string) error {
	return grpcerrors.PrepareAndLogGRPCStatus(ErrConfirmationNotConfigured, req.op.Logger(), req.op.Span(),
		codes.Unimplemented, "serving %s", method)
}
