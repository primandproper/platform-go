package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
)

// Retired definitions and cleared values are behind the grants that retire and
// clear them, on every paged read this surface serves.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire. A caller holding
// [PermissionReadDefinitions] and nothing else who sent include_archived: true
// received every setting somebody had withdrawn from the catalog, and one
// holding [PermissionReadAllValues] received every answer somebody had taken
// back. Both are their own grants precisely because retiring and clearing are
// acts of their own, and a read that hands the retired rows back under a lighter
// grant makes those grants names and not policy.
//
// So the field is honored for a caller who holds the grant that archives what
// the read returns, and cleared for everybody else, before the filter reaches
// the store.
//
// # Two grants, because this surface archives two nouns
//
// A definition is retired under [PermissionArchiveDefinitions]. A value is not
// retired at all in that vocabulary — it is *cleared*, by
// [Server.ClearValue], under [PermissionWriteValues], which is the grant this
// package already says covers "a subject answering a setting and taking the
// answer back". So that is the grant the two value reads ask for: whoever may
// take an answer back may see the answers that were taken back, and whoever may
// not sees the answers that stand.
//
// [Server.ListValuesForSubject] asks it in addition to [SubjectAuthorizer] and
// not instead of it. The authorizer says whose values these are and this says
// whether the cleared ones are among them; a caller may hold write on their own
// settings and still be refused somebody else's page entirely.
//
// # It is a narrowing and not a refusal
//
// The field is cleared rather than the read refused, and the clearing is
// recorded on the read's span. That half of the rule is every surface's, and
// internal/archivegate is where it is written once.
//
// # What this does not reach
//
// settings.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary, and settings/privacy's collector sets the
// flag in order to export a value a subject once chose and later cleared. This
// is a ruling about the wire.

// readFilter reads the page a request asked for, confined to what the caller
// may be shown: include_archived is honored for a caller holding archiveGrant,
// the grant that archives the noun this read pages, and cleared for everybody
// else.
//
// The grant is an argument rather than a constant because this surface pages
// several nouns and archives them under several grants; see the file comment.
func (s *Server) readFilter(
	ctx context.Context,
	req *request,
	in *filteringpb.QueryFilter,
	archiveGrant authorization.Permission,
	description string,
) (*filtering.QueryFilter, error) {
	return archivegate.Filter(ctx, req.op, in, s.grants, archiveGrant, archivedClearedKey, description)
}
