package grpc_test

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/mediaregistry"
	mediaregistrygrpc "github.com/primandproper/platform-go/v15/mediaregistry/grpc"
	mediaregistryhttp "github.com/primandproper/platform-go/v15/mediaregistry/http"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecordObject(T *testing.T) {
	T.Parallel()

	T.Run("bytes under the caller's prefix are registered as theirs, at the size the bucket reports", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.bucket.put(aliceHome+"/signed/receipt.png", []byte("twelve bytes"))

		res, err := h.client.RecordObject(as(t, alice, testScope), &mediaregistrypb.RecordObjectRequest{
			Key:         aliceHome + "/signed/receipt.png",
			ContentType: pngType,
			BelongsTo:   &mediaregistrypb.Subject{Type: mediaregistrygrpc.UserSubjectType, Id: alice},
		})
		must.NoError(t, err)

		object := res.GetResult()
		test.EqOp(t, alice, object.GetOwnerId())
		test.EqOp(t, int64(12), object.GetSize())
		test.EqOp(t, alice, object.GetBelongsTo().GetId())
	})

	T.Run("a key under somebody else's prefix is refused exactly as an absent one is", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.bucket.put(bob+"/x/photo.png", pngBytes())

		foreign := recordErr(t, h, alice, bob+"/x/photo.png")
		missing := recordErr(t, h, alice, aliceHome+"/nothing/here.png")

		test.EqOp(t, codes.NotFound, status.Code(foreign))
		test.ErrorIs(t, foreign, mediaregistry.ErrObjectNotFound)
		test.EqOp(t, status.Code(missing), status.Code(foreign))
		test.EqOp(t, status.Convert(missing).Message(), status.Convert(foreign).Message())

		page, err := h.store.ListObjectsByOwner(t.Context(), h.db.Reader(), testScope, alice, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)
	})

	T.Run("a key that climbs out of the caller's prefix is not under it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.bucket.put(aliceHome+"/../"+bob+"/photo.png", pngBytes())

		test.EqOp(t, codes.NotFound, status.Code(recordErr(t, h, alice, aliceHome+"/../"+bob+"/photo.png")))
		test.EqOp(t, codes.NotFound, status.Code(recordErr(t, h, alice, aliceHome)))
	})

	T.Run("a person in two tenants cannot claim one tenant's bytes in the other", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.bucket.put(aliceHome+"/1/a.png", pngBytes())

		_, err := h.client.RecordObject(as(t, alice, otherScope),
			&mediaregistrypb.RecordObjectRequest{Key: aliceHome + "/1/a.png", ContentType: pngType})
		test.EqOp(t, codes.NotFound, status.Code(err))

		// The positive control: the same key, in the tenant it is under.
		_, err = h.client.RecordObject(as(t, alice, testScope),
			&mediaregistrypb.RecordObjectRequest{Key: aliceHome + "/1/a.png", ContentType: pngType})
		test.NoError(t, err)
	})

	T.Run("the default policy follows a moved layout", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithKeyFunc(func(caller mediaregistryhttp.Caller, objectID, name string) string {
			return "uploads/" + caller.PrincipalID + "/" + objectID + "/" + name
		}))
		h.bucket.put("uploads/"+alice+"/1/a.png", pngBytes())
		h.bucket.put(aliceHome+"/1/a.png", pngBytes())

		_, err := h.client.RecordObject(as(t, alice, testScope),
			&mediaregistrypb.RecordObjectRequest{Key: "uploads/" + alice + "/1/a.png", ContentType: pngType})
		test.NoError(t, err)

		test.EqOp(t, codes.NotFound, status.Code(recordErr(t, h, alice, aliceHome+"/1/a.png")))
	})

	T.Run("a record key policy replaces the default", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithRecordKeyPolicy(
			func(_ context.Context, _ mediaregistryhttp.Caller, key string) (bool, error) {
				return key == "shared/intake.png", nil
			}))
		h.bucket.put("shared/intake.png", pngBytes())
		h.bucket.put(aliceHome+"/1/a.png", pngBytes())

		_, err := h.client.RecordObject(as(t, alice, testScope),
			&mediaregistrypb.RecordObjectRequest{Key: "shared/intake.png", ContentType: pngType})
		test.NoError(t, err)

		test.EqOp(t, codes.NotFound, status.Code(recordErr(t, h, alice, aliceHome+"/1/a.png")))
	})

	T.Run("a registration attached to somebody else is refused as an absence", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.bucket.put(aliceHome+"/1/a.png", pngBytes())

		_, err := h.client.RecordObject(as(t, alice, testScope), &mediaregistrypb.RecordObjectRequest{
			Key: aliceHome + "/1/a.png", ContentType: pngType,
			BelongsTo: &mediaregistrypb.Subject{Type: mediaregistrygrpc.UserSubjectType, Id: bob},
		})
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("a registration with no key, or a refused type, is malformed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.bucket.put(aliceHome+"/1/a.html", pngBytes())

		_, err := h.client.RecordObject(as(t, alice, testScope), &mediaregistrypb.RecordObjectRequest{ContentType: pngType})
		test.ErrorIs(t, err, mediaregistrygrpc.ErrNoObjectKey)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		_, err = h.client.RecordObject(as(t, alice, testScope),
			&mediaregistrypb.RecordObjectRequest{Key: aliceHome + "/1/a.html", ContentType: "text/html"})
		test.ErrorIs(t, err, mediaregistrygrpc.ErrContentTypeRefused)
	})

	T.Run("a key already registered is a conflict", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.bucket.put(aliceHome+"/1/a.png", pngBytes())

		_, err := h.client.RecordObject(as(t, alice, testScope),
			&mediaregistrypb.RecordObjectRequest{Key: aliceHome + "/1/a.png", ContentType: pngType})
		must.NoError(t, err)

		_, err = h.client.RecordObject(as(t, alice, testScope),
			&mediaregistrypb.RecordObjectRequest{Key: aliceHome + "/1/a.png", ContentType: pngType})
		test.ErrorIs(t, err, mediaregistry.ErrObjectKeyTaken)
	})
}

// recordErr is what registering key as principal answers with, for the tests
// that expect a refusal.
func recordErr(t *testing.T, h *harness, principal, key string) error {
	t.Helper()

	_, err := h.client.RecordObject(as(t, principal, testScope),
		&mediaregistrypb.RecordObjectRequest{Key: key, ContentType: pngType})
	must.Error(t, err)

	return err
}
