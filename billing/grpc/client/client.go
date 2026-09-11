/*
Package client is a typed client for the billing gRPC service.

It is the generated stub plus the interceptor a caller of this module's services
would otherwise wire by hand, and it is deliberately thin: every RPC reaches it
by embedding, so this file adds no method of its own beyond construction and
shutdown. A client that wrapped each RPC would be eighteen functions that can
drift from the schema, to gain nothing.

It is imported as billingclient.

# The interceptor, and why it is on by default

Error decoding, because without it every sentinel this service returns arrives
as a *status.Error that no errors.Is matches. The server encodes the sentinel
into the status details and the client has to decode it; that has always been a
function a caller could forget, and here it is a default instead.

Both idioms work on what comes back:

	if errors.Is(err, billing.ErrProductNotFound) { ... }  // std errors, matches
	if status.Code(err) == codes.NotFound { ... }          // and so does the code

It matters more here than on the other surfaces. Seven of billing's refusals
share codes.InvalidArgument, five share codes.AlreadyExists and two share
codes.FailedPrecondition, and inside each family the remedies differ — fix a
field, fix the code that chose an id, or do nothing because the work is already
done. The code alone frequently does not say which.

# Why there is no idempotency interceptor

identity's client applies one and this does not, and the difference is which
calls are here. An idempotency store keeps a response so it can be replayed, and
its value is on a write a caller must not perform twice. Twelve of these
eighteen RPCs are reads, and the writes that would have wanted it — the ones a
redelivered payment event makes — are deliberately not on this surface at all;
the store's own unique indexes are what make those safe, in the statement rather
than in a cache in front of it.

The four administrative writes that are here are a console's, made by a person
watching the result. A retried ArchiveProduct archives a product that is already
archived, which the store answers the same way twice.
*/
package client

import (
	"github.com/primandproper/platform-go/v14/billing/billingpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial. It wraps
// errors.ErrEmptyInputParameter, so a caller may check either.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter,
	"empty billing service grpc target")

// Client is BillingServiceClient over a connection this package owns.
//
// It embeds the generated interface, so every RPC is a method on this type with
// the signature the schema gave it.
type Client struct {
	billingpb.BillingServiceClient

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
// behalf — on this service least of all, whose pages are what an account has
// paid.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with no error-decoding
// interceptor, for a caller assembling their own chain.
//
// The cost of using it is the one this package's documentation opens with: an
// errors.Is against a billing sentinel then never matches, and the codes alone
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
		return nil, platformerrors.Wrapf(err, "dialing billing service at %q", target)
	}

	return &Client{
		BillingServiceClient: billingpb.NewBillingServiceClient(conn),
		conn:                 conn,
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
	return &Client{BillingServiceClient: billingpb.NewBillingServiceClient(conn)}
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
