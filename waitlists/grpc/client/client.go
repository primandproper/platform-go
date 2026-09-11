/*
Package client is a typed client for the waitlists gRPC service.

It is the generated stub plus the interceptor a caller of this module's services
would otherwise wire by hand, and it is deliberately thin: every RPC reaches it
by embedding, so this file adds no method of its own beyond construction and
shutdown. A client that wrapped each RPC would be seventeen functions that can
drift from the schema, to gain nothing.

It is imported as waitlistsclient.

# The interceptor, and why it is on by default

Error decoding, because without it every sentinel this service returns arrives
as a *status.Error that no errors.Is matches. The server encodes the sentinel
into the status details and the client has to decode it; that has always been a
function a caller could forget, and here it is a default instead.

Both idioms work on what comes back:

	if errors.Is(err, waitlists.ErrContactWithdrawn) { ... }  // std errors, matches
	if status.Code(err) == codes.FailedPrecondition { ... }   // and so does the code

It matters more here than on the surfaces next door, because the client of the
public three is frequently rendering a page for the person who caused the
refusal. Four of this service's five refusals share FailedPrecondition — a
closed list, a contact that has withdrawn, a transition from the wrong status,
and a second withdrawal — and the code alone does not say which sentence to put
on the page.

# Why there is no idempotency interceptor

identity's client applies one and this does not, and the reason is the one
authentication/oauth2clients' client gives in its own words: what a recorded
reply would hold. Here it is not a credential but an address. An idempotency
store keeps a response so it can be replayed, and the response to Join carries
the contact somebody typed into a signup form — an address the deployment has
promised to use for one thing, in a second store nobody counted when they wrote
that promise down.

The cost is smaller than it looks. Join is already idempotent where it matters:
a retried join finds the contact on the list and is refused with
waitlists.ErrAlreadySignedUp rather than adding somebody twice, because the
uniqueness is on the digest and not on a request identifier. Fifteen of the
seventeen RPCs are naturally idempotent, and the sixteenth — CreateList — mints
a row a retry would duplicate, which is a list somebody archives.
*/
package client

import (
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial. It wraps
// errors.ErrEmptyInputParameter, so a caller may check either.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter,
	"empty waitlists service grpc target")

// Client is WaitlistsServiceClient over a connection this package owns.
//
// It embeds the generated interface, so every RPC is a method on this type with
// the signature the schema gave it.
type Client struct {
	waitlistspb.WaitlistsServiceClient

	conn *grpc.ClientConn
}

// options are what the constructors accumulate.
type options struct {
	dialOptions      []grpc.DialOption
	skipInterceptors bool
}

// Option configures a Client.
type Option func(*options)

// WithDialOptions adds gRPC dial options — transport credentials, per-RPC
// credentials, a resolver.
//
// Nothing here supplies transport security. grpc.NewClient refuses a target
// with no credentials option, which is the right failure and is left in place:
// Join carries an address somebody typed into a form, and insecure.NewCredentials
// is a decision to make deliberately rather than one this package makes on a
// caller's behalf.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with no error-decoding
// interceptor, for a caller assembling their own chain.
//
// The cost of using it is the one this package's documentation opens with: an
// errors.Is against a waitlists sentinel then never matches, and four of the
// five refusals share a code.
func WithoutDefaultInterceptors() Option {
	return func(o *options) { o.skipInterceptors = true }
}

// New dials target and returns a client over it.
//
// The connection is this client's, and Close closes it.
func New(target string, opts ...Option) (*Client, error) {
	if target == "" {
		return nil, ErrEmptyTarget
	}

	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	dialOptions := o.dialOptions
	if !o.skipInterceptors {
		dialOptions = append(dialOptions, defaultInterceptors())
	}

	conn, err := grpc.NewClient(target, dialOptions...)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "dialing waitlists service at %q", target)
	}

	return &Client{
		WaitlistsServiceClient: waitlistspb.NewWaitlistsServiceClient(conn),
		conn:                   conn,
	}, nil
}

// Wrap builds a client over a connection somebody else owns.
//
// It is what a consumer with one connection to a process serving several of this
// module's services uses, and what a test over bufconn uses. Close on the result
// closes nothing, because the connection is not this client's to close — which
// is the whole difference between this and New.
//
// The interceptor is not applied here and cannot be: it is a dial option, and
// the connection has already been dialed. A caller wrapping their own connection
// installs grpcerrors.UnaryErrorDecodingInterceptor on it themselves, and this
// function's doc is the reminder.
func Wrap(conn grpc.ClientConnInterface) *Client {
	return &Client{WaitlistsServiceClient: waitlistspb.NewWaitlistsServiceClient(conn)}
}

// DefaultInterceptors is the dial option New applies: error decoding.
//
// It is exported so a caller assembling one connection for several services can
// install the same chain rather than approximating it. Note that it is not
// identity's client's chain, which also carries idempotency — see this package's
// documentation for why a response carrying somebody's address is not replayed
// out of a second store.
func DefaultInterceptors() grpc.DialOption { return defaultInterceptors() }

func defaultInterceptors() grpc.DialOption {
	return grpc.WithChainUnaryInterceptor(grpcerrors.UnaryErrorDecodingInterceptor())
}

// Close closes the connection, if this client owns one.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}

	return c.conn.Close()
}
