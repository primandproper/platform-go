package http

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"
	"github.com/primandproper/platform-go/v2/panicking"
	"github.com/primandproper/platform-go/v2/routing"

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
		httpServer:     provideStdLibHTTPServer(serverSettings.Port),
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
	if err := s.tracerProvider.ForceFlush(ctx); err != nil {
		s.logger.Error("flushing traces", err)
	}

	return s.httpServer.Shutdown(ctx)
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

	if s.config.SSLCertificateFile != "" && s.config.SSLCertificateKeyFile != "" {
		s.logger.WithValue("port", s.httpServer.Addr).Info("Listening for HTTPS requests")
		// returns ErrServerClosed on graceful close.
		if err := s.httpServer.ListenAndServeTLS(s.config.SSLCertificateFile, s.config.SSLCertificateKeyFile); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}

			s.logger.Error("shutting server down", err)
		}
	} else {
		s.logger.WithValue("port", s.httpServer.Addr).Info("Listening for HTTP requests")
		// returns ErrServerClosed on graceful close.
		if err := s.httpServer.ListenAndServe(); err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}

			s.logger.Error("shutting server down", err)
		}
	}
}

const (
	maxTimeout   = 120 * time.Second
	readTimeout  = 5 * time.Second
	writeTimeout = 2 * readTimeout
	idleTimeout  = maxTimeout
)

// provideStdLibHTTPServer provides an HTTP httpServer.
func provideStdLibHTTPServer(port uint16) *http.Server {
	// heavily inspired by https://blog.cloudflare.com/exposing-go-on-the-internet/
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
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
