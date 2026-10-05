package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
)

// Dismissed notifications are behind the grant that dismisses them, on both
// paged inbox reads this surface serves.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire, and until this file
// it reached the store untouched. A caller holding [PermissionReadInbox] and
// nothing else who sent include_archived: true received every notification
// somebody had dismissed. [PermissionArchiveInbox] is a grant of its own
// precisely because dismissing is an act of its own, and a read that hands the
// dismissed rows back under the read grant makes that split a name and not a
// policy.
//
// So the field is honored for a caller who holds [PermissionArchiveInbox] and
// cleared for everybody else, before the filter reaches the store.
//
// # One grant, because this surface archives one noun
//
// The device registry pages no archived rows: a notifications.Device has no
// ArchivedAt, being revoked by removal rather than retired in place, so
// ListDevices has no archived dimension to rule about. It names
// archivegate.NothingArchived rather than skipping the question, so a registry
// that starts retiring devices in place serves the live ones until somebody
// names the grant.
//
// # It is a narrowing and not a refusal
//
// The field is cleared rather than the read refused, and the clearing is
// recorded on the read's span. That half of the rule is every surface's, and
// internal/archivegate is where it is written once.
//
// # What this does not reach
//
// notifications.Store's own IncludeArchived is untouched. A Go caller holding
// the store is inside the trust boundary, and notifications/privacy's collector
// sets the flag in order to export a notification a subject received and later
// dismissed. This is a ruling about the wire.

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
