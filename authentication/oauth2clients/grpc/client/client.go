/*
Package client is a typed client for the OAuth2 client registry gRPC service.

It is the generated stub plus the interceptor a caller of this module's services
would otherwise wire by hand, and it is deliberately thin: every RPC reaches it
by embedding, so this file adds no method of its own beyond construction and
shutdown. A client that wrapped each RPC would be ten functions that can drift
from the schema, to gain nothing.

It is imported as oauth2clientsclient.

# The interceptor, and why it is on by default

Error decoding, because without it every sentinel this service returns arrives
as a *status.Error that no errors.Is matches. The server encodes the sentinel
into the status details and the client has to decode it; that has always been a
function a caller could forget, and here it is a default instead.

Both idioms work on what comes back:

	if errors.Is(err, oauth2clients.ErrClientNotFound) { ... }  // std errors, matches
	if status.Code(err) == codes.NotFound { ... }               // and so does the code

It matters here because three of this service's refusals share PermissionDenied
and four share InvalidArgument, so the code alone frequently does not say which
field to fix.

# Why there is no idempotency interceptor

identity's client applies one and this does not, and the difference is what a
recorded reply would hold. An idempotency store keeps a response so it can be
replayed; the response to either creation RPC here is a live client secret.
A credential whose whole security property is that it was sent once does not
belong in a store whose purpose is to hand the same bytes back a second time.

The cost is that a retried creation mints a second registration rather than
returning the first. That is the safer failure — a duplicate client somebody
archives, rather than a secret in a second store — and it is the same reading
authentication/signin's client takes of a token.
*/
package client

import (
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial. It wraps
// errors.ErrEmptyInputParameter, so a caller may check either.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter,
	"empty oauth2 clients service grpc target")

// Client is OAuth2ClientsServiceClient over a connection this package owns.
//
// It embeds the generated interface, so every RPC is a method on this type with
// the signature the schema gave it.
type Client struct {
	oauth2clientspb.OAuth2ClientsServiceClient

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
// Nothing here supplies transport security, and on this service that is worth
// stating rather than assuming: both creation RPCs answer with a plaintext
// client secret. grpc.NewClient refuses a target with no credentials option,
// which is the right failure, and insecure.NewCredentials is a decision to make
// deliberately and not one this package will make on your behalf.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with no error-decoding
// interceptor, for a caller assembling their own chain.
//
// The cost of using it is the one this package's documentation opens with: an
// errors.Is against a registry sentinel then never matches, and the codes alone
// do not tell the refusals apart.
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
		return nil, platformerrors.Wrapf(err, "dialing oauth2 clients service at %q", target)
	}

	return &Client{
		OAuth2ClientsServiceClient: oauth2clientspb.NewOAuth2ClientsServiceClient(conn),
		conn:                       conn,
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
	return &Client{OAuth2ClientsServiceClient: oauth2clientspb.NewOAuth2ClientsServiceClient(conn)}
}

// DefaultInterceptors is the dial option New applies: error decoding.
//
// It is exported so a caller assembling one connection for several services can
// install the same chain rather than approximating it. Note that it is not
// identity's client's chain, which also carries idempotency — see this package's
// documentation for why a minted secret must not be replayable.
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
