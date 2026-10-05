package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
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
// it pages, which is why readFilter takes the grant rather than naming one.
//
// # It is a narrowing and not a refusal
//
// The field is cleared rather than the read refused, and the clearing is
// recorded on the read's span. That half of the rule is every surface's, and
// internal/archivegate is where it is written once.
//
// # What this does not reach
//
// billing.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary, and billing/privacy's collector sets the
// flag in order to export a row a subject once held and later gave up. This is
// a ruling about the wire.

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
