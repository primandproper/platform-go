package service

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/encoding"
	"github.com/primandproper/primitives-go/v2/routing"
	"github.com/primandproper/primitives-go/v2/routing/backends/chi"
	routingcfg "github.com/primandproper/primitives-go/v2/routing/config"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"
	httpserver "github.com/primandproper/primitives-go/v2/server/http"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"
)

// grpcWebServersConfig is a Config with both servers and the router between
// them, and the gRPC-web block given, which a nil leaves out.
func grpcWebServersConfig(web *GRPCWebConfig) *Config {
	return &Config{
		Name:       "example",
		Encoding:   &encoding.Config{ContentType: string(encoding.ContentTypeJSON)},
		Routing:    &routingcfg.Config{Provider: routingcfg.ProviderChi, Chi: &chi.Config{ServiceName: "example", SilenceRouteLogging: true}},
		HTTPServer: &httpserver.Config{Port: 8080, StartupDeadline: time.Second},
		GRPCServer: &grpcserver.Config{MaxReceiveMessageSize: grpcserver.DefaultMaxMessageSize},
		GRPCWeb:    web,
	}
}

// newGRPCWebService builds cfg through New, with the interceptors and
// registrations the gRPC server resolves and no service of the application's:
// the health service Register's registry puts on it is what a call reaches.
func newGRPCWebService(t *testing.T, cfg *Config) (*routing.Router, error) {
	t.Helper()

	i := newInjector(t, cfg)
	do.ProvideValue(i, []grpc.UnaryServerInterceptor{})
	do.ProvideValue(i, []grpc.StreamServerInterceptor{})
	do.ProvideValue(i, []grpcserver.RegistrationFunc{})

	if _, err := New(i); err != nil {
		return nil, err
	}

	return do.MustInvoke[*routing.Router](i), nil
}

// healthCheckOverGRPCWeb makes a grpc.health.v1 Check against url as a gRPC-web
// client does, and returns the HTTP status, the grpc-status the call ended
// with, and the response when one came back.
func healthCheckOverGRPCWeb(t *testing.T, url string) (code int, grpcStatus string, reply *grpc_health_v1.HealthCheckResponse) {
	t.Helper()

	payload, err := proto.Marshal(&grpc_health_v1.HealthCheckRequest{})
	must.NoError(t, err)

	body := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(body[1:], uint32(len(payload)))
	body = append(body, payload...)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	must.NoError(t, err)
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")

	res, err := http.DefaultClient.Do(req)
	must.NoError(t, err)

	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(res.Body)
	must.NoError(t, err)

	// A refusal from the router rather than the handler is not gRPC-web at
	// all, and has no frames to read.
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/grpc-web") {
		return res.StatusCode, "", nil
	}

	grpcStatus = res.Header.Get("Grpc-Status")

	for len(raw) >= 5 {
		flag, size := raw[0], binary.BigEndian.Uint32(raw[1:5])
		must.LessEq(t, uint32(len(raw)-5), size, must.Sprint("a frame runs past the end of the body"))

		frame := raw[5 : 5+size]
		raw = raw[5+size:]

		if flag&0x80 != 0 {
			for line := range strings.SplitSeq(string(frame), "\r\n") {
				if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(name, "grpc-status") {
					grpcStatus = strings.TrimSpace(value)
				}
			}

			continue
		}

		reply = &grpc_health_v1.HealthCheckResponse{}
		must.NoError(t, proto.Unmarshal(frame, reply))
	}

	return res.StatusCode, grpcStatus, reply
}

func TestGRPCWebConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	for _, prefix := range []string{"/grpc", "/api/grpc-web", "/v1.grpc"} {
		T.Run("accepts "+prefix, func(t *testing.T) {
			t.Parallel()

			test.NoError(t, (&GRPCWebConfig{PathPrefix: prefix}).ValidateWithContext(t.Context()))
		})
	}

	for name, prefix := range map[string]string{
		"an empty prefix":            "",
		"a relative prefix":          "grpc",
		"a trailing slash":           "/grpc/",
		"the root":                   "/",
		"an empty segment":           "/api//grpc",
		"a route parameter":          "/{tenant}/grpc",
		"a wildcard":                 "/grpc/*",
		"a colon parameter":          "/:tenant",
		"whitespace inside the path": "/g rpc",
	} {
		T.Run("refuses "+name, func(t *testing.T) {
			t.Parallel()

			test.Error(t, (&GRPCWebConfig{PathPrefix: prefix}).ValidateWithContext(t.Context()))
		})
	}
}

func TestConfig_grpcWeb(T *testing.T) {
	T.Parallel()

	T.Run("a block naming no prefix is no block", func(t *testing.T) {
		t.Parallel()

		cfg := grpcWebServersConfig(&GRPCWebConfig{})
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		test.Nil(t, cfg.GRPCWeb)
	})

	T.Run("validates beside both servers", func(t *testing.T) {
		t.Parallel()

		cfg := grpcWebServersConfig(&GRPCWebConfig{PathPrefix: "/grpc"})
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix with no gRPC server behind it", func(t *testing.T) {
		t.Parallel()

		cfg := grpcWebServersConfig(&GRPCWebConfig{PathPrefix: "/grpc"})
		cfg.GRPCServer = nil

		err := cfg.ValidateWithContext(t.Context())
		must.ErrorIs(t, err, ErrGRPCWebWithoutGRPCServer)
		test.StrContains(t, err.Error(), "grpcWeb")
		test.StrContains(t, err.Error(), "grpcServer")
	})

	T.Run("refuses a prefix with no HTTP server to mount it on", func(t *testing.T) {
		t.Parallel()

		cfg := grpcWebServersConfig(&GRPCWebConfig{PathPrefix: "/grpc"})
		cfg.HTTPServer = nil

		err := cfg.ValidateWithContext(t.Context())
		must.ErrorIs(t, err, ErrGRPCWebWithoutHTTPServer)
		test.StrContains(t, err.Error(), "grpcWeb")
		test.StrContains(t, err.Error(), "httpServer")
	})

	T.Run("refuses a malformed prefix", func(t *testing.T) {
		t.Parallel()

		cfg := grpcWebServersConfig(&GRPCWebConfig{PathPrefix: "/grpc/"})
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNew_grpcWeb(T *testing.T) {
	T.Parallel()

	T.Run("serves the gRPC server's services on the HTTP router, under the prefix", func(t *testing.T) {
		t.Parallel()

		cfg := grpcWebServersConfig(&GRPCWebConfig{PathPrefix: "/grpc"})
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		router, err := newGRPCWebService(t, cfg)
		must.NoError(t, err)

		srv := httptest.NewServer(router.Handler())
		t.Cleanup(srv.Close)

		code, grpcStatus, reply := healthCheckOverGRPCWeb(t, srv.URL+"/grpc/grpc.health.v1.Health/Check")
		must.EqOp(t, http.StatusOK, code)
		must.EqOp(t, "0", grpcStatus)
		must.NotNil(t, reply)
		test.EqOp(t, grpc_health_v1.HealthCheckResponse_SERVING, reply.GetStatus())
	})

	T.Run("mounts nothing outside the prefix", func(t *testing.T) {
		t.Parallel()

		cfg := grpcWebServersConfig(&GRPCWebConfig{PathPrefix: "/grpc"})
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		router, err := newGRPCWebService(t, cfg)
		must.NoError(t, err)

		srv := httptest.NewServer(router.Handler())
		t.Cleanup(srv.Close)

		code, _, _ := healthCheckOverGRPCWeb(t, srv.URL+"/grpc.health.v1.Health/Check")
		test.EqOp(t, http.StatusNotFound, code)
	})

	T.Run("unconfigured, no route is mounted", func(t *testing.T) {
		t.Parallel()

		cfg := grpcWebServersConfig(nil)
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		router, err := newGRPCWebService(t, cfg)
		must.NoError(t, err)

		srv := httptest.NewServer(router.Handler())
		t.Cleanup(srv.Close)

		code, _, _ := healthCheckOverGRPCWeb(t, srv.URL+"/grpc/grpc.health.v1.Health/Check")
		test.EqOp(t, http.StatusNotFound, code)
	})

	T.Run("an unvalidated config with no gRPC server fails the boot by name", func(t *testing.T) {
		t.Parallel()

		// Register does not validate, so a Config assembled in code reaches the
		// mount with whatever it was given. The mount refuses it the way
		// ValidateWithContext would have.
		cfg := grpcWebServersConfig(&GRPCWebConfig{PathPrefix: "/grpc"})
		cfg.GRPCServer = nil

		i := newInjector(t, cfg)

		_, err := New(i)
		test.ErrorIs(t, err, ErrGRPCWebWithoutGRPCServer)
	})
}
