package client_test

import (
	"testing"

	mediaregistryclient "github.com/primandproper/platform-go/v15/mediaregistry/grpc/client"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("an empty target is refused", func(t *testing.T) {
		t.Parallel()

		c, err := mediaregistryclient.New("")
		test.Nil(t, c)
		test.ErrorIs(t, err, mediaregistryclient.ErrEmptyTarget)
		test.True(t, platformerrors.Is(err, platformerrors.ErrEmptyInputParameter))
	})

	T.Run("no transport security is supplied on the caller's behalf", func(t *testing.T) {
		t.Parallel()

		_, err := mediaregistryclient.New("localhost:1")
		test.Error(t, err)
	})

	T.Run("a client over its own connection closes it", func(t *testing.T) {
		t.Parallel()

		c, err := mediaregistryclient.New("localhost:1",
			mediaregistryclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())),
			mediaregistryclient.WithoutDefaultInterceptors(),
			nil)
		must.NoError(t, err)
		test.NoError(t, c.Close())
	})

	T.Run("a wrapped connection is not the client's to close", func(t *testing.T) {
		t.Parallel()

		conn, err := grpc.NewClient("localhost:1", grpc.WithTransportCredentials(insecure.NewCredentials()))
		must.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		test.NoError(t, mediaregistryclient.Wrap(conn).Close())
	})

	T.Run("the default chain decodes unary calls and streams", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 2, mediaregistryclient.DefaultInterceptors())
		test.NotNil(t, mediaregistryclient.StreamErrorDecodingInterceptor())
	})
}
