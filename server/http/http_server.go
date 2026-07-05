package http

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"
	"github.com/primandproper/platform-go/v4/panicking"
	"github.com/primandproper/platform-go/v4/routing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/net/http2"
)

const (
	defaultLoggerName = "api_server"

	appleAppSiteAssociationPath = "/.well-known/apple-app-site-association"
)

// skipNoisePaths returns false for paths that should not be traced (health checks, apple site association, etc).
func skipNoisePaths(r *http.Request) bool {
	path := r.URL.Path
	if strings.HasPrefix(path, "/_ops_/") {
		return false
	}
	if path == appleAppSiteAssociationPath {
		return false
	}
	return true
}

type (
	Server interface {
		Serve()
		Shutdown(context.Context) error
		Router() routing.Router
	}

	// server is our API http server.
	server struct {
		logger         logging.Logger
		router         routing.Router
		panicker       panicking.Panicker
		httpServer     *http.Server
		tracerProvider tracing.TracerProvider
		config         Config
	}
)

// ProvideHTTPServer builds a new server instance.
// serviceName, when non-empty, is used for the server's logger; otherwise "api_server" is used.
func ProvideHTTPServer(
	serverSettings Config,
	logger logging.Logger,
	router routing.Router,
	tracerProvider tracing.TracerProvider,
	serviceName string,
) (Server, error) {
	loggerName := defaultLoggerName
	if serviceName != "" {
		loggerName = serviceName
	}
	srv := &server{
		config: serverSettings,

		// infra things,
		router:         router,
		logger:         logging.NewNamedLogger(logger, loggerName),
		panicker:       panicking.NewProductionPanicker(),
		httpServer:     provideStdLibHTTPServer(serverSettings),
		tracerProvider: tracing.EnsureTracerProvider(tracerProvider),
	}

	return srv, nil
}

// Router returns the router.
func (s *server) Router() routing.Router {
	return s.router
}

// Shutdown shuts down the server.
func (s *server) Shutdown(ctx context.Context) error {
	// Drain in-flight requests first, then flush — otherwise spans from requests
	// that complete during draining are lost because the flush already ran.
	err := s.httpServer.Shutdown(ctx)

	if flushErr := s.tracerProvider.ForceFlush(ctx); flushErr != nil {
		s.logger.Error("flushing traces", flushErr)
	}

	return err
}

// Serve serves HTTP traffic.
func (s *server) Serve() {
	s.logger.Debug("setting up server")

	s.httpServer.Handler = otelhttp.NewHandler(
		s.router.Handler(),
		"http_server",
		otelhttp.WithSpanNameFormatter(tracing.FormatSpan),
		otelhttp.WithFilter(skipNoisePaths),
	)

	http2ServerConf := &http2.Server{}
	if err := http2.ConfigureServer(s.httpServer, http2ServerConf); err != nil {
		s.logger.Error("configuring HTTP2", err)
		s.panicker.Panic(err)
	}

	// Bind the listener up front, bounded by StartupDeadline, so a slow or wedged
	// bind fails fast rather than hanging indefinitely.
	listener, err := s.listen()
	if err != nil {
		s.logger.Error("binding listener", err)
		s.panicker.Panic(err)
		return
	}

	if s.config.SSLCertificateFile != "" && s.config.SSLCertificateKeyFile != "" {
		s.logger.WithValue("port", s.httpServer.Addr).Info("Listening for HTTPS requests")
		// returns ErrServerClosed on graceful close.
		if err = s.httpServer.ServeTLS(listener, s.config.SSLCertificateFile, s.config.SSLCertificateKeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("serving HTTPS traffic", err)
			s.panicker.Panic(err)
		}
	} else {
		s.logger.WithValue("port", s.httpServer.Addr).Info("Listening for HTTP requests")
		// returns ErrServerClosed on graceful close.
		if err = s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("serving HTTP traffic", err)
			s.panicker.Panic(err)
		}
	}
}

// listen binds the TCP listener the server serves on. When StartupDeadline is
// configured it bounds the bind with that deadline, so binding cannot hang
// indefinitely during startup.
func (s *server) listen() (net.Listener, error) {
	ctx := context.Background()
	if s.config.StartupDeadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.config.StartupDeadline)
		defer cancel()
	}

	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp", s.httpServer.Addr)
}

const (
	// maxTimeout mirrors the router's request timeout (routing/chi maxTimeout). The server's
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
func provideStdLibHTTPServer(cfg Config) *http.Server {
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
