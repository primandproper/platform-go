package grpc

import (
	"context"

	"github.com/primandproper/primitives-go/v2/authorization"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
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
// waitlists.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary, and waitlists/privacy's collector sets the
// flag in order to export an archived signup, which still holds the address it
// was made with. This is a ruling about the wire.

// archiveGrants reads the caller's authority, reporting whether it could be
// determined at all.
//
// A server built with no [WithGrantsExtractor] answers false, which is the
// fail-closed half of the default: a surface that cannot see what the caller may
// do cannot tell an operator from a visitor, and the expensive way to be wrong
// about that is to guess "operator". A consumer who wants an operator's archived
// rows on the wire supplies the same authorization.GrantsExtractor they already
// hand primitives-go's authorization/grpc enforcer.
func (s *Server) archiveGrants(ctx context.Context) (authorization.Grants, bool) {
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
func (s *Server) confineToLive(
	ctx context.Context,
	req *request,
	filter *filtering.QueryFilter,
	archiveGrant authorization.Permission,
) {
	if filter == nil || filter.IncludeArchived == nil || !*filter.IncludeArchived {
		return
	}

	if grants, ok := s.archiveGrants(ctx); ok && grants.Has(archiveGrant) {
		return
	}

	filter.IncludeArchived = nil

	req.op.Set(archivedClearedKey, true)
}

// readFilter reads the page a request asked for, and confines it to what the
// caller may be shown.
//
// It is a method with a description rather than four copies of the same eight
// lines, because the four paged reads share both failures: a filter no converter
// can read is the client's to fix and is InvalidArgument rather than the
// Internal every other call site here passes, and a request for archived rows is
// a request this surface answers in exactly one place.
//
// The grant is an argument rather than a constant because this surface pages two
// nouns and archives them under two grants; see the file comment.
func (s *Server) readFilter(
	ctx context.Context,
	req *request,
	in *filteringpb.QueryFilter,
	archiveGrant authorization.Permission,
	description string,
) (*filtering.QueryFilter, error) {
	filter, err := filteringgrpc.FromProto(in)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "%s", description)
	}

	s.confineToLive(ctx, req, filter, archiveGrant)

	return filter, nil
}
