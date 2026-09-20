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

// Retired definitions and cleared values are behind the grants that retire and
// clear them, on every paged read this surface serves.
//
// # Why the flag is a request rather than an instruction
//
// filtering.QueryFilter.IncludeArchived arrives on the wire. A caller holding
// [PermissionReadDefinitions] and nothing else who sent include_archived: true
// received every setting somebody had withdrawn from the catalog, and one
// holding [PermissionReadAllValues] received every answer somebody had taken
// back. Both are their own grants precisely because retiring and clearing are
// acts of their own, and a read that hands the retired rows back under a lighter
// grant makes those grants names and not policy.
//
// So the field is honored for a caller who holds the grant that archives what
// the read returns, and cleared for everybody else, before the filter reaches
// the store.
//
// # Two grants, because this surface archives two nouns
//
// A definition is retired under [PermissionArchiveDefinitions]. A value is not
// retired at all in that vocabulary — it is *cleared*, by
// [Server.ClearValue], under [PermissionWriteValues], which is the grant this
// package already says covers "a subject answering a setting and taking the
// answer back". So that is the grant the two value reads ask for: whoever may
// take an answer back may see the answers that were taken back, and whoever may
// not sees the answers that stand.
//
// [Server.ListValuesForSubject] asks it in addition to [SubjectAuthorizer] and
// not instead of it. The authorizer says whose values these are and this says
// whether the cleared ones are among them; a caller may hold write on their own
// settings and still be refused somebody else's page entirely.
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
// settings.Store's own IncludeArchived is untouched. A Go caller holding the
// store is inside the trust boundary, and settings/privacy's collector sets the
// flag in order to export a value a subject once chose and later cleared. This
// is a ruling about the wire.

// callerGrants reads the caller's authority, reporting whether it could be
// determined at all.
//
// It is named for what it reads rather than for what any one caller asks of it,
// because two rulings now ask: this file's, about the archived rows on a paged
// read, and adminonly.go's, about a write to a setting the catalog reserved.
//
// A server built with no [WithGrantsExtractor] answers false, which is the
// fail-closed half of the default: a surface that cannot see what the caller may
// do cannot tell an administrator from anybody else, and the expensive way to be
// wrong about that is to guess "administrator". A consumer who wants the
// archived rows on the wire supplies the same authorization.GrantsExtractor they
// already hand primitives-go's authorization/grpc enforcer.
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

// readFilter reads the page a request asked for, and confines it to what the
// caller may be shown.
//
// It is a method with a description rather than three copies of the same eight
// lines, because the three paged reads share both failures: a filter no
// converter can read is the client's to fix and is InvalidArgument rather than
// the Internal every other call site here passes, and a request for archived
// rows is a request this surface answers in exactly one place.
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
