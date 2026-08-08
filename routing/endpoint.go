package routing

// Endpoint is a route stated as a value: one method, one typed pattern, and the
// In and Out types the operation speaks. It is what a registration call takes,
// where Route is what one returns.
//
//	var GetOrder = routing.Endpoint[GetOrderRequest, Order]{
//		Method:  http.MethodGet,
//		Pattern: "/orders/{orderID:uuid}",
//	}
//
// Making the route a value is what lets a service publish its API as Go rather
// than describe it twice. A package of Endpoints plus their In/Out types is the
// whole contract: the service registers them with Register, and a Go consumer
// imports the same package and calls them with routing/client, which reads the
// pattern and the input tags to build the request. Neither side can drift from
// the other on a path or a field name, because there is one statement of each.
//
// It carries the route and not the route's presentation. Everything a server
// decides for itself — the summary, the tags, the success status, the middleware
// — stays in the Options passed alongside it, because none of it changes what a
// caller has to send or what it gets back.
type Endpoint[In, Out any] struct {
	// Method is the HTTP method, e.g. http.MethodGet.
	Method string
	// Pattern is the typed path pattern, with inline type annotations on path
	// parameters: "/orgs/{orgID:uint64}/users/{userID:uuid}".
	Pattern string
}
