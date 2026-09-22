package errormappers

import (
	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/entitlements"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	"github.com/primandproper/platform-go/v14/metering"
	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/sessions"
	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/shredding"
	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/webhooks"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"
)

// Register installs the transport mappings for every package in this module
// that declares a pair, on both transports, plus the sentinels gRPC may quote
// verbatim. It is the one call a service assembled by hand makes;
// service.Register makes it for a service built from a service.Config.
//
// It registers every one of them unconditionally, including for a service that
// has no privacy requests, runs no operations, reads no audit log, tells nobody
// anything, delivers no webhooks, sells nothing, hears no complaints and has
// nobody signing in. An unused mapper costs one comparison against a sentinel
// the process cannot produce, and that is the cheap direction to be wrong in
// — the expensive one is an action link answering 500 because nobody
// registered anything. Conditioning on presence would also mean this package
// taking an argument describing which subsystems a service has, which is the
// config tree it exists to avoid importing.
//
// Registration is additive and safe to call from more than one goroutine. It
// is also silent about collisions: a sentinel some mapper already claims
// stays that mapper's, because the registries stop at the first match. A
// consumer migrating onto this call therefore has mappers to delete rather
// than only a call to add — see the package documentation for which.
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

	// The list where the codes collide worst: four of signin's nine refusals are
	// PermissionDenied and three are FailedPrecondition, each with a different
	// remedy. See signin.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(signin.ClientSafeSentinels...)

	// And the same eleven again as identifiers, which is what a client that must
	// branch on the refusal reads instead of the prose. It is the only reasons
	// list in the module so far; the other client-safe lists are candidates for
	// one and each is its own decision about names a client compiles against
	// forever. See signin.ClientSafeReasons.
	//
	// This call registers the sentinels as client-safe too, so it would do the
	// work of the line above on its own. The line above stays because the two
	// lists are separate statements — these refusals may be quoted, these
	// refusals may be switched on — and a package that later adds a sentinel to
	// one and not the other should read as it is rather than silently inherit.
	grpcerrors.RegisterClientSafeReasons(signin.ClientSafeReasons...)

	// The two refusals an authorization request meets. Both are PermissionDenied
	// and so are indistinguishable by code, and each names a different remedy for
	// somebody staring at a browser. See oauth2clients.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(oauth2clients.ClientSafeSentinels...)

	// The audit log, whose pair claims one sentinel: an entry that is not there,
	// which is also what an entry in another tenant's log reads as. It has no
	// client-safe list because one mapped sentinel collides with nothing — see
	// audit's own errormappers.go.
	httperrors.RegisterHTTPErrorMapper(audit.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(audit.GRPCMapper)

	httperrors.RegisterHTTPErrorMapper(notifications.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(notifications.GRPCMapper)

	// No client-safe sentinels for notifications. Half of its mapped refusals
	// name a field a client is about to re-send and the mapper's own message says
	// which; the other half are the two not-founds, whose whole property is that
	// somebody else's notification and somebody else's handset read as absent.
	// See the comment beside notifications.HTTPMapper.

	httperrors.RegisterHTTPErrorMapper(comments.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(comments.GRPCMapper)

	// The refusals most obviously written for a person: four of comments' six are
	// InvalidArgument and two are NotFound, and each says which of a form's
	// fields to go back to. See comments.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(comments.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(webhooks.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(webhooks.GRPCMapper)

	// No client-safe sentinels for webhooks. Its refusals name a field a console
	// is about to re-render — a URL, a header, an event type — and the message
	// the mapper writes says which one, so there is nothing the sentinel's own
	// wording would add. The one whose wording is deliberately *narrower* than
	// the sentinel's is ErrEndpointOutOfScope, which must not tell a caller that
	// another tenant is holding the identifier they chose.

	httperrors.RegisterHTTPErrorMapper(billing.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(billing.GRPCMapper)

	// The worst collisions in the module: seven of billing's refusals are
	// InvalidArgument, five are AlreadyExists and two are FailedPrecondition,
	// and inside each family the remedies differ — acknowledge the redelivery,
	// fix the field, or fix the code that chose the id. See
	// billing.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(billing.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(issuereports.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(issuereports.GRPCMapper)

	// The report queue's four lifecycle refusals. Two of them are
	// codes.InvalidArgument and differ in what the caller does next, and the
	// other two say "re-read, it moved" and "there is no such report" — one
	// instruction each, and the code carries neither. See
	// issuereports.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(issuereports.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(settings.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(settings.GRPCMapper)

	// Three of settings' refusals are codes.NotFound — no such setting, nobody
	// has set it, and it has no value and no default — which are three different
	// things to tell somebody, and one of the six names the row an administrator
	// has to clear before their edit can land. See settings.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(settings.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(waitlists.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(waitlists.GRPCMapper)

	// The only list whose reader is not signed in: four of waitlists' five
	// refusals are FailedPrecondition, and the person meeting them is filling in
	// a signup form or clicking an unsubscribe link. See
	// waitlists.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(waitlists.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(passwordreset.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(passwordreset.GRPCMapper)

	// The other reader who is not signed in — that is the whole situation a
	// reset link exists for. All three of its outcomes are one code on each
	// transport, so without the list somebody holding a day-old link is told
	// "FailedPrecondition" where the three sentinels exist to tell them which.
	// See passwordreset.ClientSafeSentinels.
	grpcerrors.RegisterClientSafeSentinels(passwordreset.ClientSafeSentinels...)

	httperrors.RegisterHTTPErrorMapper(metering.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(metering.GRPCMapper)

	// No client-safe sentinels for metering. Its six refusals are the ingest
	// path's, the caller is a machine posting usage rather than a person reading
	// a page, and what that caller branches on is the sentinel — which survives
	// the wire whether or not gRPC is allowed to quote it. The field to fix is
	// named in the message the HTTP mapper writes.
	//
	// The same reading for entitlements below: the two refusals a request path
	// meets are platform sentinels, already mapped and already worded, and the
	// one this package maps collides with nothing else it maps.
	httperrors.RegisterHTTPErrorMapper(entitlements.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(entitlements.GRPCMapper)

	httperrors.RegisterHTTPErrorMapper(shredding.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(shredding.GRPCMapper)

	// No client-safe sentinels for shredding either, and here it is the codes
	// rather than the reader: its three mapped refusals are NotFound, Aborted and
	// InvalidArgument, so the code already says which happened. The one whose
	// wording is deliberately *narrower* than the sentinel's is
	// ErrSubjectShredded, which must not confirm to whoever is asking that a
	// particular person was ever here.

	httperrors.RegisterHTTPErrorMapper(mediaregistry.HTTPMapper)
	grpcerrors.RegisterGRPCErrorMapper(mediaregistry.GRPCMapper)

	// No client-safe sentinels for mediaregistry. Like webhooks, its refusals
	// name a thing a consumer's upload form is about to re-send — a key, a
	// belongs-to subject, a list of ids — and the message the mapper writes says
	// which one.
}
