package client_test

import (
	"testing"

	webhooksclient "github.com/primandproper/platform-go/v14/webhooks/grpc/client"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestNewRefusesAnEmptyTarget(T *testing.T) {
	T.Parallel()

	c, err := webhooksclient.New("")
	test.Nil(T, c)
	must.Error(T, err)

	// Both spellings match, which is what the sentinel wrapping is for: a
	// consumer checking the platform sentinel and one checking this package's
	// own name are asking the same question.
	test.ErrorIs(T, err, webhooksclient.ErrEmptyTarget)
	test.True(T, platformerrors.Is(err, platformerrors.ErrEmptyInputParameter))
}

// TestNewSuppliesNoTransportSecurity is the property worth pinning hardest on
// this service.
//
// SaveEndpoint carries an endpoint's HMAC signing keys in its request. A default
// of insecure credentials would be this package choosing to put those on the
// wire in the clear for somebody, so grpc.NewClient refusing a target with no
// credentials option is the right failure and is left in place.
func TestNewSuppliesNoTransportSecurity(T *testing.T) {
	T.Parallel()

	c, err := webhooksclient.New("passthrough:///nowhere")
	if c != nil {
		T.Cleanup(func() { _ = c.Close() })
	}

	test.Error(T, err)
}

func TestNewDialsWithCredentials(T *testing.T) {
	T.Parallel()

	c, err := webhooksclient.New("passthrough:///nowhere",
		webhooksclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	// Every RPC is a method on the result, by embedding, which is the whole
	// reason this package adds none of its own.
	var _ webhookspb.WebhooksServiceClient = c

	test.NoError(T, c.Close())
}

// TestCloseOnAWrappedClientClosesNothing is the difference between New and Wrap,
// and it matters here for the same reason it matters for the directory: a
// consumer with one connection to a process serving several of this module's
// services would otherwise close it by tidying up one client.
func TestCloseOnAWrappedClientClosesNothing(T *testing.T) {
	T.Parallel()

	conn, err := grpc.NewClient("passthrough:///nowhere",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	must.NoError(T, err)
	T.Cleanup(func() { _ = conn.Close() })

	c := webhooksclient.Wrap(conn)
	must.NotNil(T, c)

	test.NoError(T, c.Close())

	// The connection is still usable, which is what "closes nothing" means.
	test.NoError(T, conn.Close())
}

func TestDefaultInterceptorsIsADialOption(T *testing.T) {
	T.Parallel()

	test.NotNil(T, webhooksclient.DefaultInterceptors())
}

// TestWithoutDefaultInterceptorsBuildsAClient is the escape hatch for a caller
// assembling their own chain. What declining the defaults costs them is an
// errors.Is against a webhooks sentinel that never matches; what is checked here
// is that declining them is not declining the client.
func TestWithoutDefaultInterceptorsBuildsAClient(T *testing.T) {
	T.Parallel()

	c, err := webhooksclient.New("passthrough:///nowhere",
		webhooksclient.WithoutDefaultInterceptors(),
		webhooksclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}

// TestNilOptionsAreIgnored: a caller building an option list conditionally ends
// up with a nil in it, and a constructor that panicked on one would make the
// conditional the caller's problem.
func TestNilOptionsAreIgnored(T *testing.T) {
	T.Parallel()

	c, err := webhooksclient.New("passthrough:///nowhere",
		nil,
		webhooksclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}
