/*
Package client calls a routing.Endpoint over HTTP, with no generated code.

A typed route already states everything a caller needs. The pattern says where
it lives, the input's struct tags say which fields are path, query, header, and
cookie parameters and which make up the body, and the output type says what
comes back. routing reads that in one direction to serve a request; this package
reads the same thing in the other direction to send one:

	// Published once, by the service, and imported by both sides.
	var GetOrder = routing.Endpoint[GetOrderRequest, Order]{
		Method:  http.MethodGet,
		Pattern: "/orders/{orderID:uuid}",
	}

	// Server.
	routing.Register(r, GetOrder, getOrderHandler)

	// Consumer, in another repository.
	c, err := client.New("https://orders.internal")
	order, err := client.Call(ctx, c, GetOrder, GetOrderRequest{OrderID: id})

There is one statement of the path and one of every field name, so the two sides
cannot disagree about either. Renaming a query parameter is a compile error at
every call site rather than a 400 in production.

# Errors

A non-2xx response comes back as an *Error, carrying the status, the platform
error code, and the message. Where the code names exactly one platform sentinel,
the *Error unwraps to it, so a caller branches the same way it would on a local
call:

	order, err := client.Call(ctx, c, GetOrder, req)
	if errors.Is(err, sql.ErrNoRows) {
		// the service answered 404 / E104
	}

The codes that round-trip are the ones errors/http.ErrorForCode names — the ones
whose forward mapping is a bijection. For everything else the *Error still
carries the status and code to branch on; it just does not claim to know which
error produced them.

# Transport

The *http.Client is the caller's, which is the whole retry, circuit-breaker,
rate-limiter, response-cache, and request-signing story: build one with the
platform's httpclient package and hand it over.

	hc, err := httpclient.NewHTTPClient(httpclient.WithRetryPolicy(policy), ...)
	c, err := client.New(baseURL, client.WithHTTPClient(hc))

This package adds none of that itself. It turns a value into a request and a
response back into a value; everything between the two is transport, and
httpclient is where transport lives.

# Path values with reserved characters

Path parameters go on the wire percent-escaped, so a value containing a slash
addresses one segment rather than two. Whether the server puts it back together
depends on which routing backend it runs: the stdlib ServeMux decodes a path
value, chi hands the handler the raw escaped text, and gin and httprouter route
the escaped form to nothing. Keep values that need escaping out of the path —
they belong in the query, where every backend agrees — until that is settled.
*/
package client
