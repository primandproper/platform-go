/*
Package pagination asserts that every paged read in this module reports the
filter it actually applied.

A page carries a filtering.v1.Pagination, and its applied_query_filter is
documented as "the filter this page was answered with, after defaults and
bounds — not necessarily the one that was sent". A client paging through a list
reads it to know what it got: how many rows a page holds, which way it runs, and
where it resumed. When it echoes the request instead of the answer, the client
is told about a page it was not served.

Four things are asserted of every paged read, each against the answer rather
than against a number this suite carries:

  - An absent page size is reported as the default.
  - A page size too large for the wire's narrowing is applied the way the
    largest narrowable one is. 65546 wraps to 10 if a surface narrows it before
    clamping, which is the failure filtering/grpc's FromProto exists to prevent;
    asking for it and for 65535 must come back the same. That holds whatever
    ceiling a deployment configured, which is why it is phrased as a comparison.
  - A sort direction is reported normalized: "DESC" is answered as "desc".
  - A cursor is echoed back as the page's previous_cursor.

A read that answers the plain request with something other than a page — an
absence, for a read keyed on an identifier nothing holds — has no pagination to
read, and skips with the code it gave.
*/
package pagination
