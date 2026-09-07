package client_test

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v14/errors"
	identityclient "github.com/primandproper/platform-go/v14/identity/grpc/client"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestNewRefusesAnEmptyTarget(T *testing.T) {
	T.Parallel()

	c, err := identityclient.New("")
	test.Nil(T, c)
	must.Error(T, err)
	test.True(T, platformerrors.Is(err, platformerrors.ErrEmptyInputParameter))
}

// TestNewSuppliesNoTransportSecurity is the property worth pinning: a default of
// insecure credentials would be this package choosing plaintext for somebody,
// and grpc.NewClient refusing is the right failure.
func TestNewSuppliesNoTransportSecurity(T *testing.T) {
	T.Parallel()

	c, err := identityclient.New("passthrough:///nowhere")
	if c != nil {
		t := T
		t.Cleanup(func() { _ = c.Close() })
	}

	test.Error(T, err)
}

func TestNewDialsWithCredentials(T *testing.T) {
	T.Parallel()

	c, err := identityclient.New("passthrough:///nowhere",
		identityclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}

// TestCloseOnAWrappedClientClosesNothing is the difference between New and Wrap,
// and it matters: a consumer sharing one connection across several of this
// module's services would otherwise close it by tidying up one client.
func TestCloseOnAWrappedClientClosesNothing(T *testing.T) {
	T.Parallel()

	conn, err := grpc.NewClient("passthrough:///nowhere",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	must.NoError(T, err)
	T.Cleanup(func() { _ = conn.Close() })

	c := identityclient.Wrap(conn)
	must.NotNil(T, c)

	test.NoError(T, c.Close())

	// The connection is still usable, which is what "closes nothing" means.
	test.NoError(T, conn.Close())
}

func TestDefaultInterceptorsIsADialOption(T *testing.T) {
	T.Parallel()

	test.NotNil(T, identityclient.DefaultInterceptors())
}

// TestWithoutDefaultInterceptorsBuildsAClient is the escape hatch for a caller
// assembling their own chain. What it costs them is asserted where a server
// exists to assert it against — see identity/grpc's suite — and what is checked
// here is that declining the defaults is not declining the client.
func TestWithoutDefaultInterceptorsBuildsAClient(T *testing.T) {
	T.Parallel()

	c, err := identityclient.New("passthrough:///nowhere",
		identityclient.WithoutDefaultInterceptors(),
		identityclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}

// TestNilOptionsAreIgnored: a caller building an option list conditionally ends
// up with a nil in it, and a constructor that panicked on one would make the
// conditional the caller's problem.
func TestNilOptionsAreIgnored(T *testing.T) {
	T.Parallel()

	c, err := identityclient.New("passthrough:///nowhere",
		nil,
		identityclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}
