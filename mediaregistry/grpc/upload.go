package grpc

import (
	"errors"
	"io"

	"github.com/primandproper/platform-go/v15/mediaregistry"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/uploads"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UploadObject streams an object into the bucket as the caller's, and
// registers it.
//
// # The order
//
// The header is read and every rule that can refuse the upload is asked before
// a byte of it is: the name, the content type, the subject, and whether the
// key the layout builds is already occupied. A refusal costs the client one
// message rather than the object.
//
// Then the bytes go to the UploadManager as they arrive, through a reader over
// the stream, and nothing buffers the object: what is held at any moment is the
// one chunk the stream last delivered. The cap is enforced as they go past, so
// an upload that crosses it is refused there, with the rest of the stream
// unread — and the manager, handed a reader that failed, writes nothing.
//
// The row is written afterwards, in a transaction of its own that holds only
// the registration. That is the arrangement mediaregistry.StoreAndRecord
// recommends for exactly this caller: one who cannot afford a transaction held
// open for as long as a large object takes to arrive.
//
// # When the registration fails
//
// The bytes are at a key this call minted and nobody else can have written, so
// they are removed rather than left as an orphan for a sweep to find. The
// removal is best-effort, and a failure of it is recorded on the span beside
// the registration's failure, which is the one the client is told about.
func (s *Server) UploadObject(
	stream grpc.ClientStreamingServer[mediaregistrypb.UploadObjectRequest, mediaregistrypb.UploadObjectResponse],
) (err error) {
	ctx, req, done, err := s.caller(stream.Context(), mediaregistrypb.MediaRegistryService_UploadObject_FullMethodName)
	if err != nil {
		return err
	}

	defer func() { done(err) }()

	first, err := stream.Recv()
	if err != nil && !errors.Is(err, io.EOF) {
		return s.fail(req, err, StreamBrokenCode(err), "reading an upload's header")
	}

	header := first.GetHeader()
	if header == nil {
		return s.fail(req, ErrNoUploadHeader, codes.InvalidArgument, "reading an upload's header")
	}

	name, contentType := header.GetName(), header.GetContentType()

	switch {
	case !ValidName(name):
		return s.fail(req, ErrInvalidObjectName, codes.InvalidArgument, "reading an upload's name")
	case !s.contentTypes(contentType):
		return s.fail(req, ErrContentTypeRefused, codes.InvalidArgument, "checking an upload's content type")
	}

	subject := SubjectFromProto(header.GetBelongsTo())
	if err = subject.Validate(); err != nil {
		return s.fail(req, err, codes.InvalidArgument, "reading an upload's subject")
	}

	if !mayAttach(req.caller, subject) {
		return s.absent(req, "an upload attached to a subject other than the caller")
	}

	objectID := identifiers.New()
	key := s.keys(req.caller, objectID, name)

	req.op.Set(objectIDKey, objectID)
	req.op.Set(objectKeyKey, key)

	occupied, err := s.manager.Exists(ctx, key)
	switch {
	case err != nil:
		return s.fail(req, err, codes.Internal, "checking whether an upload's key is free")
	case occupied:
		return s.fail(req, mediaregistry.ErrObjectKeyOccupied, codes.AlreadyExists, "checking whether an upload's key is free")
	}

	body := NewUploadReader(stream.Recv, s.maxBytes)

	if err = s.manager.Save(ctx, key, body, uploads.WithContentType(contentType)); err != nil {
		// The reader's own failure is the one the client caused and is told
		// about; the manager's wrapping of it is the same fact with less in it.
		// A stream that broke is neither the client's mistake nor the
		// server's fault, and is answered the way a broken header is.
		switch {
		case body.Refusal() != nil:
			return s.fail(req, body.Refusal(), codes.InvalidArgument, "receiving an upload's bytes")
		case body.Broken() != nil:
			return s.fail(req, body.Broken(), StreamBrokenCode(body.Broken()), "receiving an upload's bytes")
		}

		return s.fail(req, err, codes.Internal, "storing an upload")
	}

	req.op.Set(sizeKey, body.Size())

	var registered *mediaregistry.Object

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		object, recordErr := s.store.RecordObject(ctx, tx, req.caller.Scope, mediaregistry.ObjectInput{
			ID:          objectID,
			Key:         key,
			ContentType: contentType,
			OwnerID:     req.caller.PrincipalID,
			BelongsTo:   subject,
			Size:        body.Size(),
		})
		registered = object

		return recordErr
	}); err != nil {
		if deleteErr := s.manager.Delete(ctx, key); deleteErr != nil {
			req.op.Acknowledge(deleteErr, "removing the bytes of an upload whose registration failed")
		}

		return s.fail(req, err, codes.Internal, "registering an upload")
	}

	if s.afterUpload != nil {
		if afterErr := s.afterUpload(ctx, req.caller, registered); afterErr != nil {
			req.op.Acknowledge(afterErr, "running the deployment's after-upload hook")
		}
	}

	if err = stream.SendAndClose(&mediaregistrypb.UploadObjectResponse{Result: ObjectToProto(registered)}); err != nil {
		return s.fail(req, err, codes.Internal, "answering an upload")
	}

	return nil
}

// UploadReader is an io.Reader over an upload's chunks, after its header has
// been taken off.
//
// It is exported for the domain upload RPCs the package documentation sends an
// attachment to one of the consumer's nouns through. Such an RPC's messages
// wrap mediaregistrypb.UploadObjectRequest rather than being it, which is why
// the reader takes a receive function over the platform message and not the
// platform stream: the consumer unwraps each of its own messages on the way
// in. UploadObject reads through the same reader, so the two receive paths
// cannot come to disagree about what an upload is.
//
// It holds the chunk the stream last delivered and nothing else, so an upload
// of any size costs one message's worth of memory. It counts what it hands
// over, which is the size the row records, and refuses the read that would
// take the count past the cap — before handing any of that chunk over — so
// whatever it is handed to is never given a byte beyond it.
//
// A failed Read is one of two facts, and the handler answers them differently.
// Refusal is the client's mistake, kept apart from io.EOF and from whatever
// the reader's consumer wraps it in, so the client is told what they did
// rather than what the manager made of it. Broken is the stream's: the client
// went away or its deadline passed partway through, which the manager would
// otherwise report as a failure to store, and which StreamBrokenCode answers.
type UploadReader struct {
	recv    func() (*mediaregistrypb.UploadObjectRequest, error)
	refusal error
	broken  error
	buf     []byte
	n       int64
	limit   int64
}

var _ io.Reader = (*UploadReader)(nil)

// NewUploadReader reads an upload's chunks from recv, whose header the caller
// has already received and checked. maxBytes is the cap, and zero or less is
// none — the reading WithoutMaxBytes gives the server's.
//
// recv is called until it returns io.EOF, which ends the upload, or another
// error, which is Broken. A message carrying a header instead of a chunk is
// ErrRepeatedUploadHeader.
func NewUploadReader(recv func() (*mediaregistrypb.UploadObjectRequest, error), maxBytes int64) *UploadReader {
	return &UploadReader{recv: recv, limit: maxBytes}
}

func (r *UploadReader) Read(p []byte) (int, error) {
	if r.refusal != nil {
		return 0, r.refusal
	}

	if r.broken != nil {
		return 0, r.broken
	}

	for len(r.buf) == 0 {
		msg, err := r.recv()
		if errors.Is(err, io.EOF) {
			return 0, io.EOF
		}

		if err != nil {
			r.broken = err

			return 0, err
		}

		if msg.GetHeader() != nil {
			r.refusal = ErrRepeatedUploadHeader

			return 0, r.refusal
		}

		chunk := msg.GetChunk()
		if r.limit > 0 && r.n+int64(len(chunk)) > r.limit {
			r.refusal = ErrObjectTooLarge

			return 0, r.refusal
		}

		r.buf = chunk
	}

	copied := copy(p, r.buf)
	r.buf = r.buf[copied:]
	r.n += int64(copied)

	return copied, nil
}

// Size is how many bytes the reader has handed over: the object's size once a
// Read has returned io.EOF, and the size its row records.
func (r *UploadReader) Size() int64 {
	return r.n
}

// Refusal is the client's mistake that stopped the upload —
// ErrObjectTooLarge or ErrRepeatedUploadHeader, each answered with
// InvalidArgument — or nil.
func (r *UploadReader) Refusal() error {
	return r.refusal
}

// Broken is the stream's own failure to deliver its next message, or nil. Its
// code is StreamBrokenCode's, never Internal.
func (r *UploadReader) Broken() error {
	return r.broken
}

// StreamBrokenCode is the code for a stream that failed to deliver its next
// message: the transport's own, where it names one — Canceled for a client
// that went away, DeadlineExceeded for one whose deadline passed — and
// Canceled otherwise. Never Internal: nothing on this side failed.
func StreamBrokenCode(err error) codes.Code {
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown && st.Code() != codes.OK {
		return st.Code()
	}

	return codes.Canceled
}
