package http

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/encoding"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type mockTracerProvider struct {
	noop.TracerProvider
	forceFlushFunc  func(ctx context.Context) error
	shutdownFunc    func(ctx context.Context) error
	forceFlushCalls int
	shutdownCalls   int
}

func (m *mockTracerProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return noop.NewTracerProvider().Tracer(name, opts...)
}

func (m *mockTracerProvider) ForceFlush(ctx context.Context) error {
	m.forceFlushCalls++
	if m.forceFlushFunc == nil {
		return nil
	}
	return m.forceFlushFunc(ctx)
}

func (m *mockTracerProvider) Shutdown(ctx context.Context) error {
	m.shutdownCalls++
	if m.shutdownFunc == nil {
		return nil
	}
	return m.shutdownFunc(ctx)
}

// testRouter builds a real *routing.Router (chi-backed) for exercising Serve().
func testRouter(t *testing.T) *routing.Router {
	t.Helper()

	backend := chi.NewBackend(&chi.Config{ServiceName: t.Name()})
	enc := encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON)

	return routing.New(backend, enc)
}

func generateTestTLSCerts(t *testing.T) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"Test"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	must.NoError(t, err)

	dir := t.TempDir()

	certPath := filepath.Join(dir, "cert.pem")
	certOut, err := os.Create(certPath)
	must.NoError(t, err)
	must.NoError(t, pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	must.NoError(t, certOut.Close())

	keyDER, err := x509.MarshalECPrivateKey(key)
	must.NoError(t, err)
	keyPath := filepath.Join(dir, "key.pem")
	keyOut, err := os.Create(keyPath)
	must.NoError(t, err)
	must.NoError(t, pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	must.NoError(t, keyOut.Close())

	return certPath, keyPath
}

func TestNewHTTPServer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		x, err := NewHTTPServer(
			t.Context(),
			&Config{
				SSLCertificateFile:    "",
				SSLCertificateKeyFile: "",
				StartupDeadline:       0,
				Port:                  0,
			},
			nil,
		)

		test.NotNil(t, x)
		test.NoError(t, err)
	})

	T.Run("with nil settings", func(t *testing.T) {
		t.Parallel()

		x, err := NewHTTPServer(t.Context(), nil, nil)

		test.NotNil(t, x)
		test.NoError(t, err)
	})

	T.Run("with custom service name", func(t *testing.T) {
		t.Parallel()

		x, err := NewHTTPServer(
			t.Context(),
			&Config{Port: 8080},
			nil,
			WithServiceName("custom_service"),
		)

		test.NotNil(t, x)
		test.NoError(t, err)
	})

	T.Run("with empty service name uses default", func(t *testing.T) {
		t.Parallel()

		x, err := NewHTTPServer(
			t.Context(),
			&Config{Port: 8080},
			nil,
		)

		test.NotNil(t, x)
		test.NoError(t, err)
	})

	T.Run("with SSL config", func(t *testing.T) {
		t.Parallel()

		x, err := NewHTTPServer(
			t.Context(),
			&Config{
				SSLCertificateFile:    "/some/cert.pem",
				SSLCertificateKeyFile: "/some/key.pem",
				Port:                  8443,
			},
			nil,
		)

		test.NotNil(t, x)
		test.NoError(t, err)
	})
}

func TestServer_Router(T *testing.T) {
	T.Parallel()

	T.Run("returns the router", func(t *testing.T) {
		t.Parallel()

		s, err := NewHTTPServer(t.Context(), &Config{Port: 0}, nil)
		must.NoError(t, err)

		// Router returns nil when nil was passed in
		test.Nil(t, s.Router())
	})
}

func TestServer_Shutdown(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		s, err := NewHTTPServer(t.Context(), &Config{Port: 0}, nil)
		must.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		test.NoError(t, s.Shutdown(ctx))
	})

	T.Run("logs error when ForceFlush fails", func(t *testing.T) {
		t.Parallel()

		mtp := &mockTracerProvider{
			forceFlushFunc: func(_ context.Context) error { return errors.New("flush failed") },
		}

		s, err := NewHTTPServer(t.Context(), &Config{Port: 0}, nil, WithTracerProvider(mtp))
		must.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		test.NoError(t, s.Shutdown(ctx))

		test.EqOp(t, 1, mtp.forceFlushCalls)
	})

	T.Run("flushes the tracer provider without shutting it down", func(t *testing.T) {
		t.Parallel()

		// The server did not build the provider and does not own it. Shutting
		// it down here closes the exporter for the gRPC sibling still draining
		// and for every background loop whose Close runs after ingress stops —
		// which is precisely the part of a process's life worth tracing.
		mtp := &mockTracerProvider{}

		s, err := NewHTTPServer(t.Context(), &Config{Port: 0}, nil, WithTracerProvider(mtp))
		must.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		test.NoError(t, s.Shutdown(ctx))

		test.EqOp(t, 1, mtp.forceFlushCalls)
		test.EqOp(t, 0, mtp.shutdownCalls)
	})
}

func TestServer_Serve(T *testing.T) {
	T.Parallel()

	T.Run("serves HTTP and shuts down cleanly", func(t *testing.T) {
		t.Parallel()

		srv := &APIServer{
			logger:         loggingnoop.NewLogger(),
			router:         testRouter(t),
			httpServer:     provideStdLibHTTPServer(&Config{}),
			tracerProvider: tracingnoop.NewTracerProvider(),
			config:         &Config{},
		}

		done := make(chan struct{})
		go func() {
			_ = srv.Serve(t.Context())
			close(done)
		}()

		// Give the server time to start listening.
		time.Sleep(50 * time.Millisecond)
		must.NoError(t, srv.httpServer.Close())
		<-done
	})

	T.Run("serves HTTPS and shuts down cleanly", func(t *testing.T) {
		t.Parallel()

		certFile, keyFile := generateTestTLSCerts(t)

		srv := &APIServer{
			logger:         loggingnoop.NewLogger(),
			router:         testRouter(t),
			httpServer:     provideStdLibHTTPServer(&Config{}),
			tracerProvider: tracingnoop.NewTracerProvider(),
			config: &Config{
				SSLCertificateFile:    certFile,
				SSLCertificateKeyFile: keyFile,
			},
		}

		done := make(chan struct{})
		go func() {
			_ = srv.Serve(t.Context())
			close(done)
		}()

		time.Sleep(50 * time.Millisecond)
		must.NoError(t, srv.httpServer.Close())
		<-done
	})

	T.Run("reports an HTTPS failure with invalid cert files", func(t *testing.T) {
		t.Parallel()

		srv := &APIServer{
			logger:         loggingnoop.NewLogger(),
			router:         testRouter(t),
			httpServer:     provideStdLibHTTPServer(&Config{}),
			tracerProvider: tracingnoop.NewTracerProvider(),
			config: &Config{
				SSLCertificateFile:    "/nonexistent/cert.pem",
				SSLCertificateKeyFile: "/nonexistent/key.pem",
			},
		}

		// ServeTLS fails immediately with invalid cert paths; the failure must
		// reach the caller rather than leaving a listenerless zombie process.
		test.Error(t, srv.Serve(t.Context()))
	})

	T.Run("reports an HTTP listen failure", func(t *testing.T) {
		t.Parallel()

		// Occupy a port so ListenAndServe fails with "address already in use".
		lis, err := new(net.ListenConfig).Listen(t.Context(), "tcp", ":0")
		must.NoError(t, err)
		defer lis.Close()

		port := lis.Addr().(*net.TCPAddr).Port

		httpSrv := provideStdLibHTTPServer(&Config{Port: uint16(port)})

		srv := &APIServer{
			logger:         loggingnoop.NewLogger(),
			router:         testRouter(t),
			httpServer:     httpSrv,
			tracerProvider: tracingnoop.NewTracerProvider(),
			config:         &Config{},
		}

		test.Error(t, srv.Serve(t.Context()))
	})
}

func TestServer_listen(T *testing.T) {
	T.Parallel()

	T.Run("binds with StartupDeadline configured", func(t *testing.T) {
		t.Parallel()

		srv := &APIServer{
			logger:         loggingnoop.NewLogger(),
			router:         testRouter(t),
			httpServer:     provideStdLibHTTPServer(&Config{Port: 0}),
			tracerProvider: tracingnoop.NewTracerProvider(),
			config:         &Config{StartupDeadline: time.Second},
		}

		listener, err := srv.listen(t.Context())
		must.NoError(t, err)
		must.NotNil(t, listener)
		test.NoError(t, listener.Close())
	})

	T.Run("binds without StartupDeadline", func(t *testing.T) {
		t.Parallel()

		srv := &APIServer{
			logger:         loggingnoop.NewLogger(),
			router:         testRouter(t),
			httpServer:     provideStdLibHTTPServer(&Config{Port: 0}),
			tracerProvider: tracingnoop.NewTracerProvider(),
			config:         &Config{},
		}

		listener, err := srv.listen(t.Context())
		must.NoError(t, err)
		must.NotNil(t, listener)
		test.NoError(t, listener.Close())
	})
}

func Test_provideStdLibHTTPServer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		srv := provideStdLibHTTPServer(&Config{Port: 8080})

		test.NotNil(t, srv)
		test.EqOp(t, ":8080", srv.Addr)
		test.EqOp(t, readTimeout, srv.ReadTimeout)
		test.EqOp(t, writeTimeout, srv.WriteTimeout)
		test.EqOp(t, idleTimeout, srv.IdleTimeout)
		test.NotNil(t, srv.TLSConfig)
		test.EqOp(t, uint16(tls.VersionTLS12), srv.TLSConfig.MinVersion)
	})

	T.Run("write timeout exceeds the router request timeout so it is reachable", func(t *testing.T) {
		t.Parallel()

		srv := provideStdLibHTTPServer(&Config{})

		test.Greater(t, maxTimeout, srv.WriteTimeout)
	})

	T.Run("honors configured timeouts", func(t *testing.T) {
		t.Parallel()

		srv := provideStdLibHTTPServer(&Config{
			ReadTimeout:  7 * time.Second,
			WriteTimeout: 200 * time.Second,
			IdleTimeout:  90 * time.Second,
		})

		test.EqOp(t, 7*time.Second, srv.ReadTimeout)
		test.EqOp(t, 200*time.Second, srv.WriteTimeout)
		test.EqOp(t, 90*time.Second, srv.IdleTimeout)
	})

	T.Run("with zero port", func(t *testing.T) {
		t.Parallel()

		srv := provideStdLibHTTPServer(&Config{})

		test.NotNil(t, srv)
		test.EqOp(t, ":0", srv.Addr)
	})
}
