package grpc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	perrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

const (
	defaultServiceName = "grpc_server"

	// DefaultMaxMessageSize bounds a single message in either direction, in
	// bytes. It is grpc-go's receive default applied to send as well.
	//
	// grpc-go's own pair is asymmetric: receive is bounded at 4 MiB and send at
	// math.MaxInt32, which is no bound at all. A server on those defaults will
	// marshal and send a response that no default-configured client can read,
	// and the ResourceExhausted lands on the caller — in a process the service
	// owner may not operate, with nothing in the server's logs or traces to say
	// a response was ever too large. Bounding send at the same 4 MiB moves that
	// failure to the handler that produced the oversized message.
	DefaultMaxMessageSize = 4 << 20

	// UnboundedMessageSize is the largest bound gRPC can be given, and the value
	// to name to opt out of bounding a direction at all. It is what grpc-go uses
	// for its own send default.
	UnboundedMessageSize = math.MaxInt32
)

type (
	Config struct {
		TLSCertificateFile    string `env:"TLS_CERTIFICATE_FILEPATH"     json:"tlsCertificate,omitempty"    yaml:"tlsCertificate,omitempty"`
		TLSCertificateKeyFile string `env:"TLS_CERTIFICATE_KEY_FILEPATH" json:"tlsCertificateKey,omitempty" yaml:"tlsCertificateKey,omitempty"`

		// MaxReceiveMessageSize bounds a single received message, in bytes.
		// Zero takes DefaultMaxMessageSize; UnboundedMessageSize removes the
		// bound. It is a deployment-time number because it depends on the
		// payloads a service actually carries, not on its code.
		MaxReceiveMessageSize int `env:"MAX_RECEIVE_MESSAGE_SIZE" json:"maxReceiveMessageSize,omitempty" yaml:"maxReceiveMessageSize,omitempty"`

		// MaxSendMessageSize bounds a single sent message, in bytes, on the same
		// terms. A denormalized read model is the usual reason to raise it: a
		// full page of embedded records is larger than a client's own 4 MiB
		// receive default long before anything looks wrong on the server.
		MaxSendMessageSize int `env:"MAX_SEND_MESSAGE_SIZE" json:"maxSendMessageSize,omitempty" yaml:"maxSendMessageSize,omitempty"`

		Port uint16 `env:"PORT" json:"port,omitempty" yaml:"port,omitempty"`
	}

	Server struct {
		logger         logging.Logger
		config         *Config
		grpcServer     *grpc.Server
		tracerProvider tracing.Provider
	}

	// RegistrationFunc is i.e. protobuf.RegisterSomeExampleServiceServer(grpcServer, &exampleServiceServerImpl{}).
	RegistrationFunc func(*grpc.Server)
)

// NewGRPCServer builds a gRPC server.
//
// It takes a context and validates the config, matching NewHTTPServer. The
// Config has had a ValidateWithContext for as long as it has had a TLS pair, and
// nothing called it — so naming a certificate without its key, which this
// constructor reads as "TLS was not configured", started a plaintext server that
// looked from its config like a TLS one.
func NewGRPCServer(
	ctx context.Context,
	cfg *Config,
	unaryServerInterceptors []grpc.UnaryServerInterceptor,
	streamServerInterceptors []grpc.StreamServerInterceptor,
	registrationFunctions []RegistrationFunc,
	opts ...Option,
) (*Server, error) {
	if cfg == nil {
		return nil, perrors.ErrNilInputParameter
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, perrors.Wrap(err, "validating gRPC server config")
	}

	o := newOptions(opts)
	logger := o.logger

	maxReceive, err := resolveMessageSize("receive", cfg.MaxReceiveMessageSize, o.maxReceiveMessageSize)
	if err != nil {
		return nil, err
	}

	maxSend, err := resolveMessageSize("send", cfg.MaxSendMessageSize, o.maxSendMessageSize)
	if err != nil {
		return nil, err
	}

	tp := tracing.EnsureTracerProvider(o.tracerProvider)
	serverOpts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler(otelgrpc.WithTracerProvider(tp))),
		grpc.ChainUnaryInterceptor(append([]grpc.UnaryServerInterceptor{LoggingInterceptor(logger)}, unaryServerInterceptors...)...),
		grpc.ChainStreamInterceptor(append([]grpc.StreamServerInterceptor{StreamLoggingInterceptor(logger)}, streamServerInterceptors...)...),
		grpc.MaxRecvMsgSize(maxReceive),
		grpc.MaxSendMsgSize(maxSend),
	}

	if cfg.TLSCertificateKeyFile != "" && cfg.TLSCertificateFile != "" {
		serverCert, certErr := tls.LoadX509KeyPair(cfg.TLSCertificateFile, cfg.TLSCertificateKeyFile)
		if certErr != nil {
			return nil, certErr
		}

		config := &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientAuth:   tls.NoClientCert,
			CurvePreferences: []tls.CurveID{
				tls.CurveP256,
				tls.X25519,
			},
			MinVersion: tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			},
		}

		serverOpts = append(serverOpts, grpc.Creds(credentials.NewTLS(config)))
	}

	grpcServer := grpc.NewServer(serverOpts...)
	for _, rf := range registrationFunctions {
		rf(grpcServer)
	}

	if o.healthRegistry != nil {
		grpc_health_v1.RegisterHealthServer(grpcServer, NewHealthService(o.healthRegistry))
	}

	if o.reflection {
		reflection.Register(grpcServer)
	}

	name := defaultServiceName
	if o.serviceName != "" {
		name = o.serviceName
	}

	return &Server{
		logger:         logging.NewNamedLogger(logger, name),
		config:         cfg,
		grpcServer:     grpcServer,
		tracerProvider: tp,
	}, nil
}

// resolveMessageSize settles one direction's message-size bound: the Option if
// one was named, else the Config field, else DefaultMaxMessageSize. The Option
// wins because caller options are applied last everywhere else in this module.
//
// Zero from either source means "not configured" rather than "reject
// everything", since a server bounded at zero bytes cannot answer a single call
// and nobody asks for that on purpose. A bound that gRPC cannot express — below
// zero, or past UnboundedMessageSize, which is the ceiling the wire's length
// prefix imposes — is refused here rather than silently clamped, because a
// server running under a bound nobody asked for is the failure this whole
// setting exists to make visible.
func resolveMessageSize(direction string, configured, override int) (int, error) {
	size := configured
	if override != 0 {
		size = override
	}

	if size == 0 {
		return DefaultMaxMessageSize, nil
	}

	if size < 0 || size > UnboundedMessageSize {
		return 0, perrors.Errorf("max %s message size %d is outside [0, %d]", direction, size, UnboundedMessageSize)
	}

	return size, nil
}

// Shutdown stops the server gracefully, then flushes the spans its RPCs
// produced — the same order as the HTTP sibling, and for the same reason: spans
// from RPCs that complete during draining are lost if the flush runs first.
//
// Like the HTTP sibling it flushes the tracer provider without shutting it
// down. The provider is shared with whatever else the process is still taking
// down, and closing an exporter one server happens to be finished with would
// blind all of it. Its owner shuts it down last.
//
// In-flight RPCs are given until ctx is done to finish. If ctx expires first the
// server is stopped hard and the context's error is returned, so a caller can
// tell a clean drain from a forced one.
func (s *Server) Shutdown(ctx context.Context) error {
	stopped := make(chan struct{})
	go func() {
		s.grpcServer.GracefulStop()
		close(stopped)
	}()

	var err error
	select {
	case <-stopped:
	case <-ctx.Done():
		// GracefulStop is still blocked on in-flight RPCs; Stop unblocks it by
		// cancelling them.
		s.grpcServer.Stop()
		<-stopped
		err = ctx.Err()
	}

	if flushErr := s.tracerProvider.ForceFlush(ctx); flushErr != nil {
		s.logger.Error("flushing traces", flushErr)
	}

	return err
}

// Serve serves gRPC traffic until Shutdown is called or ctx is done.
//
// A graceful stop reports nil; every other failure is returned. It used to
// return nothing, and the only sentinel it checked was net/http's
// ErrServerClosed — which gRPC never returns — so a bind failure or a dead
// server was completely silent.
func (s *Server) Serve(ctx context.Context) error {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", s.config.Port))
	if err != nil {
		return perrors.Wrap(err, "binding gRPC listener")
	}

	s.logger.WithValue("port", s.config.Port).Info("Listening for GRPC requests")

	// grpc.ErrServerStopped is what Stop and GracefulStop produce, and is the
	// only "this is a normal shutdown" answer this server can get.
	if err = s.grpcServer.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return perrors.Wrap(err, "serving gRPC traffic")
	}

	return nil
}

// healthProbeMethodPrefix is the gRPC health service every load balancer and
// Kubernetes probe calls on a timer. The HTTP sibling filters its probe paths
// out of both spans and logs; this filters the equivalent here, which otherwise
// made up the overwhelming majority of the lines these interceptors emitted.
const healthProbeMethodPrefix = "/grpc.health.v1.Health/"

// logRPC emits one line for a completed RPC.
//
// A failed RPC used to be reported as "error": 1 — no error, no code, no
// message — so the log said an RPC had failed and nothing whatsoever about how.
// The status code is the part an operator acts on: InvalidArgument is the
// caller's problem and Internal is this service's.
func logRPC(l logging.Logger, kind, fullMethod string, elapsed time.Duration, err error) {
	if strings.HasPrefix(fullMethod, healthProbeMethodPrefix) {
		return
	}

	values := map[string]any{
		"rpc.method":  fullMethod,
		"rpc.kind":    kind,
		"rpc.code":    status.Code(err).String(),
		"elapsed":     elapsed,
		"elapsed_ms":  elapsed.Milliseconds(),
		"rpc.errored": err != nil,
	}

	if err == nil {
		l.WithValues(values).Info("rpc invoked")
		return
	}

	if s, ok := status.FromError(err); ok {
		values["rpc.message"] = s.Message()
	}

	l.WithValues(values).Error("rpc invoked", err)
}

// LoggingInterceptor logs every completed unary RPC.
func LoggingInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	l := logging.EnsureLogger(logger)

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		result, err := handler(ctx, req)

		logRPC(l, "unary", info.FullMethod, time.Since(start), err)

		return result, err
	}
}

// StreamLoggingInterceptor logs every completed streaming RPC.
//
// The unary side has been logged since this package existed and the stream side
// was not logged at all, so a service whose API is mostly streams had no record
// that any of it had been called.
func StreamLoggingInterceptor(logger logging.Logger) grpc.StreamServerInterceptor {
	l := logging.EnsureLogger(logger)

	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)

		logRPC(l, "stream", info.FullMethod, time.Since(start), err)

		return err
	}
}
