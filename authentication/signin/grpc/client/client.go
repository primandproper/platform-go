/*
Package client is a typed client for the sign-in gRPC service.

It is the generated stub plus the interceptor a caller of this module's services
would otherwise wire by hand, and it is deliberately thin: every RPC reaches it
by embedding, so this file adds no method of its own beyond construction and
shutdown. A client that wrapped each RPC would be eleven functions that can
drift from the schema, to gain nothing.

It is imported as signinclient.

# The interceptor, and why it is on by default

Error decoding, because without it every sentinel the sign-in service returns
arrives as a *status.Error that no errors.Is matches. The server encodes the
sentinel into the status details and the client has to decode it; that has
always been a function a caller could forget, and here it is a default instead.

Both idioms work on what comes back:

	if errors.Is(err, signin.ErrSecondFactorRequired) { ... }  // std errors, matches
	if status.Code(err) == codes.Unauthenticated { ... }       // and so does the code

That the first works is not automatic — what crosses a connection is the error's
cockroachdb mark and not the sentinel's identity — and errors/grpc's decoding
interceptor is what makes it so. It matters more here than on most services,
because four of this one's refusals share PermissionDenied and three share
FailedPrecondition, so the code alone frequently does not say what to do next.

# A third idiom, for the clients that are not this one

A Go caller has the sentinel, which is the best of the three and is why this
client exists. A caller in another language has neither the sentinel nor
cockroachdb/errors, and until recently had only the status message — so telling
a second-factor prompt from a wrong password meant comparing the English string
"a second-factor code is required".

Those callers read the reason instead:

	if info, ok := grpcerrors.ClientReasonFromStatus(err); ok &&
		info.GetReason() == "SECOND_FACTOR_REQUIRED" { ... }

It is a google.rpc.ErrorInfo detail, at the standard type URL, so a TypeScript
or Swift client reads the same field from its own generated types with nothing
from this module. signin.ClientSafeReasons is the whole set, and the names there
are chosen once and never reworded, which the messages explicitly are not.

Nothing about that is Go-specific or needs this client — but it is the answer to
the question this section is about, so it is written down where somebody
comparing the idioms will find it. The reason survives an edge that strips the
encoded details, which the sentinel does not; see
errors/grpc.StripEncodedErrorDetail.

# Why there is no idempotency interceptor

identity's client applies one and this does not, and the difference is what a
recorded reply would hold. An idempotency store keeps a response so it can be
replayed; the response to a sign-in is a live token, and to an enrollment a live
second-factor secret. Neither belongs in a store whose purpose is to hand the
same bytes back a second time.

The writes here are safe to retry anyway. Signing in twice mints a second token
rather than a second account, and setting the same password twice is the state
the first attempt was aiming at — which is not true of the RPCs identity's
interceptor exists for. A retried registration is the one that would write twice,
and the directory refuses it: the username and the address are unique within a
scope, so the second attempt is a collision rather than a duplicate account.
*/
package client

import (
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial. It wraps
// errors.ErrEmptyInputParameter, so a caller may check either.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty sign-in service grpc target")

// Client is SignInServiceClient over a connection this package owns.
//
// It embeds the generated interface, so every RPC is a method on this type with
// the signature the schema gave it.
type Client struct {
	signinpb.SignInServiceClient

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
// Nothing here supplies transport security, and this is the one client in the
// module where that is not merely a default worth stating: two of these RPCs
// carry a plaintext password and one answers with a live second-factor secret.
// grpc.NewClient refuses a target with no credentials option, which is the right
// failure, and insecure.NewCredentials is a decision to make deliberately and
// not one this package will make on your behalf.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with no error-decoding
// interceptor, for a caller assembling their own chain.
//
// The cost of using it is the one this package's documentation opens with: an
// errors.Is against a sign-in sentinel then never matches, and the codes alone
// do not tell four refusals apart.
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
		return nil, platformerrors.Wrapf(err, "dialing sign-in service at %q", target)
	}

	return &Client{SignInServiceClient: signinpb.NewSignInServiceClient(conn), conn: conn}, nil
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
	return &Client{SignInServiceClient: signinpb.NewSignInServiceClient(conn)}
}

// DefaultInterceptors is the dial option New applies: error decoding.
//
// It is exported so a caller assembling one connection for several services can
// install the same chain rather than approximating it. Note that it is not
// identity's client's chain, which also carries idempotency — see this package's
// documentation for why a sign-in response must not be replayable.
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
