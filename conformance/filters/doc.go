/*
Package filters asserts that every paged read in this module refuses a malformed
filter, rather than answering a page it half-applied.

filtering/grpc's FromProto reports a filter it could not read and hands back a
usable one anyway — every field it could decode applied, the rest left at their
defaults — so a surface that logged the error and listed regardless would answer
a caller who asked for "sideways" with an ascending page, and a caller whose
window was garbage with no window at all. Both are answers to questions nobody
asked. The promise is that the error is the answer: InvalidArgument.

It is one suite over every paged read rather than a test in each surface,
because the promise is one sentence and the reads are found from the
descriptors — every RPC whose request carries a filtering.v1.QueryFilter — so a
read added later is covered without anybody coming back here.

# The positive control

A read refused as InvalidArgument for a missing account, target or subject would
pass this suite for the wrong reason. So each read is first called with the same
request and a well-formed filter, and that call must not be InvalidArgument; only
then is the malformed one asserted. The requests come from
conformance/internal/pagedrpc, which writes out the ones that need more than a
filter and checks that none is missing.
*/
package filters
