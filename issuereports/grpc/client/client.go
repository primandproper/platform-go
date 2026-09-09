/*
Package client is a typed client for the issue reports gRPC service.

It is the generated stub plus the interceptor a caller of this module's services
would otherwise wire by hand, and it is deliberately thin: every RPC reaches it
by embedding, so this file adds no method of its own beyond construction and
shutdown. A client that wrapped each RPC would be ten functions that can drift
from the schema, to gain nothing.

It is imported as issuereportsclient.

# The interceptor, and why it is on by default

Error decoding, because without it every sentinel this service returns arrives as
a *status.Error that no errors.Is matches. The server encodes the sentinel into
the status details and the client has to decode it; that has always been a
function a caller could forget, and here it is a default instead.

Both idioms work on what comes back:

	if errors.Is(err, issuereports.ErrStatusConflict) { ... }  // std errors, matches
	if status.Code(err) == codes.Aborted { ... }               // and so does the code

It matters most on the one call this surface exists for. A triage console's move
is refused in three different ways — the report moved, the move is not one the
lifecycle admits, the status does not exist — and only the first is worth
retrying after a re-read. Three of the ten RPCs can answer codes.InvalidArgument
for two different reasons apiece, and the code alone does not say which.

# Why there is no idempotency interceptor

identity's client applies one and this does not, and the difference is what the
writes here are. An idempotency store keeps a response so it can be replayed, and
its value is on a write a caller must not perform twice.

Filing is the write that would want one, and a retried CreateReport does file a
second report — which is a duplicate a triage queue sees and declines, not a
charge taken twice. The other three writes are already answer-the-same-way-twice:
the lifecycle move is a compare-and-set, so the retry that lost is told the report
already moved rather than moving it again; a revision assigns what it was given;
and archiving something already archived is the state the caller asked for.
A consumer who wants the filing deduplicated composes
idempotency's interceptor with [DefaultInterceptors].
*/
package client

import (
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	platformerrors "github.com/primandproper/primitives-go/errors"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial. It wraps
// errors.ErrEmptyInputParameter, so a caller may check either.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter,
	"empty issue reports service grpc target")

// Client is IssueReportsServiceClient over a connection this package owns.
//
// It embeds the generated interface, so every RPC is a method on this type with
// the signature the schema gave it.
type Client struct {
	issuereportspb.IssueReportsServiceClient

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
// Nothing here supplies transport security. grpc.NewClient refuses a target with
// no credentials option, which is the right failure, and insecure.NewCredentials
// is a decision to make deliberately and not one this package will make on your
// behalf — on this service least of all, whose rows are what your users said
// about things that happened to them.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with no error-decoding
// interceptor, for a caller assembling their own chain.
//
// The cost of using it is the one this package's documentation opens with: an
// errors.Is against an issuereports sentinel then never matches, and a lost
// compare-and-set is indistinguishable from the two refusals a retry cannot fix.
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
		return nil, platformerrors.Wrapf(err, "dialing issue reports service at %q", target)
	}

	return &Client{
		IssueReportsServiceClient: issuereportspb.NewIssueReportsServiceClient(conn),
		conn:                      conn,
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
	return &Client{IssueReportsServiceClient: issuereportspb.NewIssueReportsServiceClient(conn)}
}

// DefaultInterceptors is the dial option New applies: error decoding.
//
// It is exported so a caller assembling one connection for several services can
// install the same chain rather than approximating it. Note that it is not
// identity's client's chain, which also carries idempotency — see this package's
// documentation for why this surface does not want one.
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
