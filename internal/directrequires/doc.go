/*
Package directrequires is where go.mod's account of which modules this module's
own source imports is checked against the source, and it holds nothing else.

An `// indirect` comment is not documentation. It is a claim — that no file in
this module imports the module on that line — derived by `go mod tidy` from the
import graph, and true only of the tree the last tidy ran against. Nothing reads
it afterwards. A stale claim builds, tests, lints, vets and tags exactly like a
current one, in either direction, so the file goes on describing a tree that
moved on underneath it until somebody happens to read it.

Somebody did. The notifications/async subtree left for primitives-go with the
rest of the packages that own no table, and took with it the only imports of
ably-go, gorilla/websocket and pusher-http-go; all three require lines stayed in
the direct block behind it, and what found them was a person reading go.mod
during a pre-publish review — a survey with a shelf life of one branch, which is
the shape this module keeps replacing with a test.

# Why this is not only the workflow step

The generated-files workflow runs `go mod tidy` and fails on the diff, and that
is the authoritative check: tidy settles versions and go.sum as well as the
direct/indirect split, and only tidy can. It needs the network and the whole
module graph to do it, which is why it is a job on a runner.

This test needs neither. It reads go.mod and parses the imports of the tree
beside it, so it runs in `make test` with everything else — before a push rather
than after one — and a failure names the module and which direction it drifted
in, rather than handing over a diff of the file and leaving the reader to find
the three lines in it that moved.

# What is checked

Both directions, because the claim can be wrong in both. A require outside the
indirect block that nothing imports is the drift above: a dependency this module
no longer has, still pinned as though a choice were being made about it. A
require inside it that something imports is the same drift arriving from the
other side, and the worse one: a module this module's own source names, whose
version is whatever some other module in the graph happens to ask for, and which
disappears from go.mod entirely on the day that module stops asking.

Ownership is by longest matching path prefix, which is how an import path and a
module path relate. go.opentelemetry.io/otel/metric is its own module with its
own require line, so an import of it is not an import of go.opentelemetry.io/otel
and does not answer for that line.

Every .go file in the tree is parsed, test files included and build constraints
not evaluated, which is close to the set tidy considers: tidy loads every build
configuration too, and the one file it would leave out that this walk takes in is
one no configuration builds.
*/
package directrequires
