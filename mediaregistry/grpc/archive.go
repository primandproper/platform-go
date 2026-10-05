package grpc

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v15/mediaregistry"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	"github.com/primandproper/primitives-go/v2/database"

	"google.golang.org/grpc/codes"
)

// errNotTheOwner is the archive's refusal as the transaction sees it. It never
// reaches a client: ArchiveObject answers it as an absence.
var errNotTheOwner = errors.New("the caller does not own this object")

// ArchiveObject hides one of the caller's objects, and answers with the row it
// hid.
//
// It is the owner's alone, whatever the Entitlement says; see WithEntitlement.
// An object somebody else owns is answered as an absence, as it would be from
// every read here under the default rule.
//
// Archiving is metadata-only, by mediaregistry's ruling: the row is hidden and
// the bytes stay in the bucket, because whether they are still needed is the
// deployment's retention policy. The row this answers with carries the key they
// are at, and it is the last answer that will — no read here returns an
// archived object.
//
// The read and the archive share one transaction, so the ownership checked is
// the ownership of the row that is archived.
func (s *Server) ArchiveObject(
	ctx context.Context,
	request *mediaregistrypb.ArchiveObjectRequest,
) (resp *mediaregistrypb.ArchiveObjectResponse, err error) {
	ctx, req, done, err := s.caller(ctx, mediaregistrypb.MediaRegistryService_ArchiveObject_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	objectID := request.GetObjectId()
	req.op.Set(objectIDKey, objectID)

	if objectID == "" {
		return nil, s.absent(req, "no object named")
	}

	var archived *mediaregistry.Object

	err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		object, getErr := s.store.GetObject(ctx, tx, req.caller.Scope, objectID)
		if getErr != nil {
			return getErr
		}

		if object == nil || object.OwnerID != req.caller.PrincipalID {
			return errNotTheOwner
		}

		archived, getErr = s.store.ArchiveObject(ctx, tx, req.caller.Scope, objectID)

		return getErr
	})

	switch {
	case errors.Is(err, mediaregistry.ErrObjectNotFound):
		return nil, s.absent(req, "no such object in the caller's scope")
	case errors.Is(err, errNotTheOwner):
		return nil, s.absent(req, "an archive of an object the caller does not own")
	case err != nil:
		return nil, s.fail(req, err, codes.Internal, "archiving object %q", objectID)
	}

	return &mediaregistrypb.ArchiveObjectResponse{Result: ObjectToProto(archived)}, nil
}
