package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
)

var errStub = errors.New("stub error")

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

func TestNewGRPCServer(T *testing.T) {
	T.Parallel()

	T.Run("returns error with nil config", func(t *testing.T) {
		t.Parallel()

		server, err := NewGRPCServer(t.Context(), nil, nil, nil, nil)

		test.Nil(t, server)
		test.Error(t, err)
	})

	T.Run("succeeds with valid config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Port: 0}
		server, err := NewGRPCServer(t.Context(), cfg, nil, nil, nil)

		must.NoError(t, err)
		test.NotNil(t, server)
	})

	T.Run("succeeds with registration functions", func(t *testing.T) {
		t.Parallel()

		called := false
		rf := func(s *grpc.Server) {
			called = true
		}

		cfg := &Config{Port: 0}
		server, err := NewGRPCServer(t.Context(), cfg, nil, nil, []RegistrationFunc{rf})

		must.NoError(t, err)
		test.NotNil(t, server)
		test.True(t, called)
	})

	T.Run("returns error with invalid TLS files", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Port:                  0,
			TLSCertificateFile:    "/nonexistent/cert.pem",
			TLSCertificateKeyFile: "/nonexistent/key.pem",
		}

		server, err := NewGRPCServer(t.Context(), cfg, nil, nil, nil)

		test.Nil(t, server)
		test.Error(t, err)
	})

	T.Run("succeeds with valid TLS files", func(t *testing.T) {
		t.Parallel()

		certFile, keyFile := generateTestTLSCerts(t)

		cfg := &Config{
			Port:                  0,
			TLSCertificateFile:    certFile,
			TLSCertificateKeyFile: keyFile,
		}

		server, err := NewGRPCServer(t.Context(), cfg, nil, nil, nil)

		must.NoError(t, err)
		test.NotNil(t, server)
	})
}

func TestLoggingInterceptor(T *testing.T) {
	T.Parallel()

	T.Run("returns interceptor that calls handler", func(t *testing.T) {
		t.Parallel()

		interceptor := LoggingInterceptor(nil)
		test.NotNil(t, interceptor)

		handlerCalled := false
		handler := func(ctx context.Context, req any) (any, error) {
			handlerCalled = true
			return "result", nil
		}

		info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}
		result, err := interceptor(context.Background(), "request", info, handler)

		test.NoError(t, err)
		test.Eq(t, "result", result)
		test.True(t, handlerCalled)
	})

	T.Run("logs error when handler fails", func(t *testing.T) {
		t.Parallel()

		interceptor := LoggingInterceptor(loggingnoop.NewLogger())
		test.NotNil(t, interceptor)

		expectedErr := errStub
		handler := func(ctx context.Context, req any) (any, error) {
			return nil, expectedErr
		}

		info := &grpc.UnaryServerInfo{FullMethod: "/test/Method"}
		result, err := interceptor(context.Background(), "request", info, handler)

		test.ErrorIs(t, err, expectedErr)
		test.Nil(t, result)
	})
}

func TestServer_Shutdown(T *testing.T) {
	T.Parallel()

	T.Run("shuts down gracefully", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Port: 0}
		server, err := NewGRPCServer(t.Context(), cfg, nil, nil, nil)
		must.NoError(t, err)

		server.Shutdown(context.Background())
	})

	T.Run("shuts down with logger", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Port: 0}
		server, err := NewGRPCServer(t.Context(), cfg, nil, nil, nil)
		must.NoError(t, err)

		server.Shutdown(context.Background())
	})

	T.Run("logs error when ForceFlush fails", func(t *testing.T) {
		t.Parallel()

		mtp := &mockTracerProvider{
			forceFlushFunc: func(_ context.Context) error { return errors.New("flush failed") },
		}

		cfg := &Config{Port: 0}
		srv, err := NewGRPCServer(t.Context(), cfg, nil, nil, nil, WithTracerProvider(mtp))
		must.NoError(t, err)

		_ = srv.Shutdown(context.Background())

		test.EqOp(t, 1, mtp.forceFlushCalls)
	})

	T.Run("flushes the tracer provider without shutting it down", func(t *testing.T) {
		t.Parallel()

		// Same reason as the HTTP sibling: the provider is shared with
		// everything else still shutting down, and this server does not own it.
		mtp := &mockTracerProvider{}

		srv, err := NewGRPCServer(t.Context(), &Config{Port: 0}, nil, nil, nil, WithTracerProvider(mtp))
		must.NoError(t, err)

		_ = srv.Shutdown(context.Background())

		test.EqOp(t, 1, mtp.forceFlushCalls)
		test.EqOp(t, 0, mtp.shutdownCalls)
	})
}

func TestNewGRPCServer_withInterceptors(T *testing.T) {
	T.Parallel()

	T.Run("with unary interceptors", func(t *testing.T) {
		t.Parallel()

		unaryInterceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}

		cfg := &Config{Port: 0}
		server, err := NewGRPCServer(t.Context(), cfg, []grpc.UnaryServerInterceptor{unaryInterceptor}, nil, nil)

		must.NoError(t, err)
		test.NotNil(t, server)
	})

	T.Run("with stream interceptors", func(t *testing.T) {
		t.Parallel()

		streamInterceptor := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, ss)
		}

		cfg := &Config{Port: 0}
		server, err := NewGRPCServer(t.Context(), cfg, nil, []grpc.StreamServerInterceptor{streamInterceptor}, nil)

		must.NoError(t, err)
		test.NotNil(t, server)
	})

	T.Run("with multiple registration functions", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		rf1 := func(s *grpc.Server) { callCount++ }
		rf2 := func(s *grpc.Server) { callCount++ }

		cfg := &Config{Port: 0}
		server, err := NewGRPCServer(t.Context(), cfg, nil, nil, []RegistrationFunc{rf1, rf2})

		must.NoError(t, err)
		test.NotNil(t, server)
		test.EqOp(t, 2, callCount)
	})
}

func TestServer_Serve(T *testing.T) {
	T.Parallel()

	T.Run("serves and can be stopped", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Port: 0}
		srv, err := NewGRPCServer(t.Context(), cfg, nil, nil, nil)
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())

		done := make(chan struct{})
		go func() {
			_ = srv.Serve(ctx)
			close(done)
		}()

		// Give the server a moment to start listening, then stop it.
		time.Sleep(50 * time.Millisecond)
		srv.grpcServer.GracefulStop()
		cancel()
		<-done
	})

	T.Run("returns when listen fails", func(t *testing.T) {
		t.Parallel()

		// Occupy a port so the server's Listen call fails with "address already in use".
		lis, err := new(net.ListenConfig).Listen(t.Context(), "tcp", ":0")
		must.NoError(t, err)
		defer lis.Close()

		port := lis.Addr().(*net.TCPAddr).Port

		cfg := &Config{Port: uint16(port)}
		srv, err := NewGRPCServer(t.Context(), cfg, nil, nil, nil)
		must.NoError(t, err)

		// Should return immediately because the port is already in use.
		_ = srv.Serve(t.Context())
	})
}

func TestNewGRPCServer_Options(T *testing.T) {
	T.Parallel()

	T.Run("reflection is off unless asked for", func(t *testing.T) {
		t.Parallel()

		// Reflection publishes a full method inventory to anyone who can reach
		// the port, so it is opt-in rather than always registered.
		srv, err := NewGRPCServer(t.Context(), &Config{Port: 0}, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, srv)

		test.MapEmpty(t, srv.grpcServer.GetServiceInfo())
	})

	T.Run("WithReflection registers the reflection service", func(t *testing.T) {
		t.Parallel()

		srv, err := NewGRPCServer(t.Context(), &Config{Port: 0}, nil, nil, nil, WithReflection())
		must.NoError(t, err)
		must.NotNil(t, srv)

		test.MapNotEmpty(t, srv.grpcServer.GetServiceInfo())
	})

	T.Run("WithServiceName names the logger", func(t *testing.T) {
		t.Parallel()

		srv, err := NewGRPCServer(t.Context(), &Config{Port: 0}, nil, nil, nil, WithServiceName("billing"))
		must.NoError(t, err)
		must.NotNil(t, srv.logger)
	})
}

func TestServer_Serve_ReturnValues(T *testing.T) {
	T.Parallel()

	T.Run("a bind failure is returned rather than swallowed", func(t *testing.T) {
		t.Parallel()

		lis, err := new(net.ListenConfig).Listen(t.Context(), "tcp", ":0")
		must.NoError(t, err)
		t.Cleanup(func() { _ = lis.Close() })

		port, ok := lis.Addr().(*net.TCPAddr)
		must.True(t, ok)

		srv, err := NewGRPCServer(t.Context(), &Config{Port: uint16(port.Port)}, nil, nil, nil)
		must.NoError(t, err)

		// This used to return nothing at all, so a dead server was silent.
		test.Error(t, srv.Serve(t.Context()))
	})

	T.Run("a graceful stop reports success", func(t *testing.T) {
		t.Parallel()

		srv, err := NewGRPCServer(t.Context(), &Config{Port: 0}, nil, nil, nil)
		must.NoError(t, err)

		errs := make(chan error, 1)
		go func() { errs <- srv.Serve(t.Context()) }()

		time.Sleep(50 * time.Millisecond)
		srv.grpcServer.GracefulStop()

		// grpc.ErrServerStopped is the one "this is normal" answer, and must not
		// reach the caller as a failure.
		test.NoError(t, <-errs)
	})
}

func TestServer_Shutdown_ContextExpiry(T *testing.T) {
	T.Parallel()

	T.Run("an expired context reports its error after a hard stop", func(t *testing.T) {
		t.Parallel()

		srv, err := NewGRPCServer(t.Context(), &Config{Port: 0}, nil, nil, nil)
		must.NoError(t, err)

		serveErrs := make(chan error, 1)
		go func() { serveErrs <- srv.Serve(t.Context()) }()
		time.Sleep(50 * time.Millisecond)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		// The drain deadline is already blown, so Shutdown falls back to Stop and
		// reports why it could not drain.
		test.ErrorIs(t, srv.Shutdown(ctx), context.Canceled)
		<-serveErrs
	})
}
