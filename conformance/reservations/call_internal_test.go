package reservations

import (
	"context"
	"io"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	_ "google.golang.org/protobuf/types/known/emptypb"
)

// streamer is a service with one method of each shape, since no service this
// module ships streams and call must still open one where a descriptor says to.
func streamer(t *testing.T) protoreflect.ServiceDescriptor {
	t.Helper()

	empty := ".google.protobuf.Empty"
	method := func(name string, client, server bool) *descriptorpb.MethodDescriptorProto {
		return &descriptorpb.MethodDescriptorProto{
			Name:            new(name),
			InputType:       new(empty),
			OutputType:      new(empty),
			ClientStreaming: new(client),
			ServerStreaming: new(server),
		}
	}

	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       new("conformance/reservations/streamer.proto"),
		Package:    new("conformance.reservations"),
		Syntax:     new("proto3"),
		Dependency: []string{"google/protobuf/empty.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: new("Streamer"),
			Method: []*descriptorpb.MethodDescriptorProto{
				method("Unary", false, false),
				method("Download", false, true),
				method("Upload", true, false),
			},
		}},
	}, protoregistry.GlobalFiles)
	must.NoError(t, err)

	return file.Services().Get(0)
}

// stream is a client stream answering its one read with recv, keeping what it
// was sent.
type stream struct {
	grpc.ClientStream

	recv   error
	sent   int
	closed bool
}

func (s *stream) SendMsg(any) error {
	s.sent++

	return nil
}

func (s *stream) CloseSend() error {
	s.closed = true

	return nil
}

func (s *stream) RecvMsg(any) error { return s.recv }

// conn answers a unary call with invoke and opens s for a stream, recording
// which of the two it was asked for.
type conn struct {
	invoke error
	s      *stream
	desc   *grpc.StreamDesc
	calls  []string
}

func (c *conn) Invoke(_ context.Context, method string, _, _ any, _ ...grpc.CallOption) error {
	c.calls = append(c.calls, "invoke "+method)

	return c.invoke
}

func (c *conn) NewStream(_ context.Context, desc *grpc.StreamDesc, method string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	c.calls = append(c.calls, "stream "+method)
	c.desc = desc

	return c.s, nil
}

func TestCall(T *testing.T) {
	T.Parallel()

	T.Run("a unary method is invoked, and its answer returned", func(t *testing.T) {
		t.Parallel()

		method := streamer(t).Methods().ByName("Unary")
		c := &conn{invoke: status.Error(codes.PermissionDenied, "reserved")}

		err := call(t.Context(), c, "/conformance.reservations.Streamer/Unary", method)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		test.Eq(t, []string{"invoke /conformance.reservations.Streamer/Unary"}, c.calls)
	})

	T.Run("a server-streaming method is sent its one request, closed and read", func(t *testing.T) {
		t.Parallel()

		method := streamer(t).Methods().ByName("Download")
		c := &conn{s: &stream{recv: status.Error(codes.PermissionDenied, "reserved")}}

		err := call(t.Context(), c, "/conformance.reservations.Streamer/Download", method)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		test.Eq(t, []string{"stream /conformance.reservations.Streamer/Download"}, c.calls)
		must.NotNil(t, c.desc)
		test.True(t, c.desc.ServerStreams)
		test.False(t, c.desc.ClientStreams)
		test.EqOp(t, 1, c.s.sent)
		test.True(t, c.s.closed)
	})

	T.Run("a client-streaming method is sent nothing, closed and read", func(t *testing.T) {
		t.Parallel()

		method := streamer(t).Methods().ByName("Upload")
		c := &conn{s: &stream{recv: status.Error(codes.PermissionDenied, "reserved")}}

		err := call(t.Context(), c, "/conformance.reservations.Streamer/Upload", method)
		test.EqOp(t, codes.PermissionDenied, status.Code(err))
		must.NotNil(t, c.desc)
		test.True(t, c.desc.ClientStreams)
		test.EqOp(t, 0, c.s.sent)
		test.True(t, c.s.closed)
	})

	T.Run("a stream that ends without a message was answered", func(t *testing.T) {
		t.Parallel()

		method := streamer(t).Methods().ByName("Download")
		c := &conn{s: &stream{recv: io.EOF}}

		test.NoError(t, call(t.Context(), c, "/conformance.reservations.Streamer/Download", method))
	})
}
