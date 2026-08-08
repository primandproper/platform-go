package routing

import (
	"fmt"

	"github.com/primandproper/platform-go/v10/routing/internal/routeplan"
)

// ParamSpec is a path parameter parsed out of a typed-path pattern such as
// "/users/{id:uint64}". Token is the resolved type token ("string" when the
// pattern omitted an annotation).
type ParamSpec = routeplan.ParamSpec

// parsePath splits a typed-path pattern into a plain pattern (all type
// annotations stripped, safe to hand to any router) and the list of path
// parameters with their resolved tokens. It panics on an unknown token — a
// static programmer error surfaced at registration time, matching how chi
// panics on malformed patterns.
func parsePath(pattern string) (plain string, params []ParamSpec) {
	plain, params, err := routeplan.ParsePath(pattern)
	if err != nil {
		panic(fmt.Sprintf("routing: %s", err))
	}

	return plain, params
}
