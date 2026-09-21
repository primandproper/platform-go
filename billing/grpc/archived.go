package grpc

import (
	"context"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
)

// Archived rows are behind the grants that archive them, on every paged read
// this surface serves.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire, and until this file
// it reached the store untouched. A caller holding [PermissionReadProducts] and
// nothing else who sent include_archived: true received every product the
// catalog had withdrawn; one holding [PermissionReadSubscriptions] received
// every subscription somebody had ended. Each of those is its own grant
// precisely because retiring a row is an act of its own, and a read that hands
// the retired rows back under the lighter grant makes those grants names and
// not policy.
//
// So the field is honored for a caller who holds the grant that archives what
// the read returns, and cleared for everybody else, before the filter reaches
// the store.
//
// # Four grants, because this surface pages four nouns
//
// A product is retired under [PermissionArchiveProducts], a subscription under
// [PermissionArchiveSubscriptions], a purchase under
// [PermissionArchivePurchases] and a transaction under
// [PermissionArchiveTransactions]. Each read asks for the one that archives what
// it pages, which is why confineToLive takes the grant rather than naming one.
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
// billing.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary, and billing/privacy's collector sets the
// flag in order to export a row a subject once held and later gave up. This is
// a ruling about the wire.

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

// confineToLive drops a filter's IncludeArchived unless the caller holds the
// grant that archives the noun this read pages.
//
// It clears the field rather than writing false into it, so what reaches the
// store is the filter of a caller who never asked — the same value every read
// that omits the field already sends, and one this package cannot get out of
// step with whatever the store's default for an absent field becomes.
//
// The grant is an argument rather than a constant because this surface pages
// several nouns and archives them under several grants; see the file comment.
func (s *Server) confineToLive(
	ctx context.Context,
	req *request,
	filter *filtering.QueryFilter,
	archiveGrant authorization.Permission,
) {
	if filter == nil || filter.IncludeArchived == nil || !*filter.IncludeArchived {
		return
	}

	if grants, ok := s.callerGrants(ctx); ok && grants.Has(archiveGrant) {
		return
	}

	filter.IncludeArchived = nil

	req.op.Set(archivedClearedKey, true)
}
