package http

import (
	nethttp "net/http"

	"github.com/primandproper/platform-go/v14/internal/routeguard"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
)

// The permissions this surface's routes require, in authorization's
// vocabulary.
//
// They are declared here rather than in the authorization package for the
// reason every gRPC surface's are: authorization owns what a Permission is and
// no domain's names. They are dotted and namespaced so a consumer composing
// several domains' fragments cannot have two collide, and they are values
// rather than an enum because a consumer's policy is data.
//
// Platform declares them and grants none. Whether a member holds them is the
// consumer's policy — a deployment that lets every person ask for their own
// data grants all three to its members, and one that routes every privacy
// request through its support desk grants them to its staff — and a grant
// widens nothing: every route is still narrowed to the subject the resolver
// answered, so a holder of every permission here reads their own requests and
// nobody else's.
const (
	// PermissionSubmitRequests covers asking for an export or an erasure.
	PermissionSubmitRequests authorization.Permission = "dataprivacy.requests.submit"

	// PermissionReadRequests covers reading one request and paging them. The
	// two are one grant for the ordinary reason: they answer the same question
	// at two cardinalities.
	PermissionReadRequests authorization.Permission = "dataprivacy.requests.read"

	// PermissionCancelRequests covers withdrawing a request, which stops an
	// erasure that has not been confirmed and asks one in progress to stop.
	PermissionCancelRequests authorization.Permission = "dataprivacy.requests.cancel"
)

// The routes this surface mounts, as the key a reservation names each by: the
// method, a space, and the path at the default BasePath with its parameter in
// braces — routing's own spelling of a pattern.
const (
	RouteSubmit   = nethttp.MethodPost + " " + BasePath
	RouteList     = nethttp.MethodGet + " " + BasePath
	RouteGet      = nethttp.MethodGet + " " + BasePath + "/{" + pathParam + "}"
	RouteConfirm  = nethttp.MethodGet + " " + BasePath + "/{" + pathParam + "}" + ConfirmSuffix
	RouteCancel   = nethttp.MethodPost + " " + BasePath + "/{" + pathParam + "}" + CancelSuffix
	RouteArtifact = nethttp.MethodGet + " " + BasePath + "/{" + pathParam + "}" + ArtifactSuffix
)

// Permissions is what each guarded route requires, keyed by route.
//
// The routes in OwnStandingRoutes are deliberately absent, and every route
// this surface mounts is in exactly one of the two — permissions_test.go mounts
// the real handlers and checks that in both directions, so a route added later
// and decided about in neither fails there rather than serving whoever asks.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		RouteSubmit: {PermissionSubmitRequests},
		RouteList:   {PermissionReadRequests},
		RouteGet:    {PermissionReadRequests},
		RouteCancel: {PermissionCancelRequests},
	}
}

// OwnStandingRoutes are the routes reached on the strength of what the caller
// is rather than of a grant: the confirmation link, and the artifact download.
//
// It arrives in a mail sent to the person the erasure is about, and the person
// clicking it is the authorization, the way waitlists' confirmation link is.
// The route still resolves the caller and confirms only their own request, so
// the link does nothing in anybody else's hands; what it does not do is ask
// whether the person holds a grant, because a person who asked for their data
// to be erased and was then told they may not confirm it has been refused
// something they are owed.
//
// That makes it a route a deployment cannot reserve to an operator. A
// reservation naming it contradicts what this surface promises its callers,
// and conformance refuses the run that makes it rather than asserting against
// it. A deployment that wants a human click rather than a link leaves it
// unmounted, as the package documentation describes.
//
// The artifact download is the same standing arrived at from the other end:
// it hands a subject the export they asked for, and only theirs. Holding the
// grant to submit an export and then being refused the result would be the
// surface keeping what it owes. It is listed here whether or not a deployment
// mounted it, because what a route promises does not change with whether one
// deployment did.
func OwnStandingRoutes() []string {
	return []string{RouteConfirm, RouteArtifact}
}

// Permissions is the package's Permissions as this Handlers mounts them: keyed
// by the path at the base path it was built with, rather than the default one.
// It is the list to read off a surface mounted with WithBasePath; the
// package-level one names each route the way Seams.OperatorRoutes does.
func (h *Handlers) Permissions() map[string][]authorization.Permission {
	return routeguard.RebaseKeys(Permissions(), BasePath, h.basePath)
}

// OwnStandingRoutes is the package's OwnStandingRoutes as this Handlers mounts
// them, keyed the way its Permissions method is.
func (h *Handlers) OwnStandingRoutes() []string {
	return routeguard.RebaseAll(OwnStandingRoutes(), BasePath, h.basePath)
}

// WithEnforcer supplies the authorization middleware each guarded route is
// checked by — the HTTP counterpart of the authorization interceptor a
// consumer installs on its gRPC server, built over the same grants extractor.
//
// Without one every route in Permissions is refused as 403 rather than served.
// A surface with no way to check a grant is one that has been told nothing
// about who may use it, and the answer to that is the one a fail-closed gRPC
// enforcer gives an undeclared method. The confirmation link is unaffected
// either way.
func WithEnforcer(enforcer *authzhttp.Enforcer) Option {
	return func(o *options) { o.enforcer = enforcer }
}
