/*
Package client is a typed client for the sign-in gRPC service.

It is the generated stub plus the interceptors a caller of this module's
services would otherwise wire by hand, and it is deliberately thin: every RPC reaches it
by embedding, so this file adds no method of its own beyond construction and
shutdown. A client that wrapped each RPC would be eleven functions that can
drift from the schema, to gain nothing.

It is imported as signinclient.

# The error-decoding interceptor, and why it is on by default

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

# The idempotency interceptor, and why it is one RPC wide

identity's client stamps the caller's key on every call it makes. This one
stamps it on ExchangeRefreshToken and on nothing else, and both halves of that
are decisions.

The narrow half is the one that would read as an oversight, so it goes first. A
refresh exchange is the single call in this API a client cannot safely retry on
its own: the token is single-use with reuse detection, so an attempt whose
answer never arrived leaves no successor to retry with, and re-sending the token
the client still holds is indistinguishable from a replay and ends the login.
[github.com/primandproper/platform-go/v14/authentication/signin.IdempotentRefreshTokenStore]
is the answer to that, and it is reachable only if the key the caller minted
actually leaves this process. Stamping it here is what makes the fix arrive by
using this client, rather than by a consumer learning a metadata name and wiring
an interceptor this package declined to apply. Use idempotency.WithNewKey to
start a key, once per logical exchange and outside the retry loop.

The wide half is what is still refused, and the reason has not changed. An
idempotency Manager keeps a response so it can be replayed; the response to a
sign-in is a live token, and to an enrollment a live second-factor secret.
Neither belongs in a store whose purpose is to hand the same bytes back a second
time — which is why the sign-in service reads the key itself, inside the
transaction that spends the token, instead of being wrapped in that Manager. A
key stamped on the rest of these RPCs would be a key nothing on the far side
reads, and a live credential waiting in the store of any consumer who wrapped
this service in a Manager of their own. The filter is what keeps that from being
a deployment's mistake to make.

The other writes here are safe to retry anyway, so none of them lose anything to
the filter. Signing in twice mints a second token rather than a second account,
and setting the same password twice is the state the first attempt was aiming at
— which is not true of the RPCs identity's interceptor exists for. The two
sign-outs are the plainest case: every refusal a presented token can draw is
answered as a success there, so a retry cannot arrive at a different outcome than
the attempt it repeats. A retried
registration is the one that would write twice, and the directory refuses it:
the username and the address are unique within a scope, so the second attempt is
a collision rather than a duplicate account.

A consumer who does run a Manager over this service excludes
ExchangeRefreshToken from it, with idempotencygrpc.WithMethodFilter. That call
now arrives carrying a key, and a Manager that recorded its reply would be
holding in a store the refresh token this whole mechanism exists to keep out of
one.
*/
package client

import (
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	idempotencygrpc "github.com/primandproper/primitives-go/v2/idempotency/grpc"

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

// WithoutDefaultInterceptors builds a client with neither the error-decoding nor
// the idempotency interceptor, for a caller assembling their own chain.
//
// It costs two things. The first is the one this package's documentation opens
// with: an errors.Is against a sign-in sentinel then never matches, and the
// codes alone do not tell four refusals apart. The second is quieter and is why
// this option is worth reading twice — a key on the context stops reaching
// ExchangeRefreshToken, so a retry of a lost exchange goes back to being
// indistinguishable from a replay and ends the login.
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
// The interceptors are not applied here and cannot be: they are dial options,
// and the connection has already been dialed. A caller wrapping their own
// connection passes [DefaultInterceptors] when they dial it, and this function's
// doc is the reminder — a wrapped connection that skips them decodes no sentinel
// and stamps no idempotency key.
func Wrap(conn grpc.ClientConnInterface) *Client {
	return &Client{SignInServiceClient: signinpb.NewSignInServiceClient(conn)}
}

// DefaultInterceptors is the dial option New applies: error decoding, then
// idempotency on the one RPC that reads a key.
//
// It is exported so a caller assembling one connection for several services can
// install the same chain rather than approximating it. Note that it is not
// identity's client's chain, whose idempotency interceptor carries no filter —
// see this package's documentation for what a key stamped on the rest of these
// RPCs would be waiting in.
func DefaultInterceptors() grpc.DialOption { return defaultInterceptors() }

func defaultInterceptors() grpc.DialOption {
	return grpc.WithChainUnaryInterceptor(
		grpcerrors.UnaryErrorDecodingInterceptor(),
		idempotencygrpc.NewUnaryClientInterceptor(
			idempotencygrpc.WithClientMethodFilter(stampsIdempotencyKey),
		),
	)
}

// stampsIdempotencyKey names the RPCs a key leaves this client on, which is the
// one RPC on the far side that reads one.
//
// It compares against the generated constant rather than the method's spelling,
// so a rename in the schema is a build failure here rather than a filter that
// silently stops matching — which would take the idempotent exchange with it and
// leave every other test passing.
func stampsIdempotencyKey(fullMethod string) bool {
	return fullMethod == signinpb.SignInService_ExchangeRefreshToken_FullMethodName
}

// Close closes the connection, if this client owns one.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}

	return c.conn.Close()
}
