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

// The catalog half of the surface: six RPCs over the lists in the caller's
// tenant, five behind a permission and one behind none.
//
// The writes open their own transaction with Client.WithTransaction, because
// waitlists.Store's writes take a database.Tx and an RPC handler is precisely
// the caller that method's documentation describes: one with nothing of its own
// to join. The two that revise a row read it back inside that transaction, which
// is what the Store's reads taking an executor rather than a reader is for — the
// read sees the write it follows, and the response carries the timestamps the
// database stamped rather than the ones the request sent.
//
// None of them switches on a sentinel: the error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*, and
// the encoding interceptor re-runs the registered mappers over the preserved
// chain, so waitlists.GRPCMapper wins over the guess made here. The places a
// code is passed as an answer rather than a default are the two the store never
// sees: a request that named no list at all, and a filter that could not be
// read.

// CreateList opens a waitlist in the caller's tenant.
func (s *Server) CreateList(
	ctx context.Context,
	request *waitlistspb.CreateListRequest,
) (*waitlistspb.CreateListResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_CreateList_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	list := listFromProto(request.GetList())
	if list == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilListInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "creating a waitlist")

		return nil, err
	}

	// The id on the input is ignored on a creation, so that "create" cannot be
	// used to write a row at an identifier a client chose — the store mints one,
	// and every read here is keyed on it.
	list.ID = ""

	var created *waitlists.List

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		stored, createErr := s.store.CreateList(ctx, tx, req.scope, list)
		if createErr != nil {
			return createErr
		}

		created = stored

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "creating a waitlist")

		return nil, err
	}

	req.op.Set(listKey, created.ID)

	return &waitlistspb.CreateListResponse{Result: ListToProto(created)}, nil
}

// GetList reads one of the caller's lists, open or closed.
//
// A list in another tenant's scope reads as one that does not exist, which is
// what it is from here — the scope is bound into the statement rather than
// checked in front of it.
func (s *Server) GetList(
	ctx context.Context,
	request *waitlistspb.GetListRequest,
) (*waitlistspb.GetListResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_GetList_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetListId()
	req.op.Set(listKey, id)

	list, err := s.store.GetList(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading waitlist %q", id)

		return nil, err
	}

	return &waitlistspb.GetListResponse{Result: ListToProto(list)}, nil
}

// ListLists pages the caller's whole catalog, open and closed alike. It is the
// administrative read; [Server.ListOpenLists] is the public one.
func (s *Server) ListLists(
	ctx context.Context,
	request *waitlistspb.ListListsRequest,
) (*waitlistspb.ListListsResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_ListLists_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a waitlist page")

		return nil, err
	}

	page, err := s.store.ListLists(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing waitlists")

		return nil, err
	}

	return &waitlistspb.ListListsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ListsToProto(page.Data),
	}, nil
}

// ListOpenLists pages the lists still taking signups, and is the one read on
// this service a caller reaches without a grant.
//
// It is what a "join the waitlist" page offers, and it is a separate statement
// rather than a filter over ListLists' results, because a page filtered after
// the fact is a page whose size the caller cannot rely on.
//
// It resolves its tenant like the other two public RPCs: off the caller where
// there is one, and off the connection where there is not. An anonymous visitor
// therefore sees the catalog the deployment placed them in and no other.
func (s *Server) ListOpenLists(
	ctx context.Context,
	request *waitlistspb.ListOpenListsRequest,
) (*waitlistspb.ListOpenListsResponse, error) {
	ctx, req, done, err := s.visitor(ctx, waitlistspb.WaitlistsService_ListOpenLists_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of an open waitlist page")

		return nil, err
	}

	page, err := s.store.ListOpenLists(ctx, s.client.Reader(), req.scope, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing open waitlists")

		return nil, err
	}

	return &waitlistspb.ListOpenListsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ListsToProto(page.Data),
	}, nil
}

// UpdateList rewrites a list's name, description and closing time.
//
// It answers with the list as stored rather than with the list it was sent,
// which is the reason the read is inside the transaction: last_updated_at is the
// database's, and a response echoing the request would be telling a console the
// row still says what it said before this call.
//
// It will not revive an archived list, and it is not guarded against the signups
// already on one: a list closed early keeps everybody who joined while it was
// open, and reopening one lets the next person through.
func (s *Server) UpdateList(
	ctx context.Context,
	request *waitlistspb.UpdateListRequest,
) (*waitlistspb.UpdateListResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_UpdateList_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	list := listFromProto(request.GetList())
	if list == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilListInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "updating a waitlist")

		return nil, err
	}

	req.op.Set(listKey, list.ID)

	updated := list

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if updateErr := s.store.UpdateList(ctx, tx, req.scope, list); updateErr != nil {
			return updateErr
		}

		read, readErr := s.store.GetList(ctx, tx, req.scope, list.ID)
		if readErr != nil {
			return readErr
		}

		updated = read

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "updating waitlist %q", list.ID)

		return nil, err
	}

	return &waitlistspb.UpdateListResponse{Result: ListToProto(updated)}, nil
}

// ArchiveList retires one of the caller's lists.
//
// The signups against it are left alone and stay readable, because archiving is
// not erasure. What it does do is close the list to new signups immediately,
// whatever its closing time says.
func (s *Server) ArchiveList(
	ctx context.Context,
	request *waitlistspb.ArchiveListRequest,
) (*waitlistspb.ArchiveListResponse, error) {
	ctx, req, done, err := s.caller(ctx, waitlistspb.WaitlistsService_ArchiveList_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetListId()
	req.op.Set(listKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		return s.store.ArchiveList(ctx, tx, req.scope, id)
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving waitlist %q", id)

		return nil, err
	}

	return &waitlistspb.ArchiveListResponse{}, nil
}
