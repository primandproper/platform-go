package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/internal/archivegate"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
)

// Retired endpoints and subscriptions are behind the grants that retire them,
// on the two paged reads this surface serves over them.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire, and until this file
// it reached the store untouched. A caller holding [PermissionReadEndpoints] and
// nothing else who sent include_archived: true received every endpoint somebody
// had retired — including its URL, which is the delivery target a deployment
// stopped trusting. [PermissionArchiveEndpoints] is a grant of its own precisely
// because retiring one is an act of its own, and a read that hands the retired
// rows back under the read grant makes that split a name and not a policy.
//
// So the field is honored for a caller who holds the grant that archives what
// the read returns, and cleared for everybody else, before the filter reaches
// the store.
//
// # Two grants, because this surface archives two nouns
//
// An endpoint is retired under [PermissionArchiveEndpoints] and a subscription
// under [PermissionArchiveSubscriptions]. A delivery attempt is neither: a
// webhooks.Attempt has no ArchivedAt, being reaped on a schedule rather than
// retired by anybody, so ListAttempts has no archived dimension to rule about.
// It names archivegate.NothingArchived rather than skipping the question, so
// an attempt that comes to be retired in place is served live until somebody
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
// webhooks.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary, and the delivery worker's own reads are
// the component servicing itself. This is a ruling about the wire.

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
