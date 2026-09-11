package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/database"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The queue half of the surface: eleven RPCs, nine behind a permission and two
// behind none.
//
// The two are the person's own — the form they filled in, and their asking to
// come off the list — and they are the reason this service has a
// [SignupAuthorizer] and a [ScopeResolver] where the surfaces next door have
// neither. Everything else here is whoever is running the launch.
//
// Every write opens its own transaction with Client.WithTransaction. The three
// that revise or move a row read it back inside that transaction, because
// status_changed_at is what a consumer schedules a reminder off and the store
// stamps it rather than returning it — see waitlists.SignupStore.Invite.
//
// None of them switches on a sentinel: the error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*, and
// the encoding interceptor re-runs the registered mappers over the preserved
// chain, so waitlists.GRPCMapper wins over the guess made here. The places a
// code is passed as an answer are the ones the store never sees: a join that
// named no signup, an erasure that named no subject, a filter that could not be
// read, and the withdrawal a consumer's authorizer refused.

// Join adds somebody to a list. It is the form somebody filled in, and it is
// reachable without a grant.
//
// It refuses a list that is closed or missing, a contact already on the list,
// and a contact that has withdrawn from it — the last of which is the obligation
// this package is shaped around and outlives the address it is about. All four
// refusals reach the caller with the sentinel's own wording, because
// waitlists.ClientSafeSentinels says a gRPC status may quote them: the person
// reading this one is looking at a signup form.
//
// The signup's subject is the caller where there is one and nobody where there
// is not. It is never read off the request — see [waitlistspb.JoinRequest],
// which reserves the name.
func (s *Server) Join(
	ctx context.Context,
	request *waitlistspb.JoinRequest,
) (*waitlistspb.JoinResponse, error) {
	ctx, req, done, err := s.visitor(ctx, waitlistspb.WaitlistsService_Join_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID := request.GetListId()
	req.op.Set(listKey, listID)

	// A nil request cannot reach a generated handler, so the nil signup below is
	// unreachable from the wire; the converter still refuses it, because the
	// alternative is a nil dereference if it ever becomes reachable.
	signup := signupFromJoin(request, req.principal)
	if signup == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(waitlists.ErrNilSignup,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "joining waitlist %q", listID)

		return nil, err
	}

	var joined *waitlists.Signup

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		stored, joinErr := s.store.Join(ctx, tx, req.scope, listID, signup)
		if joinErr != nil {
			return joinErr
		}

		joined = stored

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "joining waitlist %q", listID)

		return nil, err
	}

	req.op.Set(signupKey, joined.ID)

	return &waitlistspb.JoinResponse{Result: SignupToProto(joined)}, nil
}

// GetSignup reads one signup on one of the caller's lists.
//
// It names both the list and the signup, because the list is half of what
// addresses a signup: a read that omitted it could hand one list's row to a
// caller holding another list's id.
func (s *Server) GetSignup(
	ctx context.Context,
	request *waitlistspb.GetSignupRequest,
) (*waitlistspb.GetSignupResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_GetSignup_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID, signupID := request.GetListId(), request.GetSignupId()
	req.op.Set(listKey, listID).Set(signupKey, signupID)

	signup, err := s.store.GetSignup(ctx, s.client.Reader(), req.scope, listID, signupID)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading waitlist signup %q", signupID)

		return nil, err
	}

	return &waitlistspb.GetSignupResponse{Result: SignupToProto(signup)}, nil
}

// GetSignupByContact reads one signup by the address it was made with.
//
// It is the sharpest read on this service and the reason the public half stops
// where it does: answering it for anybody who can reach the port would make the
// surface an oracle over which addresses are on which list. It is behind
// [PermissionReadSignups], and the question a person on a signup page is
// actually asking — "am I already on this" — is answered by Join's refusal
// instead, which discloses nothing they did not already send.
//
// A withdrawn signup is a live row and comes back as one, with its contact blank
// and its status saying why, which is what lets an unsubscribe console tell
// somebody they are already off the list rather than that they were never on it.
func (s *Server) GetSignupByContact(
	ctx context.Context,
	request *waitlistspb.GetSignupByContactRequest,
) (*waitlistspb.GetSignupByContactResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID := request.GetListId()
	req.op.Set(listKey, listID)

	// The contact is deliberately not recorded on the operation. It is an
	// address somebody gave a signup form, and a span attribute is a copy of it
	// in whatever the deployment exports traces to.
	signup, err := s.store.GetSignupByContact(ctx, s.client.Reader(), req.scope, listID, request.GetContact())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading the waitlist signup for a contact")

		return nil, err
	}

	req.op.Set(signupKey, signup.ID)

	return &waitlistspb.GetSignupByContactResponse{Result: SignupToProto(signup)}, nil
}

// ListSignups pages one list's signups, oldest first, which is the order they
// joined in.
func (s *Server) ListSignups(
	ctx context.Context,
	request *waitlistspb.ListSignupsRequest,
) (*waitlistspb.ListSignupsResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_ListSignups_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID := request.GetListId()
	req.op.Set(listKey, listID)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a waitlist signup page")

		return nil, err
	}

	page, err := s.store.ListSignups(ctx, s.client.Reader(), req.scope, listID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing the signups on waitlist %q", listID)

		return nil, err
	}

	return &waitlistspb.ListSignupsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    SignupsToProto(page.Data),
	}, nil
}

// ListSignupsForSubject pages the signups one principal holds across every list
// in the caller's tenant.
//
// It is the read a profile page makes and the one a privacy export walks; the
// filter's include_archived is what an export sets, because an archived signup
// still holds the address it was made with. A withdrawn signup is never among
// them: a withdrawal blanks the subject along with the contact, so the row that
// remembers a suppression no longer says whose it was.
func (s *Server) ListSignupsForSubject(
	ctx context.Context,
	request *waitlistspb.ListSignupsForSubjectRequest,
) (*waitlistspb.ListSignupsForSubjectResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_ListSignupsForSubject_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subject := subjectFromProto(request.GetSubject())
	req.op.Set(subjectKey, subject.Type.String()).Set(subjectIDKey, subject.ID)

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a subject's waitlist signups")

		return nil, err
	}

	page, err := s.store.ListSignupsForSubject(ctx, s.client.Reader(), req.scope, subject, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing a subject's waitlist signups")

		return nil, err
	}

	return &waitlistspb.ListSignupsForSubjectResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    SignupsToProto(page.Data),
	}, nil
}

// UpdateSignupNotes rewrites the operator's note against a signup.
//
// It is the one write that touches a signup without moving it, and the store
// leaves status_changed_at alone on purpose — a typo fixed in a note must not
// reschedule the reminder somebody's invitation started. The response carries
// the row so that a console can see that it did not move.
func (s *Server) UpdateSignupNotes(
	ctx context.Context,
	request *waitlistspb.UpdateSignupNotesRequest,
) (*waitlistspb.UpdateSignupNotesResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_UpdateSignupNotes_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID, signupID := request.GetListId(), request.GetSignupId()
	req.op.Set(listKey, listID).Set(signupKey, signupID)

	signup, err := s.writeAndReadBack(ctx, req, listID, signupID,
		func(tx database.Tx) error {
			return s.store.UpdateSignupNotes(ctx, tx, req.scope, listID, signupID, request.GetNotes())
		},
		"updating the notes on waitlist signup %q", signupID)
	if err != nil {
		return nil, err
	}

	return &waitlistspb.UpdateSignupNotesResponse{Result: SignupToProto(signup)}, nil
}

// Invite moves a waiting signup to invited and stamps the moment.
//
// Anything that is not waiting is refused, and the refusal is the affected-row
// count of a guarded update rather than a decision made on a read — so two
// requests inviting the same person send one email between them, and the one
// that lost is told so.
//
// The response carries the signup as it now stands, because status_changed_at is
// the field the reminder is scheduled off and this is the call that set it.
func (s *Server) Invite(
	ctx context.Context,
	request *waitlistspb.InviteRequest,
) (*waitlistspb.InviteResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_Invite_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID, signupID := request.GetListId(), request.GetSignupId()
	req.op.Set(listKey, listID).Set(signupKey, signupID)

	signup, err := s.writeAndReadBack(ctx, req, listID, signupID,
		func(tx database.Tx) error {
			return s.store.Invite(ctx, tx, req.scope, listID, signupID)
		},
		"inviting waitlist signup %q", signupID)
	if err != nil {
		return nil, err
	}

	return &waitlistspb.InviteResponse{Result: SignupToProto(signup)}, nil
}

// Convert moves an invited signup to converted and stamps the moment.
//
// Anything that is not invited is refused, guarded the same way Invite is. It is
// behind a grant of its own rather than Invite's, because recording that
// somebody took their turn and deciding whose turn it is are different acts by
// different callers.
func (s *Server) Convert(
	ctx context.Context,
	request *waitlistspb.ConvertRequest,
) (*waitlistspb.ConvertResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_Convert_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID, signupID := request.GetListId(), request.GetSignupId()
	req.op.Set(listKey, listID).Set(signupKey, signupID)

	signup, err := s.writeAndReadBack(ctx, req, listID, signupID,
		func(tx database.Tx) error {
			return s.store.Convert(ctx, tx, req.scope, listID, signupID)
		},
		"converting waitlist signup %q", signupID)
	if err != nil {
		return nil, err
	}

	return &waitlistspb.ConvertResponse{Result: SignupToProto(signup)}, nil
}

// Withdraw takes somebody off a list at their own request, and is reachable
// without a grant.
//
// It is the second write on the public half and the only RPC on this service
// that names a row a caller may have no standing in, so [SignupAuthorizer] is
// asked before anything is written — see that seam for why a grant could not
// have answered it and why a signup identifier is not a credential.
//
// The store blanks the contact, the notes and the subject and keeps the contact
// digest, which is what lets a later signup from the same address be refused
// rather than quietly re-subscribing somebody who asked to be left alone. A
// second call reports that the signup has already been withdrawn rather than
// restamping the moment they left.
//
// The response is empty and carries no signup. What is left of the row after a
// withdrawal is a status and a digest, and the caller who has just asked to be
// forgotten is not the caller to hand it to.
func (s *Server) Withdraw(
	ctx context.Context,
	request *waitlistspb.WithdrawRequest,
) (*waitlistspb.WithdrawResponse, error) {
	ctx, req, done, err := s.visitor(ctx, waitlistspb.WaitlistsService_Withdraw_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID, signupID := request.GetListId(), request.GetSignupId()
	req.op.Set(listKey, listID).Set(signupKey, signupID)

	if err = s.authorizeWithdrawal(ctx, req, listID, signupID); err != nil {
		return nil, err
	}

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.store.Withdraw(ctx, tx, req.scope, listID, signupID)
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "withdrawing waitlist signup %q", signupID)

		return nil, err
	}

	return &waitlistspb.WithdrawResponse{}, nil
}

// WithdrawSignupsForSubject withdraws every signup one principal holds in the
// caller's tenant, archived signups included, and reports how many that was.
//
// It is the erasure path, and it is a withdrawal rather than a delete for the
// reason a single withdrawal is: a delete frees the unique key, so somebody
// erased at their own request could be re-subscribed by the next form
// submission. Zero is not an error — a person who never joined a list is a
// person with nothing here to erase.
//
// A request naming no subject at all is refused here rather than at the store,
// because a subject bound to nobody would name every signup nobody claimed. The
// store refuses it too; this refusal is the one whose message is about the
// request.
func (s *Server) WithdrawSignupsForSubject(
	ctx context.Context,
	request *waitlistspb.WithdrawSignupsForSubjectRequest,
) (*waitlistspb.WithdrawSignupsForSubjectResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	if request.GetSubject() == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilSubject,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "withdrawing a subject's waitlist signups")

		return nil, err
	}

	subject := subjectFromProto(request.GetSubject())
	req.op.Set(subjectKey, subject.Type.String()).Set(subjectIDKey, subject.ID)

	var withdrawn int64

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		count, withdrawErr := s.store.WithdrawSignupsForSubject(ctx, tx, req.scope, subject)
		if withdrawErr != nil {
			return withdrawErr
		}

		withdrawn = count

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "withdrawing a subject's waitlist signups")

		return nil, err
	}

	req.op.Set(countKey, withdrawn)

	return &waitlistspb.WithdrawSignupsForSubjectResponse{Withdrawn: withdrawn}, nil
}

// ArchiveSignup retires a signup administratively.
//
// It is not a withdrawal and must not be used as one: it hides the row and
// changes nothing about what it holds, so the contact is still stored, nothing
// is suppressed, and the next signup from that address is refused as a
// duplicate. Somebody asking to come off a list reaches [Server.Withdraw].
func (s *Server) ArchiveSignup(
	ctx context.Context,
	request *waitlistspb.ArchiveSignupRequest,
) (*waitlistspb.ArchiveSignupResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_ArchiveSignup_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	listID, signupID := request.GetListId(), request.GetSignupId()
	req.op.Set(listKey, listID).Set(signupKey, signupID)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.store.ArchiveSignup(ctx, tx, req.scope, listID, signupID)
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving waitlist signup %q", signupID)

		return nil, err
	}

	return &waitlistspb.ArchiveSignupResponse{}, nil
}

// writeAndReadBack runs one signup write and reads the row back inside the same
// transaction, which is what the three revising RPCs share.
//
// The read is inside the transaction deliberately, and it is the reason
// waitlists.Store's reads take a database.SQLQueryExecutor rather than a reader:
// a Tx satisfies that interface, so this read sees the write it follows. On
// Client.Reader() it would be a read of a database that does not yet hold the
// change — the signup would come back in the status it was in before the call.
//
// It is a helper rather than three copies because the part that can be got wrong
// is which executor the read runs on, and it is the same mistake in all three.
func (s *Server) writeAndReadBack(
	ctx context.Context,
	req *request,
	listID, signupID string,
	write func(tx database.Tx) error,
	descriptionFmt string,
	descriptionArgs ...any,
) (*waitlists.Signup, error) {
	var signup *waitlists.Signup

	if err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if writeErr := write(tx); writeErr != nil {
			return writeErr
		}

		read, readErr := s.store.GetSignup(ctx, tx, req.scope, listID, signupID)
		if readErr != nil {
			return readErr
		}

		signup = read

		return nil
	}); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, descriptionFmt, descriptionArgs...)
	}

	return signup, nil
}
