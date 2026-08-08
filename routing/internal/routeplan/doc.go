/*
Package routeplan holds the reflection over a typed route's input: the parsed
path pattern, and the per-input-type plan describing where each field of an In
value lives on the wire.

It exists as a package because the plan is read in two directions. The server
reads a request into an In; routing/client writes an In into a request. Both
need the same field-by-field answer to "is this a path, query, header, or cookie
parameter, and what is it called" — and while that answer lived unexported in
routing, only routing could ask it. A client that could not ask would have to
restate the mapping, which is the drift the typed route exists to prevent.

It is internal rather than exported because the shape below is an agreement
between two packages in this module, not surface a consumer should build
against. Nothing here is part of the platform's API: the exported names are
exported only so that routing and routing/client can both see them.

Direction is the organizing idea. SetScalar reads a request value into a field;
FormatScalar renders a field back into a request value. New builds the plan once
per (input type, pattern, method); both sides then walk the same Params slice.
*/
package routeplan
