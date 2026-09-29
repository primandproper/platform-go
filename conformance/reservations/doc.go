/*
Package reservations asserts that a deployment refuses its members the calls it
says it reserves.

A subject hands its reservation over in Seams.OperatorMethods, and every other
suite reads it to decide who makes each call: an administrator for a reserved
call, a member for the rest. That makes the list a claim the suites act on and
nothing checks. A deployment derives it from somewhere — a permission table, a
role policy — and enforces it somewhere else, in an authorization interceptor,
and the two can drift. A list naming a call the interceptor lets members make
passes every other suite, because no suite ever puts that call in a member's
hands. This one does, with conformance.Attempting.

# What it asserts

For each reserved call on one of this module's surfaces, one subtest:

  - an administrator's empty request is not refused as PermissionDenied or
    Unauthenticated, which is the control;
  - a member's empty request is refused as PermissionDenied.

The request is empty on purpose. A reservation is refused in an interceptor
before a handler runs, so it cannot depend on what the request says, and a
suite that filled requests would need to know every method it asserts. A member
answered InvalidArgument fails with that ordering named: a deployment that
validates before it authorizes tells non-staff the shape of a call they may
not make.

Streaming methods are opened, closed and read once rather than invoked, so a
deployment that enforces its streams in a different interceptor from its unary
calls is held to the same list.

# Why the control, and where it is skipped

Without it the refusal proves nothing: a method refusing everybody refuses a
member, and so does a handler that answers an empty request with
PermissionDenied. identity's membership authorizer is one — an empty user or
account id is a target the caller is not permitted — so for some calls the
administrator's empty request is refused exactly as the member's is. There the
entry skips, saying that a reservation cannot be told from the handler, rather
than guessing a request that would get past it.

# What it does not assert

A reserved call on a service this module does not ship. What this suite
promises is about this module's surfaces, and on a consumer's own service an
empty request is no control: it may be a valid command, or a request its
handler cannot survive. That the deployment refuses such a call to a member is
the consumer's own test, beside its test that an operator may make it, and the
entry skips saying so.

Nor, yet, a route on one of the HTTP surfaces. Seams.OperatorMethods names gRPC
methods, and there is no seam by which a deployment says which routes it
reserves, so there is no list to hold it to.

Nothing here counts. Each entry is its own subtest, so a reservation of any
length is asserted entry by entry.
*/
package reservations
