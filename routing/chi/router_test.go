package chi

import (
	"context"
	"net/http"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"
	"github.com/primandproper/platform-go/observability/tracing"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
	"github.com/primandproper/platform-go/routing"

	"github.com/go-chi/chi/v5"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func buildRouterForTest() routing.Router {
	return NewRouter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), &Config{})
}

func TestNewRouter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewRouter(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), &Config{}))
	})
}

func Test_buildChiMux(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, buildChiMux(loggingnoop.NewLogger(), tracing.NewTracerForTest(t.Name()), metricsnoop.NewMetricsProvider(), &Config{}))
	})
}

func Test_convertMiddleware(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, convertMiddleware(func(http.Handler) http.Handler { return nil }))
	})
}

func Test_router_AddRoute(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		methods := []string{
			http.MethodGet,
			http.MethodHead,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodConnect,
			http.MethodOptions,
			http.MethodTrace,
		}

		for _, method := range methods {
			test.NoError(t, r.AddRoute(method, "/path", nil))
		}
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		test.Error(t, r.AddRoute("blah", "/path", nil))
	})
}

func Test_router_Connect(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Connect("/test", nil)
	})
}

func Test_router_Delete(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Delete("/test", nil)
	})
}

func Test_router_Get(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Get("/test", nil)
	})
}

func Test_router_Handle(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Handle("/test", nil)
	})
}

func Test_router_HandleFunc(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.HandleFunc("/test", nil)
	})
}

func Test_router_Handler(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		test.NotNil(t, r.Handler())
	})
}

func Test_router_Head(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Head("/test", nil)
	})
}

func Test_router_LogRoutes(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		test.NoError(t, r.AddRoute(http.MethodGet, "/path", nil))

		r.Routes()
	})
}

func Test_router_Options(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Options("/test", nil)
	})
}

func Test_router_Patch(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Patch("/test", nil)
	})
}

func Test_router_Post(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Post("/test", nil)
	})
}

func Test_router_Put(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Put("/thing", nil)
	})
}

func Test_router_Route(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		test.NotNil(t, r.Route("/test", func(routing.Router) {}))
	})
}

func Test_router_Trace(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		r.Trace("/test", nil)
	})
}

func Test_router_WithMiddleware(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouterForTest()

		test.NotNil(t, r.WithMiddleware())
	})
}

func Test_router_clone(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouter(nil, nil, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), &Config{})

		test.NotNil(t, r.clone())
	})
}

func Test_router_BuildRouteParamIDFetcher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouter(nil, nil, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), &Config{})
		l := loggingnoop.NewLogger()
		ctx := t.Context()
		exampleKey := "blah"

		rf := r.BuildRouteParamIDFetcher(l, exampleKey, "desc")
		test.NotNil(t, rf)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/blah", http.NoBody)
		test.NoError(t, err)
		must.NotNil(t, req)

		expected := uint64(123456)

		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, &chi.Context{
			URLParams: chi.RouteParams{
				Keys:   []string{exampleKey},
				Values: []string{"123456"},
			},
		}))

		actual := rf(req)
		test.EqOp(t, expected, actual)
	})

	T.Run("without appropriate value attached to context", func(t *testing.T) {
		t.Parallel()

		r := buildRouter(nil, nil, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), &Config{})
		l := loggingnoop.NewLogger()
		ctx := t.Context()
		exampleKey := "blah"

		rf := r.BuildRouteParamIDFetcher(l, exampleKey, "desc")
		test.NotNil(t, rf)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/blah", http.NoBody)
		test.NoError(t, err)
		must.NotNil(t, req)

		actual := rf(req)
		test.EqOp(t, uint64(0), actual)
	})
}

func Test_router_BuildRouteParamStringIDFetcher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		r := buildRouter(nil, nil, tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), &Config{})
		ctx := t.Context()
		exampleKey := "blah"

		rf := r.BuildRouteParamStringIDFetcher(exampleKey)
		test.NotNil(t, rf)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/blah", http.NoBody)
		test.NoError(t, err)
		must.NotNil(t, req)

		expected := "fake_user_id"

		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, &chi.Context{
			URLParams: chi.RouteParams{
				Keys:   []string{exampleKey},
				Values: []string{expected},
			},
		}))

		actual := rf(req)
		test.EqOp(t, expected, actual)
	})
}
