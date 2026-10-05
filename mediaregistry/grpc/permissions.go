package grpc

import (
	mediaregistryhttp "github.com/primandproper/platform-go/v15/mediaregistry/http"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"
	"github.com/primandproper/platform-go/v15/rbac"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// The permissions this service's methods require.
//
// Platform declares them and grants them to nobody. Whether a member holds one
// is the consumer's policy — a deployment whose users upload nothing grants
// none of them, and one that lets only staff register externally stored bytes
// grants that to staff.
//
// A grant says the caller may make this kind of call. Which objects they may
// make it against is the Entitlement's, per row, and the owner's for an
// archive; a grant widens neither.
const (
	// PermissionCreateObjects covers putting an object in the registry as the
	// caller's: uploading one, and registering bytes already in the bucket.
	//
	// One permission for the two, because they are one act by two routes — the
	// bytes arrive through this service or around it — and a grant that
	// separated them would let a consumer allow the one that bypasses the
	// size cap while forbidding the one that enforces it.
	PermissionCreateObjects authorization.Permission = "mediaregistry.objects.create"

	// PermissionReadObjects covers every read: one object, a set by id, the
	// caller's own, and those attached to a subject.
	//
	// It is mediaregistry/http's permission rather than a second one with the
	// same name, because reading an object's row and reading its bytes are one
	// question — may this caller have this object — asked of two surfaces.
	PermissionReadObjects = mediaregistryhttp.PermissionReadObjects

	// PermissionArchiveObjects covers hiding one of the caller's objects.
	PermissionArchiveObjects authorization.Permission = "mediaregistry.objects.archive"
)

// Permissions is the default map from method name to what it requires: every
// RPC this service declares, and nothing else. It returns a fresh map each
// call.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		mediaregistrypb.MediaRegistryService_UploadObject_FullMethodName: {PermissionCreateObjects},
		mediaregistrypb.MediaRegistryService_RecordObject_FullMethodName: {PermissionCreateObjects},

		mediaregistrypb.MediaRegistryService_GetObject_FullMethodName:            {PermissionReadObjects},
		mediaregistrypb.MediaRegistryService_ListObjectsByIDs_FullMethodName:     {PermissionReadObjects},
		mediaregistrypb.MediaRegistryService_ListMyObjects_FullMethodName:        {PermissionReadObjects},
		mediaregistrypb.MediaRegistryService_ListObjectsBySubject_FullMethodName: {PermissionReadObjects},

		mediaregistrypb.MediaRegistryService_ArchiveObject_FullMethodName: {PermissionArchiveObjects},
	}
}

// Require declares every method of this service on a requirements builder. A
// nil builder is tolerated and returns nil.
//
// UploadObject is a stream, so what checks it is the enforcer's
// StreamServerInterceptor rather than its unary one. A server that installs
// only the unary interceptor leaves the upload unchecked — and a principal
// that only a unary authentication interceptor puts on the context leaves
// every upload unauthenticated, which this service refuses.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	return b.RequireAll(Permissions())
}

// Tiers sorts this service's permissions by the kind of principal that should
// hold them. A deployment composes its policy from these with rbac.MergeTiers
// and rbac.PolicyFromTiers; the role names are its own.
//
// Every grant here is a member's. Which objects a holder may read is the
// Entitlement's, per row, and an archive is the owner's, so none of them
// reaches past what the caller may already have.
func Tiers() rbac.Tiers {
	return rbac.Tiers{
		Member: []authorization.Permission{
			PermissionCreateObjects,
			PermissionReadObjects,
			PermissionArchiveObjects,
		},
	}
}
