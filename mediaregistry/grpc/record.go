package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/mediaregistry"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/uploads"

	"google.golang.org/grpc/codes"
)

// RecordObject registers bytes that are already in the bucket as the caller's:
// an upload that went through a signed URL, or one some other process wrote.
//
// Registering is claiming, so the key is asked about before anything else is.
// The deployment's RecordKeyPolicy decides whether this caller may claim it —
// by default, only a key beneath the part of the bucket the layout gives them —
// and a key they may not claim is answered as an absence, the same as a key
// with nothing at it. A caller probing which keys hold somebody's bytes learns
// nothing from either.
//
// The size is the bucket's. A manager that can report an object's attributes
// is asked, and one that cannot records the size as unknown, zero, rather than
// as whatever a client claimed: a quota read off claimed sizes is one that does
// not hold. The request has no size field to claim one with.
func (s *Server) RecordObject(
	ctx context.Context,
	request *mediaregistrypb.RecordObjectRequest,
) (resp *mediaregistrypb.RecordObjectResponse, err error) {
	ctx, req, done, err := s.caller(ctx, mediaregistrypb.MediaRegistryService_RecordObject_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	key, contentType := request.GetKey(), request.GetContentType()
	req.op.Set(objectKeyKey, key)

	switch {
	case key == "":
		return nil, s.fail(req, ErrNoObjectKey, codes.InvalidArgument, "reading a registration's key")
	case !s.contentTypes(contentType):
		return nil, s.fail(req, ErrContentTypeRefused, codes.InvalidArgument, "checking a registration's content type")
	}

	subject := SubjectFromProto(request.GetBelongsTo())
	if err = subject.Validate(); err != nil {
		return nil, s.fail(req, err, codes.InvalidArgument, "reading a registration's subject")
	}

	if !mayAttach(req.caller, subject) {
		return nil, s.absent(req, "a registration attached to a subject other than the caller")
	}

	claimable, err := s.recordKeys(ctx, req.caller, key)
	switch {
	case err != nil:
		return nil, s.fail(req, err, codes.Internal, "deciding whether the caller may register a key")
	case !claimable:
		return nil, s.absent(req, "a registration of a key outside the caller's part of the bucket")
	}

	size, present, err := s.storedSize(ctx, key)
	switch {
	case err != nil:
		return nil, s.fail(req, err, codes.Internal, "reading what the bucket holds at a registration's key")
	case !present:
		return nil, s.absent(req, "a registration of a key the bucket holds nothing at")
	}

	var registered *mediaregistry.Object

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		object, recordErr := s.store.RecordObject(ctx, tx, req.caller.Scope, mediaregistry.ObjectInput{
			Key:         key,
			ContentType: contentType,
			OwnerID:     req.caller.PrincipalID,
			BelongsTo:   subject,
			Size:        size,
		})
		registered = object

		return recordErr
	}); err != nil {
		return nil, s.fail(req, err, codes.Internal, "registering an object")
	}

	req.op.Set(objectIDKey, registered.ID)

	return &mediaregistrypb.RecordObjectResponse{Result: ObjectToProto(registered)}, nil
}

// storedSize reports whether the bucket holds bytes at key and, where the
// manager can say, how many.
func (s *Server) storedSize(ctx context.Context, key string) (size int64, present bool, err error) {
	present, err = s.manager.Exists(ctx, key)
	if err != nil || !present {
		return 0, present, err
	}

	attributer, ok := s.manager.(uploads.Attributer)
	if !ok {
		return 0, true, nil
	}

	attributes, err := attributer.Attributes(ctx, key)
	if err != nil {
		return 0, true, err
	}

	return attributes.Size, true, nil
}
