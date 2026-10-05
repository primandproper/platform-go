package grpc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v15/mediaregistry"
	mediaregistrygrpc "github.com/primandproper/platform-go/v15/mediaregistry/grpc"
	mediaregistryhttp "github.com/primandproper/platform-go/v15/mediaregistry/http"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// shareEverything is an Entitlement a deployment whose objects hang off
// something shared would write: anybody in the tenant may read anything.
func shareEverything(context.Context, mediaregistryhttp.Caller, *mediaregistry.Object) (bool, error) {
	return true, nil
}

func TestGetObject(T *testing.T) {
	T.Parallel()

	T.Run("another owner's object, another tenant's, and none at all are one answer", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		object := h.uploadPNG(t, as(t, alice, testScope))

		_, err := h.client.GetObject(as(t, alice, testScope), &mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
		must.NoError(t, err, must.Sprint("the owner could not read their object; the refusals below prove nothing"))

		unknown := getErr(t, h, alice, testScope, "no-such-object")
		for name, err := range map[string]error{
			"a colleague":    getErr(t, h, bob, testScope, object.GetId()),
			"another tenant": getErr(t, h, alice, otherScope, object.GetId()),
		} {
			test.EqOp(t, codes.NotFound, status.Code(err), test.Sprint(name))
			test.EqOp(t, status.Convert(unknown).Message(), status.Convert(err).Message(), test.Sprint(name))
			test.ErrorIs(t, err, mediaregistry.ErrObjectNotFound, test.Sprint(name))
		}
	})

	T.Run("an entitlement can widen the read", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithEntitlement(shareEverything))
		object := h.uploadPNG(t, as(t, alice, testScope))

		got, err := h.client.GetObject(as(t, bob, testScope), &mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
		must.NoError(t, err)
		test.EqOp(t, alice, got.GetResult().GetOwnerId())

		// The tenant is not the entitlement's to widen: the read is bound to
		// the caller's scope before the rule is asked.
		test.EqOp(t, codes.NotFound, status.Code(getErr(t, h, bob, otherScope, object.GetId())))
	})

	T.Run("an entitlement that cannot decide is an internal failure", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithEntitlement(
			func(context.Context, mediaregistryhttp.Caller, *mediaregistry.Object) (bool, error) {
				return false, errors.New("the permissions service is down")
			}))
		object := h.uploadPNG(t, as(t, alice, testScope))

		test.EqOp(t, codes.Internal, status.Code(getErr(t, h, alice, testScope, object.GetId())))
	})

	T.Run("an empty id names nothing", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		test.EqOp(t, codes.NotFound, status.Code(getErr(t, h, alice, testScope, "")))
	})
}

func TestListObjectsByIDs(T *testing.T) {
	T.Parallel()

	T.Run("ids the caller may not read are absent rather than refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		mine := h.uploadPNG(t, as(t, alice, testScope))
		theirs := h.uploadPNG(t, as(t, bob, testScope))

		res, err := h.client.ListObjectsByIDs(as(t, alice, testScope), &mediaregistrypb.ListObjectsByIDsRequest{
			ObjectIds: []string{mine.GetId(), theirs.GetId(), "no-such-object"},
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, mine.GetId(), res.GetResults()[0].GetId())
	})

	T.Run("more ids than one read carries is malformed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ids := make([]string, mediaregistry.MaxObjectIDsPerRead+1)

		for i := range ids {
			ids[i] = string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676%26))
		}

		_, err := h.client.ListObjectsByIDs(as(t, alice, testScope), &mediaregistrypb.ListObjectsByIDsRequest{ObjectIds: ids})
		test.ErrorIs(t, err, mediaregistry.ErrTooManyObjectIDs)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestListMyObjects(T *testing.T) {
	T.Parallel()

	T.Run("only the caller's own objects, and never an archived one", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		kept := h.uploadPNG(t, as(t, alice, testScope))
		gone := h.uploadPNG(t, as(t, alice, testScope))
		h.uploadPNG(t, as(t, bob, testScope))
		h.uploadPNG(t, as(t, alice, otherScope))

		_, err := h.client.ArchiveObject(as(t, alice, testScope), &mediaregistrypb.ArchiveObjectRequest{ObjectId: gone.GetId()})
		must.NoError(t, err)

		includeArchived := true

		res, err := h.client.ListMyObjects(as(t, alice, testScope), &mediaregistrypb.ListMyObjectsRequest{
			Filter: &filteringpb.QueryFilter{IncludeArchived: &includeArchived},
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, kept.GetId(), res.GetResults()[0].GetId())
	})

	T.Run("a filter no converter can read is malformed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		sideways := "sideways"

		_, err := h.client.ListMyObjects(as(t, alice, testScope), &mediaregistrypb.ListMyObjectsRequest{
			Filter: &filteringpb.QueryFilter{SortBy: &sideways},
		})
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestListObjectsBySubject(T *testing.T) {
	T.Parallel()

	T.Run("only what the caller may read, and no counts that would count anybody else's", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		subject := mediaregistry.Subject{Type: "article", ID: "article_1"}

		// Attached through the store, as the consumer's own RPC would: this
		// surface attaches nothing to an article.
		mine := h.seed(t, testScope, mediaregistry.ObjectInput{Key: "a/1", OwnerID: alice, BelongsTo: subject})
		h.seed(t, testScope, mediaregistry.ObjectInput{Key: "b/1", OwnerID: bob, BelongsTo: subject})
		h.seed(t, testScope, mediaregistry.ObjectInput{Key: "b/2", OwnerID: bob, BelongsTo: subject})

		res, err := h.client.ListObjectsBySubject(as(t, alice, testScope), &mediaregistrypb.ListObjectsBySubjectRequest{
			Subject: mediaregistrygrpc.SubjectToProto(subject),
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, mine.ID, res.GetResults()[0].GetId())

		test.False(t, res.GetPagination().GetCountsKnown())
		test.EqOp(t, uint64(0), res.GetPagination().GetTotalCount())
		test.EqOp(t, uint64(0), res.GetPagination().GetFilteredCount())
	})

	T.Run("a shared entitlement sees every object attached", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithEntitlement(shareEverything))
		subject := mediaregistry.Subject{Type: "article", ID: "article_1"}

		h.seed(t, testScope, mediaregistry.ObjectInput{Key: "a/1", OwnerID: alice, BelongsTo: subject})
		h.seed(t, testScope, mediaregistry.ObjectInput{Key: "b/1", OwnerID: bob, BelongsTo: subject})

		res, err := h.client.ListObjectsBySubject(as(t, alice, testScope), &mediaregistrypb.ListObjectsBySubjectRequest{
			Subject: mediaregistrygrpc.SubjectToProto(subject),
		})
		must.NoError(t, err)
		test.SliceLen(t, 2, res.GetResults())
	})

	T.Run("a subject that names nothing, or half of something, is malformed", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.client.ListObjectsBySubject(as(t, alice, testScope), &mediaregistrypb.ListObjectsBySubjectRequest{})
		test.ErrorIs(t, err, mediaregistry.ErrUnattachedSubject)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))

		_, err = h.client.ListObjectsBySubject(as(t, alice, testScope), &mediaregistrypb.ListObjectsBySubjectRequest{
			Subject: &mediaregistrypb.Subject{Id: "article_1"},
		})
		test.ErrorIs(t, err, mediaregistry.ErrPartialSubject)
	})
}

func TestArchiveObject(T *testing.T) {
	T.Parallel()

	T.Run("somebody else's object is refused as an absence, even under a shared entitlement", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithEntitlement(shareEverything))
		object := h.uploadPNG(t, as(t, alice, testScope))

		_, err := h.client.ArchiveObject(as(t, bob, testScope), &mediaregistrypb.ArchiveObjectRequest{ObjectId: object.GetId()})
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.EqOp(t, status.Convert(getErr(t, h, alice, testScope, "no-such-object")).Message(), status.Convert(err).Message())

		_, err = h.client.GetObject(as(t, alice, testScope), &mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
		test.NoError(t, err, test.Sprint("a refused archive archived the object anyway"))
	})

	T.Run("an archived object cannot be archived again", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		object := h.uploadPNG(t, as(t, alice, testScope))

		_, err := h.client.ArchiveObject(as(t, alice, testScope), &mediaregistrypb.ArchiveObjectRequest{ObjectId: object.GetId()})
		must.NoError(t, err)

		_, err = h.client.ArchiveObject(as(t, alice, testScope), &mediaregistrypb.ArchiveObjectRequest{ObjectId: object.GetId()})
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = h.client.ArchiveObject(as(t, alice, testScope), &mediaregistrypb.ArchiveObjectRequest{})
		test.EqOp(t, codes.NotFound, status.Code(err))
	})
}

// getErr is what reading objectID as principal in scope answers with.
func getErr(t *testing.T, h *harness, principal string, scope tenancy.Scope, objectID string) error {
	t.Helper()

	_, err := h.client.GetObject(as(t, principal, scope), &mediaregistrypb.GetObjectRequest{ObjectId: objectID})
	must.Error(t, err)

	return err
}
