/*
Package client is a typed client for the settings gRPC service.

It is the generated stub plus the two interceptors a caller of this module's
services would otherwise wire by hand, and it is deliberately thin: every RPC
reaches it by embedding, so this file adds no method of its own beyond
construction and shutdown. A client that wrapped each RPC would be thirteen
functions that can drift from the schema, to gain nothing.

It is imported as settingsclient.

# The two interceptors, and why they are on by default

Error decoding, because without it every sentinel this service returns arrives
as a *status.Error that no errors.Is matches. The server encodes the sentinel
into the status details and the client has to decode it; that has always been a
function a caller could forget, and here it is a default instead.

Both idioms work on what comes back:

	if errors.Is(err, settings.ErrStrandedValues) { ... }  // std errors, matches
	if status.Code(err) == codes.FailedPrecondition { ... } // and so does the code

It matters here because three of this service's refusals share codes.NotFound: a
setting that does not exist, a value nobody has stored, and a resolution with
neither a value nor a default are three different things to tell somebody, and
the code says the same word about all three.

Idempotency, because a retried write on this service is answered as a mistake.
Only [settingspb.SettingsServiceClient.SetValue] converges — it writes the row
for the (subject, setting) pair rather than adding a second one — and the rest
of the writes are told apart by whether the work had already been done:
ClearValue on a value that is already cleared is settings.ErrValueNotFound,
ArchiveDefinition on a definition that is already archived is
settings.ErrDefinitionNotFound, and CreateDefinition run twice is
settings.ErrDefinitionNameTaken. A client retrying a reply it never received
therefore reads NotFound, or AlreadyExists, for work that succeeded. The
interceptor is what makes the second attempt answer what the first one did.

It stamps a key the caller put on the context and never mints one of its own —
a client that generated keys by itself would make every call idempotent-looking
and none of them idempotent, since a retry would carry a fresh key. Use
idempotency.WithNewKey to start one, once per logical operation. A call with no
key on its context is sent exactly as it would have been without the
interceptor, so nothing here is imposed on a caller who wants none of it.

UpdateDefinition is the one write whose replay would differ under its own
steam, and the difference is the honest one: a second identical edit is refused
only if somebody else's edit landed in between, which is exactly what the
caller wants to be told. A recorded reply keyed to the first attempt tells them
the same thing, because it is the first attempt's answer.

Both are defaults rather than obligations: WithoutDefaultInterceptors turns them
off for a caller assembling their own chain.
*/
package client

import (
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	idempotencygrpc "github.com/primandproper/primitives-go/v2/idempotency/grpc"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial. It wraps
// errors.ErrEmptyInputParameter, so a caller may check either.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter,
	"empty settings service grpc target")

// Client is SettingsServiceClient over a connection this package owns.
//
// It embeds the generated interface, so every RPC is a method on this type with
// the signature the schema gave it.
type Client struct {
	settingspb.SettingsServiceClient

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
// with no credentials option, which is the right failure, and
// insecure.NewCredentials is a decision to make deliberately and not one this
// package will make on your behalf.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with neither the error-decoding nor
// the idempotency interceptor, for a caller assembling their own chain.
//
// The cost of using it is the two this package's documentation opens with: an
// errors.Is against a settings sentinel then never matches and the codes alone
// do not tell three of the refusals apart, and a retried clear or archive is
// answered NotFound for work that already succeeded.
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
		return nil, platformerrors.Wrapf(err, "dialing settings service at %q", target)
	}

	return &Client{
		SettingsServiceClient: settingspb.NewSettingsServiceClient(conn),
		conn:                  conn,
	}, nil
}

// Wrap builds a client over a connection somebody else owns.
//
// It is what a consumer with one connection to a process serving several of
// this module's services uses, and what a test over bufconn uses. Close on the
// result closes nothing, because the connection is not this client's to close —
// which is the whole difference between this and New.
//
// The interceptors are not applied here and cannot be: they are dial options,
// and the connection has already been dialed. A caller wrapping their own
// connection passes [DefaultInterceptors] when they dial it, and this
// function's doc is the reminder.
func Wrap(conn grpc.ClientConnInterface) *Client {
	return &Client{SettingsServiceClient: settingspb.NewSettingsServiceClient(conn)}
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
