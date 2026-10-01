package grpc_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrygrpc "github.com/primandproper/platform-go/v14/mediaregistry/grpc"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUploadObject(T *testing.T) {
	T.Parallel()

	T.Run("the bytes are stored at the default layout and the row records what was counted", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		content := bytes.Repeat([]byte("abcdefgh"), 1000)

		object, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "photo.png", ContentType: pngType}, content, 333)
		must.NoError(t, err)

		test.EqOp(t, alice, object.GetOwnerId())
		test.EqOp(t, pngType, object.GetContentType())
		test.EqOp(t, int64(len(content)), object.GetSize())
		test.EqOp(t, aliceHome+"/"+object.GetId()+"/photo.png", object.GetKey())
		test.NotNil(t, object.GetCreatedAt())
		test.Nil(t, object.GetBelongsTo())

		stored, err := h.bucket.Open(t.Context(), object.GetKey())
		must.NoError(t, err)

		read := new(bytes.Buffer)
		_, err = read.ReadFrom(stored)
		must.NoError(t, err)
		test.Eq(t, content, read.Bytes())

		row, err := h.store.GetObject(t.Context(), h.db.Reader(), testScope, object.GetId())
		must.NoError(t, err)
		test.EqOp(t, int64(len(content)), row.Size)
	})

	T.Run("upload, read, list and archive, then the object is absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		ctx := as(t, alice, testScope)
		object := h.uploadPNG(t, ctx)

		got, err := h.client.GetObject(ctx, &mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
		must.NoError(t, err)
		test.EqOp(t, object.GetKey(), got.GetResult().GetKey())

		mine, err := h.client.ListMyObjects(ctx, &mediaregistrypb.ListMyObjectsRequest{})
		must.NoError(t, err)
		must.SliceLen(t, 1, mine.GetResults())
		test.EqOp(t, object.GetId(), mine.GetResults()[0].GetId())

		archived, err := h.client.ArchiveObject(ctx, &mediaregistrypb.ArchiveObjectRequest{ObjectId: object.GetId()})
		must.NoError(t, err)
		test.NotNil(t, archived.GetResult().GetArchivedAt())
		test.EqOp(t, object.GetKey(), archived.GetResult().GetKey())

		_, err = h.client.GetObject(ctx, &mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
		test.EqOp(t, codes.NotFound, status.Code(err))

		// Archival is metadata-only: the bytes are still there for the
		// deployment's retention policy to decide about.
		test.True(t, h.bucket.holds(object.GetKey()))
	})

	T.Run("an upload over the cap is refused mid-stream, and nothing is stored", func(t *testing.T) {
		t.Parallel()

		const limit = 1024

		h := newHarness(t, mediaregistrygrpc.WithMaxBytes(limit))
		content := bytes.Repeat([]byte("x"), 64*limit)

		_, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "big.png", ContentType: pngType}, content, 100)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.ErrorIs(t, err, mediaregistrygrpc.ErrObjectTooLarge)

		// The manager was never handed a byte past the cap: the chunk that
		// would have crossed it was refused before any of it was copied.
		test.LessEq(t, int64(limit), h.bucket.saved.Load())

		page, err := h.store.ListObjectsByOwner(t.Context(), h.db.Reader(), testScope, alice, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)
	})

	T.Run("the cap is on by default", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		content := make([]byte, mediaregistrygrpc.DefaultMaxBytes+1)

		_, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "big.png", ContentType: pngType}, content, 1<<20)

		test.ErrorIs(t, err, mediaregistrygrpc.ErrObjectTooLarge)
		test.LessEq(t, mediaregistrygrpc.DefaultMaxBytes, h.bucket.saved.Load())
	})

	T.Run("an upload exactly at the cap is accepted", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithMaxBytes(10))

		object, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "ten.png", ContentType: pngType}, []byte("0123456789"), 4)
		must.NoError(t, err)
		test.EqOp(t, int64(10), object.GetSize())
	})

	T.Run("WithoutMaxBytes switches the cap off by name", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithMaxBytes(4), mediaregistrygrpc.WithoutMaxBytes())

		object, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "ten.png", ContentType: pngType}, []byte("0123456789"), 4)
		must.NoError(t, err)
		test.EqOp(t, int64(10), object.GetSize())
	})

	T.Run("a WithMaxBytes that is not positive leaves the cap in place", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithMaxBytes(4), mediaregistrygrpc.WithMaxBytes(0))

		_, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "ten.png", ContentType: pngType}, []byte("0123456789"), 4)
		test.ErrorIs(t, err, mediaregistrygrpc.ErrObjectTooLarge)
	})

	T.Run("active content and an unstated type are refused by default, before any bytes are read", func(t *testing.T) {
		t.Parallel()

		for _, contentType := range []string{"text/html", "image/svg+xml", "application/xhtml+xml; charset=utf-8", ""} {
			h := newHarness(t)

			_, err := h.upload(t, as(t, alice, testScope),
				&mediaregistrypb.UploadObjectHeader{Name: "page", ContentType: contentType}, []byte("<script>"), 2)

			test.EqOp(t, codes.InvalidArgument, status.Code(err), test.Sprintf("content type %q", contentType))
			test.ErrorIs(t, err, mediaregistrygrpc.ErrContentTypeRefused)
			test.EqOp(t, int64(0), h.bucket.saved.Load())
		}
	})

	T.Run("a content type policy replaces the default", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithContentTypePolicy(mediaregistrygrpc.AllowContentTypes("image/svg+xml")))

		_, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "a.png", ContentType: pngType}, pngBytes(), 4)
		test.ErrorIs(t, err, mediaregistrygrpc.ErrContentTypeRefused)

		_, err = h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "a.svg", ContentType: "image/svg+xml"}, []byte("<svg/>"), 4)
		test.NoError(t, err)
	})

	T.Run("a name that is not one segment is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		for _, name := range []string{"", ".", "..", "../bob/photo.png", `a\b.png`, "nested/photo.png"} {
			_, err := h.upload(t, as(t, alice, testScope),
				&mediaregistrypb.UploadObjectHeader{Name: name, ContentType: pngType}, pngBytes(), 4)

			test.EqOp(t, codes.InvalidArgument, status.Code(err), test.Sprintf("name %q", name))
			test.ErrorIs(t, err, mediaregistrygrpc.ErrInvalidObjectName)
		}
	})

	T.Run("a stream that opens with bytes, or sends nothing, is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.upload(t, as(t, alice, testScope), nil, pngBytes(), 4)
		test.ErrorIs(t, err, mediaregistrygrpc.ErrNoUploadHeader)

		_, err = h.upload(t, as(t, alice, testScope), nil, nil, 4)
		test.ErrorIs(t, err, mediaregistrygrpc.ErrNoUploadHeader)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("a second header is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		header := &mediaregistrypb.UploadObjectHeader{Name: "a.png", ContentType: pngType}

		stream, err := h.client.UploadObject(as(t, alice, testScope))
		must.NoError(t, err)

		for _, msg := range []*mediaregistrypb.UploadObjectRequest{
			{Part: &mediaregistrypb.UploadObjectRequest_Header{Header: header}},
			{Part: &mediaregistrypb.UploadObjectRequest_Chunk{Chunk: []byte("ab")}},
			{Part: &mediaregistrypb.UploadObjectRequest_Header{Header: header}},
		} {
			if err = stream.Send(msg); err != nil {
				break
			}
		}

		_, err = stream.CloseAndRecv()
		test.ErrorIs(t, err, mediaregistrygrpc.ErrRepeatedUploadHeader)
		test.EqOp(t, codes.InvalidArgument, status.Code(err))
	})

	T.Run("an upload may attach to its sender", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		self := &mediaregistrypb.Subject{Type: mediaregistrygrpc.UserSubjectType, Id: alice}

		object, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "avatar.png", ContentType: pngType, BelongsTo: self}, pngBytes(), 4)
		must.NoError(t, err)
		test.EqOp(t, alice, object.GetBelongsTo().GetId())

		attached, err := h.client.ListObjectsBySubject(as(t, alice, testScope),
			&mediaregistrypb.ListObjectsBySubjectRequest{Subject: self})
		must.NoError(t, err)
		must.SliceLen(t, 1, attached.GetResults())
		test.EqOp(t, object.GetId(), attached.GetResults()[0].GetId())
	})

	T.Run("an upload attached to anybody or anything else is refused as an absence", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		for _, subject := range []*mediaregistrypb.Subject{
			{Type: mediaregistrygrpc.UserSubjectType, Id: bob},
			{Type: "article", Id: alice},
		} {
			_, err := h.upload(t, as(t, alice, testScope),
				&mediaregistrypb.UploadObjectHeader{Name: "a.png", ContentType: pngType, BelongsTo: subject}, pngBytes(), 4)

			test.EqOp(t, codes.NotFound, status.Code(err), test.Sprintf("subject %v", subject))
			test.ErrorIs(t, err, mediaregistry.ErrObjectNotFound)
		}

		test.EqOp(t, int64(0), h.bucket.saved.Load())
	})

	T.Run("half a subject is malformed rather than refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{
				Name: "a.png", ContentType: pngType, BelongsTo: &mediaregistrypb.Subject{Type: mediaregistrygrpc.UserSubjectType},
			}, pngBytes(), 4)

		test.EqOp(t, codes.InvalidArgument, status.Code(err))
		test.ErrorIs(t, err, mediaregistry.ErrPartialSubject)
	})

	T.Run("a key function moves where the bytes go", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithKeyFunc(func(caller mediaregistryhttp.Caller, objectID, name string) string {
			return "uploads/" + caller.PrincipalID + "/" + objectID + "-" + name
		}))

		object := h.uploadPNG(t, as(t, alice, testScope))
		test.EqOp(t, "uploads/"+alice+"/"+object.GetId()+"-photo.png", object.GetKey())
		test.True(t, h.bucket.holds(object.GetKey()))
	})

	T.Run("an occupied key is refused before the bytes overwrite it", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithKeyFunc(func(mediaregistryhttp.Caller, string, string) string {
			return "fixed/photo.png"
		}))
		h.bucket.put("fixed/photo.png", []byte("somebody's"))

		_, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "photo.png", ContentType: pngType}, pngBytes(), 4)

		test.ErrorIs(t, err, mediaregistry.ErrObjectKeyOccupied)
		test.EqOp(t, int64(0), h.bucket.saved.Load())
	})

	T.Run("a registration that fails removes the bytes it would have described", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithKeyFunc(func(mediaregistryhttp.Caller, string, string) string {
			return "fixed/photo.png"
		}))

		// The key is registered and holds nothing, so the bucket clears the
		// upload and the registry refuses it.
		h.seed(t, testScope, mediaregistry.ObjectInput{Key: "fixed/photo.png", OwnerID: bob})

		_, err := h.upload(t, as(t, alice, testScope),
			&mediaregistrypb.UploadObjectHeader{Name: "photo.png", ContentType: pngType}, pngBytes(), 4)

		test.ErrorIs(t, err, mediaregistry.ErrObjectKeyTaken)
		test.False(t, h.bucket.holds("fixed/photo.png"))
	})

	T.Run("the after-upload hook sees the registered row, and cannot fail the upload", func(t *testing.T) {
		t.Parallel()

		var seen []*mediaregistry.Object

		h := newHarness(t, mediaregistrygrpc.WithAfterUpload(
			func(_ context.Context, caller mediaregistryhttp.Caller, object *mediaregistry.Object) error {
				seen = append(seen, object)
				test.EqOp(t, alice, caller.PrincipalID)

				return errors.New("the meter is down")
			}))

		object := h.uploadPNG(t, as(t, alice, testScope))

		must.SliceLen(t, 1, seen)
		test.EqOp(t, object.GetId(), seen[0].ID)
		test.EqOp(t, int64(len(pngBytes())), seen[0].Size)
	})

	T.Run("an upload with nobody on it is unauthenticated", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.upload(t, t.Context(),
			&mediaregistrypb.UploadObjectHeader{Name: "a.png", ContentType: pngType}, pngBytes(), 4)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
		test.EqOp(t, int64(0), h.bucket.saved.Load())
	})

	T.Run("a caller resolved with no principal identifier is unauthenticated", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithCallerResolver(func(context.Context) (mediaregistryhttp.Caller, error) {
			return mediaregistryhttp.Caller{Scope: testScope}, nil
		}))

		_, err := h.upload(t, t.Context(),
			&mediaregistrypb.UploadObjectHeader{Name: "a.png", ContentType: pngType}, pngBytes(), 4)
		test.EqOp(t, codes.Unauthenticated, status.Code(err))
		test.ErrorIs(t, err, mediaregistrygrpc.ErrNoPrincipal)
	})

	T.Run("a resolver that cannot answer is an internal failure", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, mediaregistrygrpc.WithCallerResolver(func(context.Context) (mediaregistryhttp.Caller, error) {
			return mediaregistryhttp.Caller{}, errors.New("the session store is down")
		}))

		_, err := h.client.GetObject(t.Context(), &mediaregistrypb.GetObjectRequest{ObjectId: "x"})
		test.EqOp(t, codes.Internal, status.Code(err))
		test.False(t, strings.Contains(status.Convert(err).Message(), "session store"))
	})
}
