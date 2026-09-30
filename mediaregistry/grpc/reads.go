package grpc

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The four reads, and the one rule they share: every row this surface hands
// back is one the deployment's Entitlement said the caller may read.
//
// The single read refuses what the entitlement declines as an absence, exactly
// as mediaregistry/http's serve route does. The three that answer with a set
// leave it out, which is the reading ListObjectsByIDs already takes of an id
// that names nothing: the caller was going to skip it either way.
//
// A paged read that leaves rows out has a page that can be short, or empty
// with a cursor to the next, and the cursor is still the store's and still
// correct. What it cannot keep is the store's counts where they would count
// somebody else's objects, so ListObjectsBySubject answers without them; see
// that method.
//
// No read here returns an archived object. The store's reads already filter
// them, and the one knob that would reach them — a filter's include_archived —
// is cleared before the filter reaches the store: an archived object's row is
// the only record of where its bytes still are, and that is a deployment's
// retention concern rather than something a client pages through.

// GetObject reads one of the caller's objects by id.
func (s *Server) GetObject(
	ctx context.Context,
	request *mediaregistrypb.GetObjectRequest,
) (resp *mediaregistrypb.GetObjectResponse, err error) {
	ctx, req, done, err := s.caller(ctx, mediaregistrypb.MediaRegistryService_GetObject_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	objectID := request.GetObjectId()
	req.op.Set(objectIDKey, objectID)

	if objectID == "" {
		return nil, s.absent(req, "no object named")
	}

	object, err := s.store.GetObject(ctx, s.client.Reader(), req.caller.Scope, objectID)
	switch {
	case errors.Is(err, mediaregistry.ErrObjectNotFound):
		return nil, s.absent(req, "no such object in the caller's scope")
	case err != nil:
		return nil, s.fail(req, err, codes.Internal, "reading object %q", objectID)
	}

	entitled, err := s.entitled(ctx, req, object)
	switch {
	case err != nil:
		return nil, s.fail(req, err, codes.Internal, "deciding the caller's entitlement to object %q", objectID)
	case !entitled:
		return nil, s.absent(req, "the entitlement declined the caller")
	}

	return &mediaregistrypb.GetObjectResponse{Result: ObjectToProto(object)}, nil
}

// ListObjectsByIDs reads a bounded set of the caller's objects in one call. An
// id that names nothing the caller may read is absent from the answer.
func (s *Server) ListObjectsByIDs(
	ctx context.Context,
	request *mediaregistrypb.ListObjectsByIDsRequest,
) (resp *mediaregistrypb.ListObjectsByIDsResponse, err error) {
	ctx, req, done, err := s.caller(ctx, mediaregistrypb.MediaRegistryService_ListObjectsByIDs_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	objects, err := s.store.ListObjectsByIDs(ctx, s.client.Reader(), req.caller.Scope, request.GetObjectIds())
	if err != nil {
		return nil, s.fail(req, err, codes.Internal, "reading a set of objects")
	}

	readable, err := s.readable(ctx, req, objects)
	if err != nil {
		return nil, err
	}

	return &mediaregistrypb.ListObjectsByIDsResponse{Results: ObjectsToProto(readable)}, nil
}

// ListMyObjects pages the caller's own objects.
//
// Whose they are is who is asking; the request has no owner to name. The
// counts are the store's and are kept: every row they count is the caller's
// own, so they tell the caller nothing about anybody else.
func (s *Server) ListMyObjects(
	ctx context.Context,
	request *mediaregistrypb.ListMyObjectsRequest,
) (resp *mediaregistrypb.ListMyObjectsResponse, err error) {
	ctx, req, done, err := s.caller(ctx, mediaregistrypb.MediaRegistryService_ListMyObjects_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	filter, err := s.readFilter(req, request.GetFilter())
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListObjectsByOwner(ctx, s.client.Reader(), req.caller.Scope, req.caller.PrincipalID, filter)
	if err != nil {
		return nil, s.fail(req, err, codes.Internal, "listing the caller's objects")
	}

	readable, err := s.readable(ctx, req, page.Data)
	if err != nil {
		return nil, err
	}

	return &mediaregistrypb.ListMyObjectsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    ObjectsToProto(readable),
	}, nil
}

// ListObjectsBySubject pages the objects attached to one thing, as far as the
// caller may read them.
//
// It answers without counts. The store counts every object attached to the
// subject, whoever owns it, and under OwnerOnly most of those are rows this
// caller is not shown — so a count would be exactly the oracle the refusals
// are worded to avoid, reporting how many objects other people hung off a
// thing. The pagination says its counts are not known, which is true of what
// this caller may see, and the cursor still walks the whole list.
func (s *Server) ListObjectsBySubject(
	ctx context.Context,
	request *mediaregistrypb.ListObjectsBySubjectRequest,
) (resp *mediaregistrypb.ListObjectsBySubjectResponse, err error) {
	ctx, req, done, err := s.caller(ctx, mediaregistrypb.MediaRegistryService_ListObjectsBySubject_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	subject := SubjectFromProto(request.GetSubject())
	if err = subject.Validate(); err != nil {
		return nil, s.fail(req, err, codes.InvalidArgument, "reading the subject to list by")
	}

	if !subject.Attached() {
		return nil, s.fail(req, mediaregistry.ErrUnattachedSubject, codes.InvalidArgument, "reading the subject to list by")
	}

	filter, err := s.readFilter(req, request.GetFilter())
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListObjectsBySubject(ctx, s.client.Reader(), req.caller.Scope, subject, filter)
	if err != nil {
		return nil, s.fail(req, err, codes.Internal, "listing the objects attached to %s", subject)
	}

	readable, err := s.readable(ctx, req, page.Data)
	if err != nil {
		return nil, err
	}

	pagination := page.Pagination
	pagination.CountsKnown = false
	pagination.FilteredCount = 0
	pagination.TotalCount = 0

	return &mediaregistrypb.ListObjectsBySubjectResponse{
		Pagination: filteringgrpc.PaginationToProto(pagination),
		Results:    ObjectsToProto(readable),
	}, nil
}

// readable is the rows the caller may read, in the order the store gave them.
// An entitlement that could not decide fails the whole read: answering "no" to
// a question nobody managed to ask is how a caller loses sight of their own
// objects during an outage.
func (s *Server) readable(
	ctx context.Context,
	req *request,
	objects []*mediaregistry.Object,
) ([]*mediaregistry.Object, error) {
	out := make([]*mediaregistry.Object, 0, len(objects))

	for _, object := range objects {
		entitled, err := s.entitled(ctx, req, object)
		if err != nil {
			return nil, s.fail(req, err, codes.Internal, "deciding the caller's entitlement to a listed object")
		}

		if entitled {
			out = append(out, object)
		}
	}

	return out, nil
}

// readFilter reads the page a request asked for, with include_archived
// cleared. A filter no converter can read is the client's to fix.
func (s *Server) readFilter(req *request, in *filteringpb.QueryFilter) (*filtering.QueryFilter, error) {
	filter, err := filteringgrpc.FromProto(in)
	if err != nil {
		return nil, s.fail(req, err, codes.InvalidArgument, "reading the filter of an object page")
	}

	if filter != nil {
		filter.IncludeArchived = nil
	}

	return filter, nil
}
