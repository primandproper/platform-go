package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

	"github.com/primandproper/primitives-go/database"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	filteringgrpc "github.com/primandproper/primitives-go/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The registry half of the surface: three RPCs whose caller is a handset.
//
// This is the strongest RPC case in the package, because the caller is
// literally a remote device. A registration converges on (platform, token)
// rather than inserting, because the token is the handset and a handset
// re-registers on every app launch and every token rotation — which is a
// description of a call being made from a phone rather than of a row a console
// edits.
//
// The three of them together are also the whole of what a device screen needs:
// this handset is here, these are my handsets, that one is not mine any more.
// The two registry methods that are not here are machinery — see roster_test.go
// and the Store methods themselves.
//
// RegisterDevice answers with the row the store handed it rather than with the
// registration it sent, because those differ whenever the write converged on a
// token already registered. RevokeDevice discards the row it is handed:
// RevokeDeviceResponse has no field to carry it, and the row is gone by the time
// a client could ask about it, which is the case the store returns it for — a
// consumer recording what it revoked inside its own transaction.

// RegisterDevice records the calling handset's token, under the caller's own
// principal.
//
// It converges rather than inserting: a token already registered moves to the
// principal registering it now, keeping its id and its creation time, because a
// handset that changes hands has one owner and a registry that kept both would
// deliver the previous owner's notifications to the new one. There is
// deliberately no variant on this surface that skips that convergence.
//
// The read-back runs inside the transaction this handler opens, which is what
// the store's reads taking an executor rather than a reader is for: a
// re-registration answers with the original id and creation time, and a
// response assembled outside the transaction would carry whatever was committed
// before the write.
//
// The registration carries no scope and no principal of its own. Both come off
// the connection: a handset naming a principal is a handset registering itself
// to receive somebody else's notifications.
func (s *Server) RegisterDevice(
	ctx context.Context,
	request *notificationspb.RegisterDeviceRequest,
) (*notificationspb.RegisterDeviceResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_RegisterDevice_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	device, err := deviceFromProto(request.GetInput(), req.principal)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading a device registration")

		return nil, err
	}

	req.op.Set(platformKey, device.Platform.String())

	var registered *notifications.Device

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var registerErr error

		registered, registerErr = s.registry.RegisterDevice(ctx, tx, req.scope, device)

		return registerErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "registering a %s device", device.Platform)

		return nil, err
	}

	req.op.Set(deviceIDKey, registered.ID)

	return &notificationspb.RegisterDeviceResponse{Result: DeviceToProto(registered)}, nil
}

// ListDevices pages the caller's own registered devices.
//
// What comes back is what is registered, on what, and when it was last seen. It
// is not the tokens: [DeviceToProto] has nowhere to put one, so this cannot
// become the call that harvests every push address an account holds.
func (s *Server) ListDevices(
	ctx context.Context,
	request *notificationspb.ListDevicesRequest,
) (*notificationspb.ListDevicesResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_ListDevices_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := filteringgrpc.FromProto(request.GetFilter())
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a device page")

		return nil, err
	}

	page, err := s.registry.ListDevices(ctx, s.client.Reader(), req.scope, req.principal, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing devices")

		return nil, err
	}

	return &notificationspb.ListDevicesResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    DevicesToProto(page.Data),
	}, nil
}

// RevokeDevice removes one of the caller's registrations — a sign-out, or a
// device somebody no longer has. The row is deleted rather than archived.
//
// A registration under another principal is notifications.ErrDeviceNotFound,
// which is what it is from here.
//
// It is the surface's answer to "stop pushing to that handset", and it is not
// the same call as the registry's own token invalidation: this one is a person
// naming a device they own, and that one is a push provider reporting a token
// it has permanently rejected. The second stays off the wire — see the Store
// method, which says why at length.
func (s *Server) RevokeDevice(
	ctx context.Context,
	request *notificationspb.RevokeDeviceRequest,
) (*notificationspb.RevokeDeviceResponse, error) {
	ctx, req, done, err := s.caller(ctx, notificationspb.NotificationsService_RevokeDevice_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetDeviceId()
	req.op.Set(deviceIDKey, id)

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		_, revokeErr := s.registry.RevokeDevice(ctx, tx, req.scope, req.principal, id)

		return revokeErr
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "revoking device %q", id)

		return nil, err
	}

	return &notificationspb.RevokeDeviceResponse{}, nil
}
