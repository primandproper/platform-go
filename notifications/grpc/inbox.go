package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/v2/database"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The inbox half of the surface: six RPCs over the rows a bell icon reads.
//
// Every one of them is addressed to the caller and to nobody else. The
// recipient is req.principal, resolved from the connection, and it is bound
// into each statement beside the scope — which is why a notification belonging
// to somebody else is notifications.ErrNotificationNotFound rather than a
// refusal that would confirm the row exists.
//
// The three writes each open a transaction with Client.WithTransaction and pass
// the Tx they are handed. notifications ships no write that opens one of its
// own, deliberately, because a notification is almost always about something
// else that was just written; a handler here is the caller that convention
// describes as having nothing to join.
//
// Each method is one call plus its conversion. The error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*: the
// encoding interceptor re-runs the registered mappers over the preserved chain,
// so notifications.GRPCMapper wins over the guess made here and no handler on
// this surface switches on a sentinel.
//
// Two of the three writes discard the row the store answers with, because
// MarkNotificationReadResponse and ArchiveNotificationResponse have no field to
// carry it. What those RPCs promise is that the row moved, and a client that
// wants to see it afterwards asks for it — the mark is visible to
// GetNotification and the archive to a list that includes archived rows. The
// store returns it for the caller that has no second read available, which is a
// consumer writing an audit entry inside its own transaction rather than a
// handler answering a request that is about to end.

// ListNotifications pages the caller's inbox, in the direction the filter names.
func (s *Server) ListNotifications(
	ctx context.Context,
	request *notificationspb.ListNotificationsRequest,
) (*notificationspb.ListNotificationsResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_ListNotifications_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of an inbox page")

		return nil, err
	}

	page, err := s.inbox.ListNotifications(ctx, s.client.Reader(), req.scope, req.principal, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing notifications")

		return nil, err
	}

	return &notificationspb.ListNotificationsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    NotificationsToProto(page.Data),
	}, nil
}

// ListUnreadNotifications is ListNotifications restricted to what the caller has
// not read.
//
// It is a separate RPC rather than a flag on the one above, for the reason the
// store method is a separate method: unread is "read_at is absent" and there is
// no value a caller could pass to relax it. The badge count is on the response's
// pagination, filtered rather than paged, so a client that wants only the number
// asks for one row and reads the count.
func (s *Server) ListUnreadNotifications(
	ctx context.Context,
	request *notificationspb.ListUnreadNotificationsRequest,
) (*notificationspb.ListUnreadNotificationsResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_ListUnreadNotifications_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of an unread inbox page")

		return nil, err
	}

	page, err := s.inbox.ListUnreadNotifications(ctx, s.client.Reader(), req.scope, req.principal, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing unread notifications")

		return nil, err
	}

	return &notificationspb.ListUnreadNotificationsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    NotificationsToProto(page.Data),
	}, nil
}

// GetNotification reads one of the caller's live notifications.
//
// Absent, archived, and addressed to somebody else are one answer, which is what
// keeps this read from being an oracle for what other people have been told.
func (s *Server) GetNotification(
	ctx context.Context,
	request *notificationspb.GetNotificationRequest,
) (*notificationspb.GetNotificationResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_GetNotification_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetNotificationId()
	req.op.Set(notificationIDKey, id)

	notification, err := s.inbox.GetNotification(ctx, s.client.Reader(), req.scope, req.principal, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading notification %q", id)

		return nil, err
	}

	return &notificationspb.GetNotificationResponse{Result: NotificationToProto(notification)}, nil
}

// MarkNotificationRead stamps one of the caller's notifications as read, now.
//
// It is idempotent and does not move the stamp: a notification already read
// reports success and keeps the time it was first read, which is what a digest
// and a re-notify both read.
func (s *Server) MarkNotificationRead(
	ctx context.Context,
	request *notificationspb.MarkNotificationReadRequest,
) (*notificationspb.MarkNotificationReadResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_MarkNotificationRead_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetNotificationId()
	req.op.Set(notificationIDKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		_, markErr := s.inbox.MarkNotificationRead(ctx, tx, req.scope, req.principal, id)

		return markErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "marking notification %q read", id)

		return nil, err
	}

	return &notificationspb.MarkNotificationReadResponse{}, nil
}

// MarkAllNotificationsRead stamps everything the caller has not read, and
// reports how many that was.
//
// The count is the answer rather than a diagnostic — it is what was sitting
// unread at the moment the statement ran, and there is no cheaper way to learn
// it — and it is the count this transaction wrote. A retry answers zero, which
// is the honest answer rather than a replay: nothing was unread the second time.
func (s *Server) MarkAllNotificationsRead(
	ctx context.Context,
	_ *notificationspb.MarkAllNotificationsReadRequest,
) (*notificationspb.MarkAllNotificationsReadResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_MarkAllNotificationsRead_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	var marked int64

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var markErr error
		marked, markErr = s.inbox.MarkAllNotificationsRead(ctx, tx, req.scope, req.principal)

		return markErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "marking every notification read")

		return nil, err
	}

	req.op.Set(countKey, marked)

	return &notificationspb.MarkAllNotificationsReadResponse{Marked: marked}, nil
}

// ArchiveNotification dismisses one of the caller's notifications, leaving the
// row for whoever asks later what somebody was told.
//
// A notification already archived is not in the inbox and this RPC addresses the
// inbox, so it answers as absent.
func (s *Server) ArchiveNotification(
	ctx context.Context,
	request *notificationspb.ArchiveNotificationRequest,
) (*notificationspb.ArchiveNotificationResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_ArchiveNotification_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetNotificationId()
	req.op.Set(notificationIDKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		_, archiveErr := s.inbox.ArchiveNotification(ctx, tx, req.scope, req.principal, id)

		return archiveErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "archiving notification %q", id)

		return nil, err
	}

	return &notificationspb.ArchiveNotificationResponse{}, nil
}
