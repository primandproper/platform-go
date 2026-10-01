package grpc_test

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrygrpc "github.com/primandproper/platform-go/v14/mediaregistry/grpc"
	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConverters(T *testing.T) {
	T.Parallel()

	T.Run("a row renders every field but its scope", func(t *testing.T) {
		t.Parallel()

		created := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
		archived := created.Add(time.Hour)

		out := mediaregistrygrpc.ObjectToProto(&mediaregistry.Object{
			CreatedAt:   created,
			ArchivedAt:  &archived,
			BelongsTo:   mediaregistry.Subject{Type: "user", ID: alice},
			ID:          "obj",
			Key:         "k",
			ContentType: pngType,
			OwnerID:     alice,
			Scope:       testScope,
			Size:        7,
		})
		must.NotNil(t, out)

		test.EqOp(t, created, out.GetCreatedAt().AsTime())
		test.EqOp(t, archived, out.GetArchivedAt().AsTime())
		test.Nil(t, out.GetLastUpdatedAt())
		test.EqOp(t, "user", out.GetBelongsTo().GetType())
		test.EqOp(t, alice, out.GetBelongsTo().GetId())
		test.EqOp(t, "obj", out.GetId())
		test.EqOp(t, "k", out.GetKey())
		test.EqOp(t, pngType, out.GetContentType())
		test.EqOp(t, alice, out.GetOwnerId())
		test.EqOp(t, int64(7), out.GetSize())
	})

	T.Run("nothing renders as nothing", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, mediaregistrygrpc.ObjectToProto(nil))
		test.Nil(t, mediaregistrygrpc.SubjectToProto(mediaregistry.Subject{}))
		test.SliceLen(t, 1, mediaregistrygrpc.ObjectsToProto([]*mediaregistry.Object{nil, {ID: "a"}}))
		test.EqOp(t, mediaregistry.Subject{}, mediaregistrygrpc.SubjectFromProto(nil))
	})

	T.Run("a subject round-trips", func(t *testing.T) {
		t.Parallel()

		in := mediaregistry.Subject{Type: "user", ID: alice}
		test.EqOp(t, in, mediaregistrygrpc.SubjectFromProto(mediaregistrygrpc.SubjectToProto(in)))
		test.EqOp(t, "x", mediaregistrygrpc.SubjectFromProto(&mediaregistrypb.Subject{Id: "x"}).ID)
	})
}
