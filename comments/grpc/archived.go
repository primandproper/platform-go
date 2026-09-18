package grpc

import (
	"context"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
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
// comments.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary — comments.Store.ArchiveComment's
// documentation says as much, and comments/privacy's collector sets the flag in
// order to export what a subject wrote and had removed. This is a ruling about
// the wire.

// archiveGrants reads the caller's authority, reporting whether it could be
// determined at all.
//
// A server built with no [WithGrantsExtractor] answers false, which is the
// fail-closed half of the default: a surface that cannot see what the caller may
// do cannot tell a moderator from anybody else, and the expensive way to be
// wrong about that is to guess "moderator". A consumer who wants an operator's
// archived rows on the wire supplies the same authorization.GrantsExtractor they
// already hand primitives-go's authorization/grpc enforcer.
func (s *Server) archiveGrants(ctx context.Context) (authorization.Grants, bool) {
	if s.grants == nil {
		return authorization.DenyAll(), false
	}

	return s.grants(ctx)
}

// confineToLive drops a filter's IncludeArchived unless the caller holds
// [PermissionArchiveComments].
//
// It clears the field rather than writing false into it, so what reaches the
// store is the filter of a caller who never asked — the same value every read
// that omits the field already sends, and one this package cannot get out of
// step with whatever the store's default for an absent field becomes.
func (s *Server) confineToLive(ctx context.Context, req *request, filter *filtering.QueryFilter) {
	if filter == nil || filter.IncludeArchived == nil || !*filter.IncludeArchived {
		return
	}

	if grants, ok := s.archiveGrants(ctx); ok && grants.Has(PermissionArchiveComments) {
		return
	}

	filter.IncludeArchived = nil

	req.op.Set(archivedClearedKey, true)
}
