/*
Package client is a typed client for the media registry gRPC service.

It is the generated stub plus the interceptors a caller of this module's
services would otherwise wire by hand, and it is deliberately thin: every RPC
reaches it by embedding.

It is imported as mediaregistryclient.

# The interceptors, and why there are two

Error decoding, because without it every sentinel this service returns arrives
as a *status.Error that no errors.Is matches. This service has a streaming
method, UploadObject, and the unary decoder does not see a stream's errors, so
the client installs a stream decoder beside it: an upload refused for its size
answers errors.Is(err, mediaregistrygrpc.ErrObjectTooLarge) the way a refused
read answers errors.Is(err, mediaregistry.ErrObjectNotFound).
*/
package client

import (
	"context"

	"github.com/primandproper/platform-go/v15/mediaregistry/mediaregistrypb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc"
)

// ErrEmptyTarget indicates New was given no address to dial.
var ErrEmptyTarget = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter,
	"empty media registry service grpc target")

// Client is MediaRegistryServiceClient over a connection this package owns.
type Client struct {
	mediaregistrypb.MediaRegistryServiceClient

	conn *grpc.ClientConn
}

type options struct {
	dialOptions      []grpc.DialOption
	skipInterceptors bool
}

// Option configures a Client.
type Option func(*options)

// WithDialOptions adds gRPC dial options — transport credentials, per-RPC
// credentials, a resolver. Nothing here supplies transport security.
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, opts...) }
}

// WithoutDefaultInterceptors builds a client with neither error decoder, for a
// caller assembling their own chain.
func WithoutDefaultInterceptors() Option {
	return func(o *options) { o.skipInterceptors = true }
}

// New dials target and returns a client over it. The connection is this
// client's, and Close closes it.
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
		dialOptions = append(dialOptions, defaultInterceptors()...)
	}

	conn, err := grpc.NewClient(target, dialOptions...)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "dialing media registry service at %q", target)
	}

	return &Client{
		MediaRegistryServiceClient: mediaregistrypb.NewMediaRegistryServiceClient(conn),
		conn:                       conn,
	}, nil
}

// Wrap builds a client over a connection somebody else owns. Close on the
// result closes nothing, and the interceptors are not applied: they are dial
// options, and the connection has already been dialed. A caller wrapping their
// own connection installs DefaultInterceptors on it themselves.
func Wrap(conn grpc.ClientConnInterface) *Client {
	return &Client{MediaRegistryServiceClient: mediaregistrypb.NewMediaRegistryServiceClient(conn)}
}

// DefaultInterceptors are the dial options New applies: error decoding, for
// unary calls and for streams.
func DefaultInterceptors() []grpc.DialOption { return defaultInterceptors() }

func defaultInterceptors() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(grpcerrors.UnaryErrorDecodingInterceptor()),
		grpc.WithChainStreamInterceptor(StreamErrorDecodingInterceptor()),
	}
}

// StreamErrorDecodingInterceptor decodes the sentinel chain out of a stream's
// status, as grpcerrors.UnaryErrorDecodingInterceptor does for a unary call —
// and by way of it, so what comes back is the same error that one returns: it
// matches errors.Is against the sentinel and still reports its status code.
//
// primitives-go ships the unary half only, because until this service no
// surface streamed from the client. Rather than a second copy of how a decoded
// error answers both idioms, each stream error is handed to the unary
// decoder's own logic as though a unary call had returned it.
func StreamErrorDecodingInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			return nil, decode(ctx, err)
		}

		return &decodingStream{ClientStream: stream}, nil
	}
}

// unaryDecoder is the unary decoding interceptor, held to decode a stream's
// errors with.
var unaryDecoder = grpcerrors.UnaryErrorDecodingInterceptor()

// decode runs err through the unary decoder, standing in for the call that
// returned it.
func decode(ctx context.Context, err error) error {
	return unaryDecoder(ctx, "", nil, nil, nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return err })
}

// decodingStream decodes the errors its stream reports on receipt. SendMsg's
// are left alone: a send fails with io.EOF once the server has answered, and
// the answer is what RecvMsg then reports.
type decodingStream struct {
	grpc.ClientStream
}

func (s *decodingStream) RecvMsg(m any) error {
	if err := s.ClientStream.RecvMsg(m); err != nil {
		return decode(s.Context(), err)
	}

	return nil
}

// Close closes the connection, if this client owns one.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}

	return c.conn.Close()
}
