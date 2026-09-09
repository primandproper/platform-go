package errormappers

import (
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/sessions"
	"github.com/primandproper/platform-go/v14/waitlists"

	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/errors/http"
)

// Register installs the transport mappings for every package in this module
// that declares a pair, on both transports, plus the sentinels gRPC may quote
// verbatim. It is the one call a service assembled by hand makes;
// service.Register makes it for a service built from a service.Config.
//
// It registers all eight unconditionally, including for a service that has no
// privacy requests, runs no operations, keeps no waiting list and has nobody
// signing in. An unused mapper costs one comparison against a sentinel the
// process cannot produce, and that is the cheap direction to be wrong in — the
// expensive one is an action link answering 500 because nobody registered
// anything. Conditioning on presence would also mean this package taking an
// argument describing which subsystems a service has, which is the config tree
// it exists to avoid importing.
//
// Registration is additive and safe to call from more than one goroutine.
func Register() {
	httperrors.RegisterHTTPErrorMapper(dataprivacy.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(dataprivacy.GRPCMapper)

	httperrors.RegisterHTTPErrorMapper(links.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(links.GRPCMapper)

	// The redemption outcomes' own wording is meant for the person reading it,
	// so gRPC is told it may send it rather than rendering "FailedPrecondition"
	// four times.
	grpcerrors.RegisterClientSafeSentinels(links.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(identity.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(identity.GRPCMapper)

	// The same reading for the directory: its codes collide too, and a client
	// told "AlreadyExists" cannot tell which of two fields to change.
	grpcerrors.RegisterClientSafeSentinels(identity.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(operations.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(operations.GRPCMapper)

	httperrors.RegisterHTTPErrorMapper(sessions.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(sessions.GRPCMapper)

	httperrors.RegisterHTTPErrorMapper(oauth2clients.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(oauth2clients.GRPCMapper)

	httperrors.RegisterHTTPErrorMapper(signin.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(signin.GRPCMapper)

	// The third package whose wording is meant for the person reading it, and
	// the one where the codes collide worst: four of its nine are
	// PermissionDenied and three are FailedPrecondition, each with a different
	// remedy. See signin.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(signin.ClientSafeSentinels...)

	// The two refusals an authorization request meets. Both are PermissionDenied
	// and so are indistinguishable by code, and each names a different remedy for
	// somebody staring at a browser. See oauth2clients.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(oauth2clients.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(waitlists.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(waitlists.GRPCMapper)

	// The fourth package whose wording is meant for the person reading it, and
	// the only one whose reader is not signed in: four of its five refusals are
	// FailedPrecondition, and the person meeting them is filling in a signup
	// form or clicking an unsubscribe link. See waitlists.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(waitlists.ClientSafeSentinels...)
}
