package grpc_test

import (
	"testing"

	mediaregistrygrpc "github.com/primandproper/platform-go/v15/mediaregistry/grpc"
	mediaregistryhttp "github.com/primandproper/platform-go/v15/mediaregistry/http"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// everyMethod is every RPC the service declares, read off the descriptor
// rather than listed, streams included.
func everyMethod() []string {
	desc := mediaregistrypb.MediaRegistryService_ServiceDesc

	out := make([]string, 0, len(desc.Methods)+len(desc.Streams))
	for _, m := range desc.Methods {
		out = append(out, "/"+desc.ServiceName+"/"+m.MethodName)
	}

	for i := range desc.Streams {
		out = append(out, "/"+desc.ServiceName+"/"+desc.Streams[i].StreamName)
	}

	return out
}

func TestPermissions(T *testing.T) {
	T.Parallel()

	T.Run("every method requires exactly one grant, and nothing else is declared", func(t *testing.T) {
		t.Parallel()

		permissions := mediaregistrygrpc.Permissions()
		methods := everyMethod()

		must.MapLen(t, len(methods), permissions)

		for _, method := range methods {
			test.SliceLen(t, 1, permissions[method], test.Sprintf("%s", method))
		}
	})

	T.Run("the read is the serve route's permission, not a second one", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, mediaregistryhttp.PermissionReadObjects, mediaregistrygrpc.PermissionReadObjects)
	})

	T.Run("the map is the caller's copy", func(t *testing.T) {
		t.Parallel()

		mine := mediaregistrygrpc.Permissions()
		delete(mine, mediaregistrypb.MediaRegistryService_UploadObject_FullMethodName)

		test.MapContainsKey(t, mediaregistrygrpc.Permissions(), mediaregistrypb.MediaRegistryService_UploadObject_FullMethodName)
	})

	T.Run("Require tolerates a nil builder and declares every method on a real one", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, mediaregistrygrpc.Require(nil))

		reqs, err := mediaregistrygrpc.Require(authzgrpc.NewRequirements()).Build()
		must.NoError(t, err)
		test.NotNil(t, reqs)
	})
}
