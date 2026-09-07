/*
Package client is a typed client for identity's gRPC service.

It is the generated stub plus the two interceptors a caller of this module's
services would otherwise wire by hand, and it is deliberately thin: every RPC
reaches it by embedding, so this file adds no method of its own beyond
construction and shutdown. A client that wrapped each RPC would be twenty-eight
functions that can drift from the schema, to gain nothing.

It is imported as identityclient.

# The two interceptors, and why they are on by default

Error decoding, because without it every sentinel identity returns arrives as a
*status.Error that no errors.Is matches. The server encodes the sentinel into
the status details and the client has to decode it; that has always been a
function a caller could forget, and here it is a default instead.

Both idioms work on what comes back:

	if errors.Is(err, identity.ErrUsernameTaken) { ... }   // std errors, matches
	if status.Code(err) == codes.AlreadyExists { ... }     // and so does the code

That the first works is not automatic — what crosses a connection is the error's
cockroachdb mark and not the sentinel's identity — and errors/grpc's decoding
interceptor is what makes it so.

Idempotency, because a retried write that mints a second invitation or a second
account is the failure the interceptor exists to prevent. It stamps a key the
caller put on the context and never mints one of its own — a client that
generated keys by itself would make every call idempotent-looking and none of
them idempotent, since a retry would carry a fresh key. Use
idempotency.NewIdempotencyContext to start one.

Both are defaults rather than obligations: WithoutDefaultInterceptors turns them
off for a caller assembling their own chain.
*/
package client

import (
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	idempotencygrpc "github.com/primandproper/platform-go/v14/idempotency/grpc"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial. It wraps
// errors.ErrEmptyInputParameter, so a caller may check either.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty grpc target")

// Client is IdentityServiceClient over a connection this package owns.
//
// It embeds the generated interface, so every RPC is a method on this type with
// the signature the schema gave it.
type Client struct {
	identitypb.IdentityServiceClient

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
// Nothing here supplies transport security: what a connection to your own
// service is secured with is yours, and a default of insecure.NewCredentials
// would be this package choosing plaintext on your behalf. grpc.NewClient
// refuses a target with no credentials option, which is the right failure.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with neither the error-decoding nor
// the idempotency interceptor, for a caller assembling their own chain.
//
// The cost of using it is the one this package's documentation opens with: an
// errors.Is against an identity sentinel then never matches.
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
		return nil, platformerrors.Wrapf(err, "dialing identity service at %q", target)
	}

	return &Client{IdentityServiceClient: identitypb.NewIdentityServiceClient(conn), conn: conn}, nil
}

// Wrap builds a client over a connection somebody else owns.
//
// It is what a consumer with one connection to a process serving several of this
// module's services uses, and what a test over bufconn uses. Close on the result
// closes nothing, because the connection is not this client's to close — which
// is the whole difference between this and New.
//
// The interceptors are not applied here and cannot be: they are dial options,
// and the connection has already been dialed. A caller wrapping their own
// connection installs grpcerrors.UnaryErrorDecodingInterceptor on it themselves,
// and this function's doc is the reminder.
func Wrap(conn grpc.ClientConnInterface) *Client {
	return &Client{IdentityServiceClient: identitypb.NewIdentityServiceClient(conn)}
}

// DefaultInterceptors is the dial option New applies: error decoding, then
// idempotency.
//
// It is exported so a caller assembling one connection for several services can
// install the same chain rather than approximating it.
func DefaultInterceptors() grpc.DialOption { return defaultInterceptors() }

func defaultInterceptors() grpc.DialOption {
	return grpc.WithChainUnaryInterceptor(
		grpcerrors.UnaryErrorDecodingInterceptor(),
		idempotencygrpc.NewUnaryClientInterceptor(),
	)
}

// Close closes the connection, if this client owns one.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}

	return c.conn.Close()
}
