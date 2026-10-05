package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
)

// Archived reports are behind the grant that archives them, on every paged read
// this surface serves.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire. A caller holding
// [PermissionReadReports] or [PermissionTriageReports] and nothing else who sent
// include_archived: true received every report somebody had taken out of the
// queue — [PermissionArchiveReports] is its own grant, and a read that hands the
// removed rows back under a lighter one makes that grant a name and not a
// policy.
//
// So the field is honored for a caller who holds [PermissionArchiveReports] and
// cleared for everybody else, before the filter reaches the store.
//
// # It is a narrowing and not a refusal
//
// The field is cleared rather than the read refused, and the clearing is
// recorded on the read's span. That half of the rule is every surface's, and
// internal/archivegate is where it is written once.
//
// # What this does not reach
//
// issuereports.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary, and issuereports/privacy's collector sets
// the flag in order to export what a subject filed and somebody archived. This
// is a ruling about the wire.

// readFilter reads the page a request asked for, confined to what the caller
// may be shown: include_archived is honored for a caller holding
// [PermissionArchiveReports] and cleared for everybody else.
func (s *Server) readFilter(
	ctx context.Context,
	req *request,
	in *filteringpb.QueryFilter,
	description string,
) (*filtering.QueryFilter, error) {
	return archivegate.Filter(ctx, req.op, in, s.grants, PermissionArchiveReports, archivedClearedKey, description)
}
