package archivegate

import (
	"context"

	"github.com/primandproper/primitives-go/v2/authorization"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"
	"github.com/primandproper/primitives-go/v2/observability"

	"google.golang.org/grpc/codes"
)

// NothingArchived is the archive grant of a paged read whose rows are never
// archived. No caller holds it, an allow-everything grant included, so the
// field is cleared for everybody.
const NothingArchived authorization.Permission = ""

// Filter converts a paged read's wire filter and narrows include_archived to
// a caller holding archiveGrant.
//
// grants is the surface's authorization.GrantsExtractor — the one a consumer
// already hands primitives-go's authorization/grpc enforcer, so the interceptor
// deciding whether the method may be called and this deciding which rows the
// answer may contain read the same authority. A nil extractor, or one that
// cannot say who is calling, clears the field: a surface that cannot see what
// the caller may do cannot tell somebody entitled to the archived rows from
// somebody who is not, and the expensive way to be wrong is to guess.
//
// A malformed filter is codes.InvalidArgument rather than the Internal a
// surface's other failures default to: it is the one thing on these requests a
// client can get wrong on its own, and description is what its log line says
// was being read. An absent filter is the default page.
func Filter(
	ctx context.Context,
	op observability.Operation,
	in *filteringpb.QueryFilter,
	grants authorization.GrantsExtractor,
	archiveGrant authorization.Permission,
	clearedKey string,
	description string,
) (*filtering.QueryFilter, error) {
	filter, err := filteringgrpc.FromProto(in)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.InvalidArgument, "%s", description)
	}

	return Narrow(ctx, op, filter, grants, archiveGrant, clearedKey), nil
}

// Narrow is [Filter] for a filter that did not arrive as a protobuf message —
// the one an MCP tool call decodes from its arguments — and is the half of
// Filter that decides: include_archived honored for a caller holding
// archiveGrant, cleared for everybody else, the clearing recorded under
// clearedKey. It narrows the filter it is handed and returns it.
func Narrow(
	ctx context.Context,
	op observability.Operation,
	filter *filtering.QueryFilter,
	grants authorization.GrantsExtractor,
	archiveGrant authorization.Permission,
	clearedKey string,
) *filtering.QueryFilter {
	if filter == nil || filter.IncludeArchived == nil || !*filter.IncludeArchived {
		return filter
	}

	if holds(ctx, grants, archiveGrant) {
		return filter
	}

	filter.IncludeArchived = nil

	op.Set(clearedKey, true)

	return filter
}

// holds reports whether the caller holds archiveGrant, answering no wherever
// the question cannot be answered.
func holds(ctx context.Context, grants authorization.GrantsExtractor, archiveGrant authorization.Permission) bool {
	if archiveGrant == NothingArchived || grants == nil {
		return false
	}

	held, ok := grants(ctx)

	return ok && held.Has(archiveGrant)
}
