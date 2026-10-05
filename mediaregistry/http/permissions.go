package http

import (
	nethttp "net/http"

	"github.com/primandproper/platform-go/v15/internal/routeguard"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
)

// PermissionReadObjects covers fetching a stored object's bytes, and is the
// one permission this surface declares.
//
// It is declared here rather than in the authorization package for the reason
// every gRPC surface's are: authorization owns what a Permission is and no
// domain's names. Platform declares it and grants it to nobody. Whether a
// member holds it is the consumer's policy, and a deployment that serves its
// stored objects to its staff alone does so by not granting it.
//
// It decides whether a caller may use this route at all. Which objects they
// may have once they are through it is the Entitlement's to say, per row, and
// a grant widens nothing: a caller holding it is still refused an object the
// Entitlement declines, as the same 404 an absent one gets.
const PermissionReadObjects authorization.Permission = "mediaregistry.objects.read"

// RouteServe is the route this surface mounts, as the key a reservation names
// it by: the method, a space, and the path at the default BasePath with its
// parameter in braces — routing's own spelling of a pattern.
const RouteServe = nethttp.MethodGet + " " + BasePath + "/{" + pathParam + "}"

// Permissions is what each guarded route requires, keyed by route.
//
// Every route this surface mounts is in exactly one of this and
// OwnStandingRoutes; permissions_test.go mounts the real handler and checks
// that in both directions. It returns a fresh map each call, so a consumer
// composing it into their own policy is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		RouteServe: {PermissionReadObjects},
	}
}

// OwnStandingRoutes are the routes reached on the strength of what the caller
// is rather than of a grant, and this surface has none.
//
// An object's owner reading it back might look like one, but it is not reached
// by anything the caller holds apart from their session — no link, no token —
// and an object's reader need not be its owner once a deployment supplies an
// Entitlement of its own. So the route is a grant's like any other, and a
// deployment may reserve it. The function exists so the answer is spelled out
// beside the other two HTTP surfaces' rather than left to be inferred.
func OwnStandingRoutes() []string { return nil }

// Permissions is the package's Permissions as this Handler mounts them: keyed
// by the path at the base path it was built with, rather than the default one.
// It is the list to read off a surface mounted with WithBasePath; the
// package-level one names each route the way Seams.OperatorRoutes does.
func (h *Handler) Permissions() map[string][]authorization.Permission {
	return routeguard.RebaseKeys(Permissions(), BasePath, h.basePath)
}

// OwnStandingRoutes is the package's OwnStandingRoutes as this Handler mounts
// them, keyed the way its Permissions method is.
func (h *Handler) OwnStandingRoutes() []string {
	return routeguard.RebaseAll(OwnStandingRoutes(), BasePath, h.basePath)
}

// WithEnforcer supplies the authorization middleware each guarded route is
// checked by — the HTTP counterpart of the authorization interceptor a
// consumer installs on its gRPC server, built over the same grants extractor.
//
// Without one the route is refused as 403 rather than served. A surface with
// no way to check a grant is one that has been told nothing about who may use
// it, and the answer to that is the one a fail-closed gRPC enforcer gives an
// undeclared method.
func WithEnforcer(enforcer *authzhttp.Enforcer) Option {
	return func(o *options) { o.enforcer = enforcer }
}
