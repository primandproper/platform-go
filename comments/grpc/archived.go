package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
)

// Archived comments are behind the grant that archives them, on every paged
// read this surface serves.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire. A caller holding
// [PermissionReadComments] and nothing else who sent include_archived: true
// received the body of every comment a moderator had removed —
// [PermissionArchiveComments] is split from [PermissionUpdateComments] because
// removing something somebody said and rewriting it are different amounts of
// trust, and a read that hands the removed text back under the read grant makes
// that split a name and not a policy.
//
// So the field is honored for a caller who holds [PermissionArchiveComments]
// and cleared for everybody else, before the filter reaches the store.
//
// # It is a narrowing and not a refusal
//
// The field is cleared rather than the read refused, and the clearing is
// recorded on the read's span. That half of the rule is every surface's, and
// internal/archivegate is where it is written once.
//
// # What this does not reach
//
// comments.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary — comments.Store.ArchiveComment's
// documentation says as much, and comments/privacy's collector sets the flag in
// order to export what a subject wrote and had removed. This is a ruling about
// the wire.

// readFilter reads the page a request asked for, confined to what the caller
// may be shown: include_archived is honored for a caller holding
// [PermissionArchiveComments] and cleared for everybody else.
func (s *Server) readFilter(
	ctx context.Context,
	req *request,
	in *filteringpb.QueryFilter,
	description string,
) (*filtering.QueryFilter, error) {
	return archivegate.Filter(ctx, req.op, in, s.grants, PermissionArchiveComments, archivedClearedKey, description)
}
