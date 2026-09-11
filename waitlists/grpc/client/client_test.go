package client_test

import (
	"testing"

	waitlistsclient "github.com/primandproper/platform-go/v14/waitlists/grpc/client"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestNewRefusesAnEmptyTarget(T *testing.T) {
	T.Parallel()

	c, err := waitlistsclient.New("")
	test.Nil(T, c)
	must.Error(T, err)

	// Both spellings match, which is what the sentinel wrapping is for: a
	// consumer checking the platform sentinel and one checking this package's
	// own name are asking the same question.
	test.ErrorIs(T, err, waitlistsclient.ErrEmptyTarget)
	test.True(T, platformerrors.Is(err, platformerrors.ErrEmptyInputParameter))
}

// TestNewSuppliesNoTransportSecurity: grpc.NewClient refuses a target with no
// credentials option, and that refusal is left in place. Join carries an address
// somebody typed into a form, and insecure.NewCredentials is a decision to make
// deliberately rather than one this package makes on a caller's behalf.
func TestNewSuppliesNoTransportSecurity(T *testing.T) {
	T.Parallel()

	c, err := waitlistsclient.New("passthrough:///nowhere")
	if c != nil {
		T.Cleanup(func() { _ = c.Close() })
	}

	test.Error(T, err)
}

func TestNewDialsWithCredentials(T *testing.T) {
	T.Parallel()

	c, err := waitlistsclient.New("passthrough:///nowhere",
		waitlistsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	// Every RPC is a method on the result, by embedding, which is the whole
	// reason this package adds none of its own.
	var _ waitlistspb.WaitlistsServiceClient = c

	test.NoError(T, c.Close())
}

// TestCloseOnAWrappedClientClosesNothing is the difference between New and Wrap,
// and it matters for the reason it matters on the directory: a consumer with one
// connection to a process serving several of this module's services would
// otherwise close it by tidying up one client.
func TestCloseOnAWrappedClientClosesNothing(T *testing.T) {
	T.Parallel()

	conn, err := grpc.NewClient("passthrough:///nowhere",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	must.NoError(T, err)
	T.Cleanup(func() { _ = conn.Close() })

	c := waitlistsclient.Wrap(conn)
	must.NotNil(T, c)

	test.NoError(T, c.Close())

	// The connection is still usable, which is what "closes nothing" means.
	test.NoError(T, conn.Close())
}

func TestDefaultInterceptorsIsADialOption(T *testing.T) {
	T.Parallel()

	test.NotNil(T, waitlistsclient.DefaultInterceptors())
}

// TestWithoutDefaultInterceptorsBuildsAClient is the escape hatch for a caller
// assembling their own chain. What declining the defaults costs them is an
// errors.Is against a waitlists sentinel that never matches — which on this
// service is four refusals sharing FailedPrecondition; what is checked here is
// that declining them is not declining the client.
func TestWithoutDefaultInterceptorsBuildsAClient(T *testing.T) {
	T.Parallel()

	c, err := waitlistsclient.New("passthrough:///nowhere",
		waitlistsclient.WithoutDefaultInterceptors(),
		waitlistsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}

// TestNilOptionsAreIgnored: a caller building an option list conditionally ends
// up with a nil in it, and a constructor that panicked on one would make the
// conditional the caller's problem.
func TestNilOptionsAreIgnored(T *testing.T) {
	T.Parallel()

	c, err := waitlistsclient.New("passthrough:///nowhere",
		nil,
		waitlistsclient.WithDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	must.NoError(T, err)
	must.NotNil(T, c)

	test.NoError(T, c.Close())
}
