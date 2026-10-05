package http

import (
	nethttp "net/http"

	"github.com/primandproper/platform-go/v15/internal/routeguard"

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
// consumer's policy, and a deployment that keeps a route from its members does
// so by not granting its permission — which is the whole of reserving an HTTP
// route to an operator.
const (
	// PermissionListOperations covers paging every operation the caller's
	// owners hold.
	//
	// It is the enumeration, and it is separate from following one operation
	// on purpose: reading an operation is reached by its identifier, which the
	// caller was handed when the work was started, and a deployment may well
	// let every member follow their own work while keeping the console view of
	// everything running in a tenant to its staff.
	PermissionListOperations authorization.Permission = "operations.list"

	// PermissionCancelOperations covers asking an operation to stop.
	//
	// It is the one write here, and the most likely route for a deployment to
	// keep: an operation that should run to completion is one nobody but an
	// operator stops.
	PermissionCancelOperations authorization.Permission = "operations.cancel"
)

// The routes this surface mounts, as the key a reservation names each by: the
// method, a space, and the path at the default BasePath with its parameter in
// braces — routing's own spelling of a pattern.
const (
	RouteList   = nethttp.MethodGet + " " + BasePath
	RouteGet    = nethttp.MethodGet + " " + BasePath + "/{" + pathParam + "}"
	RouteCancel = nethttp.MethodPost + " " + BasePath + "/{" + pathParam + "}" + CancelSuffix
	RouteEvents = nethttp.MethodGet + " " + BasePath + "/{" + pathParam + "}" + EventsSuffix
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
		RouteList:   {PermissionListOperations},
		RouteCancel: {PermissionCancelOperations},
	}
}

// OwnStandingRoutes are the routes reached on the strength of what the caller
// is rather than of a grant: following one operation, by polling it or by
// subscribing to it.
//
// They are the paths a 202 hands back — Accepted's Location and Events, and
// dataprivacy's Receipt, which points a person at the operation fulfilling
// their own export or erasure. The identifier is the caller's handle on work
// they started or that was started on their behalf, and the owners the
// resolver answers are what confine it: an operation belonging to anybody else
// reads as absent, grant or no grant. A deployment that required a grant here
// would be telling a person who asked for their data erased that they may not
// watch it happen.
//
// That makes them routes a deployment cannot reserve to an operator. A
// reservation naming one contradicts what this surface promises its callers,
// and conformance refuses the run that makes it rather than asserting against
// it.
func OwnStandingRoutes() []string {
	return []string{RouteGet, RouteEvents}
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
// enforcer gives an undeclared method. The routes in OwnStandingRoutes are
// unaffected either way.
func WithEnforcer(enforcer *authzhttp.Enforcer) Option {
	return func(o *options) { o.enforcer = enforcer }
}
