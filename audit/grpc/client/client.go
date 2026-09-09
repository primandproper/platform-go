/*
Package client is a typed client for the audit log's gRPC service.

It is the generated stub plus the interceptor a caller of this module's services
would otherwise wire by hand, and it is deliberately thin: every RPC reaches it
by embedding, so this file adds no method of its own beyond construction and
shutdown. A client that wrapped each RPC would be three functions that can drift
from the schema, to gain nothing.

It is imported as auditclient.

# The interceptor, and why it is on by default

Error decoding, because without it every sentinel the audit reader returns
arrives as a *status.Error that no errors.Is matches. The server encodes the
sentinel into the status details and the client has to decode it; that has always
been a function a caller could forget, and here it is a default instead.

Both idioms work on what comes back:

	if errors.Is(err, audit.ErrEntryNotFound) { ... }  // std errors, matches
	if status.Code(err) == codes.NotFound { ... }      // and so does the code

That the first works is not automatic — what crosses a connection is the error's
cockroachdb mark and not the sentinel's identity — and errors/grpc's decoding
interceptor is what makes it so.

# Why there is no idempotency interceptor

identity's client applies one because a retried write that mints a second account
is the failure it exists to prevent. There are no writes here: this service is
three reads, and a retried read is the same read.

# What a caller still owes

Transport security, as everywhere in this module — nothing here supplies it, and
a log of who did what to which resource is a poor thing to send in the clear.

A scope on the connection. The service reads whose log a request is against off
the connection rather than off the request, so whatever carries it — a host, a
piece of metadata, a token the consumer's interceptor resolves — is dialed in
here, by the caller, through WithDialOptions. A client that carries nothing gets
whatever the server's resolver makes of a request that names nobody.

# What is absent

There is no recording call, on this client or on the service. See audit.Recorder:
a recording belongs inside the transaction of the change it describes, and a
client is by definition somewhere else.
*/
package client

import (
	"github.com/primandproper/platform-go/v14/audit/auditpb"

	platformerrors "github.com/primandproper/primitives-go/errors"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial. It wraps
// errors.ErrEmptyInputParameter, so a caller may check either.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty audit service grpc target")

// Client is AuditServiceClient over a connection this package owns.
//
// It embeds the generated interface, so every RPC is a method on this type with
// the signature the schema gave it.
type Client struct {
	auditpb.AuditServiceClient

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
// credentials, a resolver, and whatever carries the scope this connection reads
// its log as.
//
// Nothing here supplies transport security: what a connection to your own
// service is secured with is yours, and a default of insecure.NewCredentials
// would be this package choosing plaintext on your behalf for an audit log.
// grpc.NewClient refuses a target with no credentials option, which is the right
// failure.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with no error-decoding
// interceptor, for a caller assembling their own chain.
//
// The cost of using it is the one this package's documentation opens with: an
// errors.Is against an audit sentinel then never matches.
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
		return nil, platformerrors.Wrapf(err, "dialing audit service at %q", target)
	}

	return &Client{AuditServiceClient: auditpb.NewAuditServiceClient(conn), conn: conn}, nil
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
	return &Client{AuditServiceClient: auditpb.NewAuditServiceClient(conn)}
}

// DefaultInterceptors is the dial option New applies: error decoding.
//
// It is exported so a caller assembling one connection for several services can
// install the same chain rather than approximating it. Note that it is not
// identity's client's chain, which also carries idempotency — see this package's
// documentation for why three reads need none.
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
