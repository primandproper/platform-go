// Package archivegate is the one door from a wire filter to a store filter on
// this module's gRPC surfaces, and the archive grant every paged read has to
// name to pass through it.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire. A read that passes
// it through to a store that honors it hands a caller holding the read grant
// alone every row somebody holding a stronger grant took out of the answer —
// which makes the archive grant a name rather than a policy. So the field is
// honored for a caller who holds the grant that archives what the read returns,
// and cleared for everybody else, before the filter reaches the store.
//
// # It is a narrowing and not a refusal
//
// A read that failed because the caller asked for too much turns a console's
// checkbox into an error, and a client cannot tell that refusal from a broken
// filter. Clearing the field answers with the live rows, which is what the read
// grant entitled the caller to ask for, and the page they receive is the page
// they would have received had they never set it.
//
// The field is cleared rather than set to false, so what reaches the store is
// the filter of a caller who never asked — the value every read that omits the
// field already sends, and one this package cannot get out of step with
// whatever a store's default for an absent field becomes.
//
// The clearing is recorded on the read's span, under the key the surface names,
// because "my archived rows stopped arriving" is otherwise a question an
// operator can only answer by reading this package.
//
// # Why it is one package and a roster beside it
//
// The rule existed seven times as near-identical files, one per surface, and
// two surfaces were missed because nothing made it unavoidable: each converted
// its filter with primitives-go's filtering/grpc.FromProto, and the narrowing
// was a second call a handler had to remember. [Filter] is that conversion with
// the archive decision as a parameter, so a read cannot convert a filter without
// naming one, and this package's roster test fails a module whose code calls
// FromProto anywhere but here, or serves a filtered RPC whose handler never
// reaches [Filter].
//
// An MCP tool's filter arrives decoded rather than as a protobuf message, so it
// enters at [Narrow], the half of [Filter] that decides, through
// internal/mcptool — one door for every transport rather than a second copy of
// the rule beside the tools.
//
// A read whose rows are never archived names [NothingArchived] rather than
// skipping the call. That is a decision recorded where it can be read, and it
// clears the field for everybody — so a store that starts archiving those rows
// later serves the live ones until somebody names the grant.
//
// # What this does not reach
//
// A store's own IncludeArchived is untouched. A Go caller holding a store is
// inside the trust boundary, and the privacy collectors set the flag in order
// to export rows a subject once held. This is a ruling about the wire.
package archivegate
