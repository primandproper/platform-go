package grpc

import (
	"context"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
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
// ListDevices has no archived dimension for this file to rule about and asks
// nothing of it.
//
//
// # It is a narrowing and not a refusal
//
// A read that failed because the caller asked for too much turns a console's
// checkbox into an error, and a client cannot tell that refusal from a broken
// filter. Clearing the field answers with the live rows, which is what the read
// grant entitled the caller to ask for, and the page they receive is the page
// they would have received had they never set it.
//
// The clearing is recorded on the read's span, because "my archived rows stopped
// arriving" is otherwise a question an operator can only answer by reading this
// file.
//
// # What this does not reach
//
// notifications.Store's own IncludeArchived is untouched. A Go caller holding
// the store is inside the trust boundary, and notifications/privacy's collector
// sets the flag in order to export a notification a subject received and later
// dismissed. This is a ruling about the wire.

// callerGrants reads the caller's authority, reporting whether it could be
// determined at all.
//
// A server built with no [WithGrantsExtractor] answers false, which is the
// fail-closed half of the default: a surface that cannot see what the caller may
// do cannot tell somebody who may see the archived rows from somebody who may
// not, and the expensive way to be wrong about that is to guess. A consumer who
// wants the archived rows on the wire supplies the same
// authorization.GrantsExtractor they already hand primitives-go's
// authorization/grpc enforcer.
func (s *Server) callerGrants(ctx context.Context) (authorization.Grants, bool) {
	if s.grants == nil {
		return authorization.DenyAll(), false
	}

	return s.grants(ctx)
}

// confineToLive drops a filter's IncludeArchived unless the caller holds
// [PermissionArchiveInbox].
//
// It clears the field rather than writing false into it, so what reaches the
// store is the filter of a caller who never asked — the same value every read
// that omits the field already sends, and one this package cannot get out of
// step with whatever the store's default for an absent field becomes.
func (s *Server) confineToLive(ctx context.Context, req *request, filter *filtering.QueryFilter) {
	if filter == nil || filter.IncludeArchived == nil || !*filter.IncludeArchived {
		return
	}

	if grants, ok := s.callerGrants(ctx); ok && grants.Has(PermissionArchiveInbox) {
		return
	}

	filter.IncludeArchived = nil

	req.op.Set(archivedClearedKey, true)
}
