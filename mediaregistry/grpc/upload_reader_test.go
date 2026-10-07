package grpc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	mediaregistrygrpc "github.com/primandproper/platform-go/v15/mediaregistry/grpc"
	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// recvOf is a receive function over msgs, ending with end once they run out.
func recvOf(end error, msgs ...*mediaregistrypb.UploadObjectRequest) func() (*mediaregistrypb.UploadObjectRequest, error) {
	return func() (*mediaregistrypb.UploadObjectRequest, error) {
		if len(msgs) == 0 {
			return nil, end
		}

		msg := msgs[0]
		msgs = msgs[1:]

		return msg, nil
	}
}

func chunk(b []byte) *mediaregistrypb.UploadObjectRequest {
	return &mediaregistrypb.UploadObjectRequest{Part: &mediaregistrypb.UploadObjectRequest_Chunk{Chunk: b}}
}

func header() *mediaregistrypb.UploadObjectRequest {
	return &mediaregistrypb.UploadObjectRequest{Part: &mediaregistrypb.UploadObjectRequest_Header{
		Header: &mediaregistrypb.UploadObjectHeader{Name: "again.png", ContentType: pngType},
	}}
}

func TestUploadReader(T *testing.T) {
	T.Parallel()

	T.Run("hands over every chunk and counts what it handed over", func(t *testing.T) {
		t.Parallel()

		r := mediaregistrygrpc.NewUploadReader(recvOf(io.EOF, chunk([]byte("abc")), chunk(nil), chunk([]byte("defg"))), 7)

		got, err := io.ReadAll(r)
		must.NoError(t, err)
		test.Eq(t, []byte("abcdefg"), got)
		test.EqOp(t, int64(7), r.Size())
		test.NoError(t, r.Refusal())
		test.NoError(t, r.Broken())
	})

	T.Run("refuses the chunk that would cross the cap, before handing any of it over", func(t *testing.T) {
		t.Parallel()

		r := mediaregistrygrpc.NewUploadReader(recvOf(io.EOF, chunk([]byte("abc")), chunk([]byte("defg"))), 6)

		got, err := io.ReadAll(r)
		test.ErrorIs(t, err, mediaregistrygrpc.ErrObjectTooLarge)
		test.Eq(t, []byte("abc"), got)
		test.EqOp(t, int64(3), r.Size())
		test.ErrorIs(t, r.Refusal(), mediaregistrygrpc.ErrObjectTooLarge)
		test.NoError(t, r.Broken())

		// The refusal is sticky: nothing past the cap is ever read.
		_, err = r.Read(make([]byte, 8))
		test.ErrorIs(t, err, mediaregistrygrpc.ErrObjectTooLarge)
	})

	T.Run("a cap of zero is none", func(t *testing.T) {
		t.Parallel()

		content := bytes.Repeat([]byte("x"), 1<<16)
		r := mediaregistrygrpc.NewUploadReader(recvOf(io.EOF, chunk(content)), 0)

		got, err := io.ReadAll(r)
		must.NoError(t, err)
		test.Eq(t, content, got)
	})

	T.Run("a second header is the client's mistake", func(t *testing.T) {
		t.Parallel()

		r := mediaregistrygrpc.NewUploadReader(recvOf(io.EOF, chunk([]byte("abc")), header()), 0)

		_, err := io.ReadAll(r)
		test.ErrorIs(t, err, mediaregistrygrpc.ErrRepeatedUploadHeader)
		test.ErrorIs(t, r.Refusal(), mediaregistrygrpc.ErrRepeatedUploadHeader)
		test.NoError(t, r.Broken())
	})

	T.Run("a stream that stops delivering is broken, not refused", func(t *testing.T) {
		t.Parallel()

		gone := status.FromContextError(context.Canceled).Err()
		calls := 0
		recv := recvOf(gone, chunk([]byte("abc")))
		r := mediaregistrygrpc.NewUploadReader(func() (*mediaregistrypb.UploadObjectRequest, error) {
			calls++

			return recv()
		}, 0)

		_, err := io.ReadAll(r)
		test.ErrorIs(t, err, gone)
		test.ErrorIs(t, r.Broken(), gone)
		test.NoError(t, r.Refusal())
		test.EqOp(t, codes.Canceled, mediaregistrygrpc.StreamBrokenCode(r.Broken()))

		// A broken stream stays broken rather than being asked again.
		_, err = r.Read(make([]byte, 8))
		test.ErrorIs(t, err, gone)
		test.EqOp(t, 2, calls)
	})
}

func TestStreamBrokenCode(T *testing.T) {
	T.Parallel()

	for name, tc := range map[string]struct {
		err  error
		want codes.Code
	}{
		"a client that went away":        {err: status.FromContextError(context.Canceled).Err(), want: codes.Canceled},
		"a deadline that passed":         {err: status.FromContextError(context.DeadlineExceeded).Err(), want: codes.DeadlineExceeded},
		"a transport error with no code": {err: errors.New("connection reset"), want: codes.Canceled},
		"a status that names nothing":    {err: status.Error(codes.Unknown, "?"), want: codes.Canceled},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			// Never Internal: a stream that broke is not the server's fault.
			test.EqOp(t, tc.want, mediaregistrygrpc.StreamBrokenCode(tc.err))
		})
	}
}

func TestValidName(T *testing.T) {
	T.Parallel()

	for _, name := range []string{"photo.png", ".hidden", "a..b", "with space.jpg"} {
		test.True(T, mediaregistrygrpc.ValidName(name), test.Sprintf("name %q", name))
	}

	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "../../bob/x.png", "/abs"} {
		test.False(T, mediaregistrygrpc.ValidName(name), test.Sprintf("name %q", name))
	}
}
