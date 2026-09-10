/*
Package protoconvention is where the JSON spelling of every field in every
.proto this module ships is checked against one rule, and it holds nothing else.

The rule is that a field's JSON name is the name the Go type it renders beside
already uses. protobuf derives one of its own — resource_id becomes resourceId —
and the Go types in this module tag that column `json:"resourceID"`, so a
consumer serving the same row over its own JSON handlers and over gRPC-JSON, by
grpc-gateway or protojson, emits two spellings of one field. Neither is wrong on
its own; having both is, and the client has to know which door it came through to
know which one it got.

Only the id suffix is affected, because it is the only place the two derivations
disagree: everywhere else protoc's lower camel and Go's are the same string, and
resource_type is resourceType on both sides. So the rule is protoc's own
derivation with a single substitution — a trailing Id becomes ID and a trailing
Ids becomes IDs — and jsonName in the test is that sentence as a function. A
field spelled plainly id is unaffected: its derivation and its tag are both "id",
which is why the substitution is on the derived camel rather than on the
underscore segments.

# Why the assertion is not per-package

Two of the eleven schemas pinned the overrides and nine pinned none, which is
close enough to a split decision that neither half read as the exception. Each
package's own grpc/ tests passed either way: a package that has never seen the
other spelling has nothing to compare its own against, and the disagreement is
between a Go tag in one package and a descriptor in another. The failure is only
visible from somewhere that can see all eleven at once.

It is also a failure with a deadline. A json_name added to a field that has
shipped changes the JSON wire spelling for every transcoding client, and there is
no /vN to put that in — the descriptor is the contract in every language a
consumer generates into, and a Go major version renames nothing on the wire. It
was free to fix only while none of the nine had shipped in a tag.

# What is checked

Two sweeps, one per description, because the rule is an agreement between them
and either alone can be right while they disagree.

The first is over the schemas: every field of every message, nested messages
included and map entries excluded, in all eleven files. The rule is asserted as
an equality rather than as "an id field carries some override", so it holds in
both directions: an id field that pins nothing fails, and so does one that pins
the wrong spelling or a field that pins a name protoc would have derived anyway.

The second is over this module's Go source: every json struct tag in the tree,
held to the same sentence read backwards. On its own the schema sweep is a
statement about protobuf and none at all about Go — a module whose descriptors
every one said "resourceID" while its structs said `json:"resourceId"` would pass
it and still emit two spellings — so the tags are swept too, and the two
descriptions agree because each conforms to one rule rather than because
something compared them pair by pair.

Pairing is what identity/grpc and waitlists/grpc do, and it is the stronger check
where it exists: it knows which field corresponds to which. It does not
generalize to eleven schemas cheaply, because most of their messages are requests
and responses with no Go type to pair with, and a roster naming both halves of
every pair is a roster that goes stale in the direction nobody notices. The
two-sided rule covers every tag in the module instead, including the ones no
message renders.

"All eleven" is a claim, and a claim about a set is only checkable against an
enumeration of it. The roster in the test names each file by the path it sits at
and maps it to the descriptor its bindings carry, and a second test walks the
tree for .proto files and checks the two against each other in both directions. A
twelfth schema fails here until somebody records it, rather than being the one
file nobody swept.
*/
package protoconvention
