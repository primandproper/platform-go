package mediaregistry

import (
	"path"
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
	mediaregistrygrpc "github.com/primandproper/platform-go/v14/mediaregistry/grpc"
	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"

	"github.com/primandproper/primitives-go/v2/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The calls the resource surface's assertions make, as the names a caller is
// minted to make them by.
const (
	uploadObject         = mediaregistrypb.MediaRegistryService_UploadObject_FullMethodName
	recordObject         = mediaregistrypb.MediaRegistryService_RecordObject_FullMethodName
	getObject            = mediaregistrypb.MediaRegistryService_GetObject_FullMethodName
	listObjectsByIDs     = mediaregistrypb.MediaRegistryService_ListObjectsByIDs_FullMethodName
	listMyObjects        = mediaregistrypb.MediaRegistryService_ListMyObjects_FullMethodName
	listObjectsBySubject = mediaregistrypb.MediaRegistryService_ListObjectsBySubject_FullMethodName
	archiveObject        = mediaregistrypb.MediaRegistryService_ArchiveObject_FullMethodName
)

// uploadedType is what every object this suite uploads declares itself to be:
// the type every deployment that accepts images accepts.
const uploadedType = "image/png"

// uploadedBytes is what every object this suite uploads contains.
var uploadedBytes = []byte("\x89PNG conformance")

// resourceSurface is mediaregistry/grpc's promises: an upload is the sender's,
// is read back by them, and is absent to everybody else from every read.
func resourceSurface(t *testing.T, s *conformance.Session) {
	t.Helper()

	probe := s.Subject(t)
	if probe.Surfaces.MediaRegistry == nil {
		conformance.Skip(t, "conformance: this subject does not mount the media registry's resource surface")
	}

	t.Run("an upload is read back, listed, archived, and then absent", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(uploadObject, getObject, listMyObjects, archiveObject))
		needsUser(t, mine)

		object := upload(t, mine, nil)
		test.EqOp(t, mine.UserID, object.GetOwnerId(), test.Sprint("an upload is not owned by the caller who sent it"))
		test.EqOp(t, int64(len(uploadedBytes)), object.GetSize(), test.Sprint("the row does not record the bytes that were sent"))

		got, err := mine.Surfaces.MediaRegistry.GetObject(mine.Context(t.Context()),
			&mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
		must.NoError(t, err, must.Sprint("the sender could not read back their upload"))
		test.EqOp(t, object.GetKey(), got.GetResult().GetKey())

		listed, err := mine.Surfaces.MediaRegistry.ListMyObjects(mine.Context(t.Context()), &mediaregistrypb.ListMyObjectsRequest{})
		must.NoError(t, err)
		test.True(t, holds(listed.GetResults(), object.GetId()), test.Sprint("the sender's upload is not among their objects"))

		archived, err := mine.Surfaces.MediaRegistry.ArchiveObject(mine.Context(t.Context()),
			&mediaregistrypb.ArchiveObjectRequest{ObjectId: object.GetId()})
		must.NoError(t, err, must.Sprint("the sender could not archive their upload"))
		test.NotNil(t, archived.GetResult().GetArchivedAt(), test.Sprint("the archive did not answer with the row it archived"))

		_, err = mine.Surfaces.MediaRegistry.GetObject(mine.Context(t.Context()),
			&mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
		test.EqOp(t, codes.NotFound, status.Code(err), test.Sprint("an archived object is still read back"))

		listed, err = mine.Surfaces.MediaRegistry.ListMyObjects(mine.Context(t.Context()), &mediaregistrypb.ListMyObjectsRequest{})
		must.NoError(t, err)
		test.False(t, holds(listed.GetResults(), object.GetId()), test.Sprint("an archived object is still listed"))
	})

	reads := conformance.Making(getObject, listObjectsByIDs, listObjectsBySubject, archiveObject)

	t.Run("a colleague's upload is absent from every read, exactly as an unknown one is", func(t *testing.T) {
		t.Parallel()

		if s.Seams().MediaObjectsShared {
			conformance.Skip(t, "conformance: this deployment's entitlement lets somebody other than the owner read an object")
		}

		mine := s.Subject(t, conformance.Making(uploadObject, getObject))
		needsUser(t, mine)

		colleague := s.Subject(t, reads, conformance.InTenant(surface, mine.ScopeFor(surface)))
		must.StrNotEqFold(t, mine.UserID, colleague.UserID,
			must.Sprint("the subject minted a colleague as the same user; ownership cannot be observed"))

		absentEverywhere(t, mine, colleague)
	})

	t.Run("another tenant's upload is absent from every read, exactly as an unknown one is", func(t *testing.T) {
		t.Parallel()

		mine, theirs := s.TwoTenants(t, surface, conformance.Making(uploadObject, getObject))
		needsUser(t, mine)

		absentEverywhere(t, mine, s.Subject(t, reads, conformance.InTenant(surface, theirs.ScopeFor(surface))))
	})

	t.Run("a registration of somebody else's key is refused as an absence", func(t *testing.T) {
		t.Parallel()

		mine := s.Subject(t, conformance.Making(uploadObject, recordObject))
		needsUser(t, mine)

		colleague := s.Subject(t, conformance.Making(uploadObject), conformance.InTenant(surface, mine.ScopeFor(surface)))
		must.StrNotEqFold(t, mine.UserID, colleague.UserID,
			must.Sprint("the subject minted a colleague as the same user; a foreign key cannot be named"))

		theirs := upload(t, colleague, nil)
		own := upload(t, mine, nil)

		// The colleague's key holds bytes, so a policy that let this caller
		// claim it would reach the registry and be told the key is taken. Only
		// the key policy answers it as an absence — and that answer has to
		// read exactly as a key under the caller's own prefix that holds
		// nothing does, or the refusal says which keys hold bytes.
		foreign := recordErr(t, mine, theirs.GetKey())
		missing := recordErr(t, mine, path.Join(path.Dir(own.GetKey()), "conformance-"+identifiers.New()+".png"))

		test.EqOp(t, codes.NotFound, status.Code(foreign), test.Sprint("a caller registered another user's key"))
		sameAnswer(t, "a foreign key", missing, foreign)
	})
}

// absentEverywhere uploads an object as mine and asserts that other reaches it
// through no read — and that each refusal reads as the answer for an object
// nobody uploaded.
//
// The object is attached to its sender, so the listing by subject has
// something of theirs to leave out. The sender reads it back first, so a
// deployment that serves nobody cannot pass on the strength of refusing
// everybody.
func absentEverywhere(t *testing.T, mine, other *conformance.Subject) {
	t.Helper()

	object := upload(t, mine, &mediaregistrypb.Subject{Type: mediaregistrygrpc.UserSubjectType, Id: mine.UserID})

	_, err := mine.Surfaces.MediaRegistry.GetObject(mine.Context(t.Context()),
		&mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
	must.NoError(t, err, must.Sprint("the sender could not read their own upload; the refusals below prove nothing"))

	unknown := getErr(t, other, identifiers.New())

	refused := getErr(t, other, object.GetId())
	test.EqOp(t, codes.NotFound, status.Code(refused), test.Sprint("somebody else's upload was read"))
	sameAnswer(t, "a read", unknown, refused)

	byIDs, err := other.Surfaces.MediaRegistry.ListObjectsByIDs(other.Context(t.Context()),
		&mediaregistrypb.ListObjectsByIDsRequest{ObjectIds: []string{object.GetId()}})
	must.NoError(t, err)
	test.SliceEmpty(t, byIDs.GetResults(), test.Sprint("somebody else's upload was read by id"))

	bySubject, err := other.Surfaces.MediaRegistry.ListObjectsBySubject(other.Context(t.Context()),
		&mediaregistrypb.ListObjectsBySubjectRequest{Subject: object.GetBelongsTo()})
	must.NoError(t, err)
	test.False(t, holds(bySubject.GetResults(), object.GetId()),
		test.Sprint("somebody else's upload was listed by what it is attached to"))

	_, err = other.Surfaces.MediaRegistry.ArchiveObject(other.Context(t.Context()),
		&mediaregistrypb.ArchiveObjectRequest{ObjectId: object.GetId()})
	test.EqOp(t, codes.NotFound, status.Code(err), test.Sprint("somebody else's upload was archived"))
	sameAnswer(t, "an archive", unknown, err)

	_, err = mine.Surfaces.MediaRegistry.GetObject(mine.Context(t.Context()),
		&mediaregistrypb.GetObjectRequest{ObjectId: object.GetId()})
	test.NoError(t, err, test.Sprint("a refused archive archived the upload anyway"))
}

// upload sends uploadedBytes through the surface as sub, attached to subject,
// in two chunks so the stream is a stream.
func upload(t *testing.T, sub *conformance.Subject, subject *mediaregistrypb.Subject) *mediaregistrypb.Object {
	t.Helper()

	stream, err := sub.Surfaces.MediaRegistry.UploadObject(sub.Context(t.Context()))
	must.NoError(t, err)

	half := len(uploadedBytes) / 2

	for _, msg := range []*mediaregistrypb.UploadObjectRequest{
		{Part: &mediaregistrypb.UploadObjectRequest_Header{Header: &mediaregistrypb.UploadObjectHeader{
			Name: "conformance.png", ContentType: uploadedType, BelongsTo: subject,
		}}},
		{Part: &mediaregistrypb.UploadObjectRequest_Chunk{Chunk: uploadedBytes[:half]}},
		{Part: &mediaregistrypb.UploadObjectRequest_Chunk{Chunk: uploadedBytes[half:]}},
	} {
		if err = stream.Send(msg); err != nil {
			break
		}
	}

	res, err := stream.CloseAndRecv()
	must.NoError(t, err, must.Sprint("uploading an object"))
	must.NotNil(t, res.GetResult())
	must.StrNotEqFold(t, "", res.GetResult().GetId(), must.Sprint("an upload came back with no identifier"))

	return res.GetResult()
}

// getErr is what reading objectID as sub answers with, for the assertions that
// expect a refusal.
func getErr(t *testing.T, sub *conformance.Subject, objectID string) error {
	t.Helper()

	_, err := sub.Surfaces.MediaRegistry.GetObject(sub.Context(t.Context()), &mediaregistrypb.GetObjectRequest{ObjectId: objectID})
	must.Error(t, err)

	return err
}

// recordErr is what registering key as sub answers with, for the assertions
// that expect a refusal.
func recordErr(t *testing.T, sub *conformance.Subject, key string) error {
	t.Helper()

	_, err := sub.Surfaces.MediaRegistry.RecordObject(sub.Context(t.Context()),
		&mediaregistrypb.RecordObjectRequest{Key: key, ContentType: uploadedType})
	must.Error(t, err)

	return err
}

// sameAnswer asserts a refusal reads exactly as the answer for something that
// is not there: the code and the message a client is shown.
func sameAnswer(t *testing.T, name string, absent, refused error) {
	t.Helper()

	test.EqOp(t, status.Code(absent), status.Code(refused),
		test.Sprintf("%s: a refusal and an absence answered with different codes", name))
	test.EqOp(t, status.Convert(absent).Message(), status.Convert(refused).Message(),
		test.Sprintf("%s: a refusal and an absence answered with different messages", name))
}

// holds reports whether objects includes one with objectID.
func holds(objects []*mediaregistrypb.Object, objectID string) bool {
	for _, object := range objects {
		if object.GetId() == objectID {
			return true
		}
	}

	return false
}

// needsUser skips unless the subject surfaced the caller's user identifier,
// which is who owns an upload.
func needsUser(t *testing.T, sub *conformance.Subject) {
	t.Helper()

	if sub.UserID == "" {
		conformance.Skip(t, "conformance: this subject does not surface the caller's user identifier")
	}
}
