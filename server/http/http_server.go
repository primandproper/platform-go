package http

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	perrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"

	"golang.org/x/net/http2"
)

const defaultLoggerName = "api_server"

var _ Server = (*APIServer)(nil)

type (
	Server interface {
		// Serve binds the listener and serves until Shutdown is called or the
		// context is done. A graceful close reports no error.
		Serve(ctx context.Context) error
		// Shutdown drains in-flight requests, then flushes the spans they
		// produced. It does not shut the tracer provider down; see the method.
		Shutdown(ctx context.Context) error
		Router() *routing.Router
	}

	// APIServer is our API http server. It is exported, and returned by
	// NewHTTPServer, so a caller can depend on the server it built rather than on
	// the Server seam — matching server/grpc, whose NewGRPCServer has always
	// returned its own *Server.
	APIServer struct {
		logger         logging.Logger
		router         *routing.Router
		httpServer     *http.Server
		tracerProvider tracing.Provider
		config         *Config
	}
)

// NewHTTPServer builds a new server instance.
//
// serverSettings may be nil, which is treated as a zero-valued Config. The
// config is validated: a server that never binds because Port was unset should
// fail here rather than at the first request that does not arrive.
//
// The service name comes from WithServiceName, not a positional argument, so it
// sits with the other observability wiring — and matches the gRPC sibling.
//
// WithHealthRegistry and WithVersionEndpoint mount the operational routes on
// the router's backend, outside the OpenAPI document. Without them the router is
// left exactly as it was handed over.
func NewHTTPServer(
	ctx context.Context,
	serverSettings *Config,
	router *routing.Router,
	opts ...Option,
) (*APIServer, error) {
	if serverSettings == nil {
		serverSettings = &Config{}
	}

	if err := serverSettings.ValidateWithContext(ctx); err != nil {
		return nil, perrors.Wrap(err, "validating http server config")
	}

	o := newOptions(opts)

	loggerName := defaultLoggerName
	if o.serviceName != "" {
		loggerName = o.serviceName
	}
	srv := &APIServer{
		config: serverSettings,

		// infra things,
		router:         router,
		logger:         logging.NewNamedLogger(o.logger, loggerName),
		httpServer:     provideStdLibHTTPServer(serverSettings),
		tracerProvider: tracing.EnsureTracerProvider(o.tracerProvider),
	}

	if router != nil {
		// The operational routes are mounted before anything else this
		// constructor registers, so a service whose router is otherwise empty
		// still answers a probe.
		mountOperationalEndpoints(router.Backend(), o.healthRegistry, o.versionRoute, WithLogger(srv.logger))
	}

	if router != nil && serverSettings.AppleAppSiteAssociation.Enabled() {
		// Registered on the backend rather than through routing's typed registration:
		// the response shape is dictated by Apple, so it must not be enveloped, and it
		// isn't part of the service's API surface, so it doesn't belong in the OpenAPI
		// document either.
		router.Backend().Handle(
			http.MethodGet,
			AppleAppSiteAssociationPath,
			AppleAppSiteAssociationHandler(
				serverSettings.AppleAppSiteAssociation,
				WithLogger(srv.logger),
				WithTracerProvider(srv.tracerProvider),
			),
		)
	}

	return srv, nil
}

// Router returns the router.
func (s *APIServer) Router() *routing.Router {
	return s.router
}

// Shutdown drains in-flight requests and flushes the spans they produced.
//
// It flushes but does not shut the tracer provider down. The provider was
// handed to this server, not built by it, and shutting it down closes the
// exporter for everything else that shares it: the gRPC sibling that is still
// draining, and every background loop whose Close runs after ingress stops. A
// server that shut it down made itself the last thing in the process that could
// be traced, which is the opposite of what a shutdown wants — the shutdown is
// the part worth tracing. Whoever built the provider shuts it down;
// observability.Pillars.Shutdown is that owner, and service.Service runs it
// last.
func (s *APIServer) Shutdown(ctx context.Context) error {
	// Drain in-flight requests first, then flush — otherwise spans from requests
	// that complete during draining are lost because the flush already ran.
	err := s.httpServer.Shutdown(ctx)

	if flushErr := s.tracerProvider.ForceFlush(ctx); flushErr != nil {
		s.logger.Error("flushing traces", flushErr)
	}

	return err
}

// Serve serves HTTP traffic until Shutdown is called or ctx is done.
//
// It returns the failure rather than panicking through a hard-wired panicker:
// a library cannot decide that a bind failure should take the host process
// down, and a caller that wants that can still do it from the returned error.
// A graceful close reports nil.
func (s *APIServer) Serve(ctx context.Context) error {
	s.logger.Debug("setting up server")

	// The router is served as-is. Request tracing belongs to the routing backend,
	// which already installs it — otelchi for chi, otelhttp for the rest — and
	// wrapping the router here as well produced two nested server spans for every
	// request when both were handed the same provider, which is the default
	// wiring.
	//
	// The backend's is the one worth keeping: chi's reads the matched route
	// pattern, so its span names distinguish /users/{id} from /orders/{id} rather
	// than collapsing both onto the request method. The noise paths this wrapper
	// used to filter are filtered there too, by httpmw.IsUntraced.
	s.httpServer.Handler = s.router.Handler()

	http2ServerConf := &http2.Server{}
	if err := http2.ConfigureServer(s.httpServer, http2ServerConf); err != nil {
		return perrors.Wrap(err, "configuring HTTP2")
	}

	// Bind the listener up front, bounded by StartupDeadline, so a slow or wedged
	// bind fails fast rather than hanging indefinitely.
	listener, err := s.listen(ctx)
	if err != nil {
		return perrors.Wrap(err, "binding listener")
	}

	if s.config.SSLCertificateFile != "" && s.config.SSLCertificateKeyFile != "" {
		s.logger.WithValue("port", s.httpServer.Addr).Info("Listening for HTTPS requests")
		// returns ErrServerClosed on graceful close.
		if err = s.httpServer.ServeTLS(listener, s.config.SSLCertificateFile, s.config.SSLCertificateKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return perrors.Wrap(err, "serving HTTPS traffic")
		}

		return nil
	}

	s.logger.WithValue("port", s.httpServer.Addr).Info("Listening for HTTP requests")
	// returns ErrServerClosed on graceful close.
	if err = s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return perrors.Wrap(err, "serving HTTP traffic")
	}

	return nil
}

// listen binds the TCP listener the server serves on. When StartupDeadline is
// configured it bounds the bind with that deadline, so binding cannot hang
// indefinitely during startup.
func (s *APIServer) listen(ctx context.Context) (net.Listener, error) {
	if s.config.StartupDeadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.config.StartupDeadline)
		defer cancel()
	}

	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp", s.httpServer.Addr)
}

const (
	// maxTimeout mirrors the router's request timeout (routing/backends/chi maxTimeout). The server's
	// write timeout must exceed it, or a slow handler is killed mid-write before the router's
	// own timeout can ever fire.
	maxTimeout  = 120 * time.Second
	readTimeout = 5 * time.Second
	// writeTimeout must be larger than maxTimeout so the router's 120s request timeout is
	// actually reachable and slow responses are not severed mid-write.
	writeTimeout = maxTimeout + 30*time.Second
	idleTimeout  = maxTimeout
)

// provideStdLibHTTPServer provides an HTTP httpServer.
func provideStdLibHTTPServer(cfg *Config) *http.Server {
	readTO := cfg.ReadTimeout
	if readTO <= 0 {
		readTO = readTimeout
	}

	writeTO := cfg.WriteTimeout
	if writeTO <= 0 {
		writeTO = writeTimeout
	}

	idleTO := cfg.IdleTimeout
	if idleTO <= 0 {
		idleTO = idleTimeout
	}

	// heavily inspired by https://blog.cloudflare.com/exposing-go-on-the-internet/
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		ReadTimeout:  readTO,
		WriteTimeout: writeTO,
		IdleTimeout:  idleTO,
		TLSConfig: &tls.Config{
			// "Only use curves which have assembly implementations"
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
		},
	}

	return srv
}
