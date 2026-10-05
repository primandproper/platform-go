package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
)

// Archived lists and archived signups are behind the grants that archive them,
// on every paged read this surface serves.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire. A caller holding
// [PermissionReadSignups] and nothing else who sent include_archived: true
// received every signup somebody had withdrawn — [PermissionArchiveSignups] is
// its own grant, and a read that hands the retired rows back under the read
// grant makes that grant a name and not a policy.
//
// So the field is honored for a caller who holds the grant that archives what
// the read returns, and cleared for everybody else, before the filter reaches
// the store.
//
// # Two grants, because this surface archives two nouns
//
// A list is retired under [PermissionArchiveLists] and a signup under
// [PermissionArchiveSignups], and they are deliberately separate — retiring a
// waitlist and retiring somebody's place on one are different acts. So each read
// asks about the noun it pages rather than about the surface: the list reads ask
// for the first and the signup reads for the second, and an operator who holds
// one does not see through the other.
//
// [Server.ListOpenLists] is a list read like any other and is under the same
// rule, which on the public RPC means the field is always cleared: a visitor on
// a signup page carries no grants at all, so a request for the archived catalog
// is a request nobody anonymous can make.
//
// # It is a narrowing and not a refusal
//
// The field is cleared rather than the read refused, and the clearing is
// recorded on the read's span. That half of the rule is every surface's, and
// internal/archivegate is where it is written once.
//
// # What this does not reach
//
// waitlists.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary, and waitlists/privacy's collector sets the
// flag in order to export an archived signup, which still holds the address it
// was made with. This is a ruling about the wire.

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
