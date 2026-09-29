package routeguard

import (
	"maps"
	"path"
	"strings"
)

// Rebase re-keys a route spelled at defaultBase onto base, the path its surface
// was built to mount at, the way that surface's Mount joins them: the
// collection route is the base itself, and every other route is joined to it.
func Rebase(route, defaultBase, base string) string {
	method, pattern, _ := strings.Cut(route, " ")

	rest := strings.TrimPrefix(pattern, defaultBase)
	if rest == "" {
		return method + " " + base
	}

	return method + " " + path.Join(base, rest)
}

// RebaseKeys is Rebase over every key of routes, into a fresh map.
func RebaseKeys[V any](routes map[string]V, defaultBase, base string) map[string]V {
	rebased := make(map[string]V, len(routes))
	for route, v := range maps.All(routes) {
		rebased[Rebase(route, defaultBase, base)] = v
	}

	return rebased
}

// RebaseAll is Rebase over every route, into a fresh slice.
func RebaseAll(routes []string, defaultBase, base string) []string {
	rebased := make([]string, 0, len(routes))
	for _, route := range routes {
		rebased = append(rebased, Rebase(route, defaultBase, base))
	}

	return rebased
}
