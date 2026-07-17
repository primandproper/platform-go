package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v5/circuitbreaking"
	mockcircuitbreaking "github.com/primandproper/platform-go/v5/circuitbreaking/mock"
	cbnoop "github.com/primandproper/platform-go/v5/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v5/identifiers"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
	"github.com/primandproper/platform-go/v5/testutils/containers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	elasticsearchcontainers "github.com/testcontainers/testcontainers-go/modules/elasticsearch"
	"github.com/testcontainers/testcontainers-go/wait"
)

const elasticsearchImage = "elasticsearch:8.10.2"

// esTestInfra holds a single shared Elasticsearch container for all container-
// backed subtests inside a package run. Subtests use unique index names to stay
// isolated, mirroring the qdrant/pgvector/distributedlock shared-container
// pattern.
type esTestInfra struct {
	cfg      *Config
	shutdown func(context.Context) error
}

// extendWaitStrategyTimeout returns a PostCreates lifecycle hook that extends
// the timeouts of the elasticsearch module's bundled wait strategies.
//
// Why this exists: the elasticsearch module appends its own configureWaitFor
// customizer AFTER user opts, via WithAdditionalWaitStrategyAndDeadline(60s, ...),
// which unconditionally clamps the outer MultiStrategy deadline to 60s. The
// inner HTTPStrategy and FileStrategy also each default to 60s, and
// HTTPStrategy.WaitUntilReady wraps its ctx in context.WithTimeout(60s), so
// extending only the outer deadline is insufficient — each inner strategy
// must be extended individually. Neither WithStartupTimeoutDefault on
// MultiStrategy nor passing WithWaitStrategyAndDeadline as a user opt works
// around this; both get overwritten by the module's append.
//
// A cold start (image pull + ES auto-config + cert generation) regularly
// exceeds 60s on a busy CI host, so 60s is too tight in practice.
//
// The type assertions are load-bearing: we have to touch concrete types
// (*wait.MultiStrategy and the inner *wait.HTTPStrategy / *wait.FileStrategy)
// because wait.Strategy has no interface method for mutating a timeout. A
// future testcontainers refactor that changes these types will fail loudly
// here rather than silently regressing to a flaky 60s ceiling — which is the
// right failure mode.
func extendWaitStrategyTimeout(timeout time.Duration) testcontainers.ContainerHook {
	return func(_ context.Context, c testcontainers.Container) error {
		dc, ok := c.(*testcontainers.DockerContainer)
		if !ok {
			return fmt.Errorf("extendWaitStrategyTimeout: unexpected container type %T", c)
		}
		ms, ok := dc.WaitingFor.(*wait.MultiStrategy)
		if !ok {
			return fmt.Errorf("extendWaitStrategyTimeout: unexpected wait strategy type %T", dc.WaitingFor)
		}
		ms.WithDeadline(timeout)
		for _, s := range ms.Strategies {
			switch w := s.(type) {
			case *wait.FileStrategy:
				w.WithStartupTimeout(timeout)
			case *wait.HTTPStrategy:
				w.WithStartupTimeout(timeout)
			}
		}
		return nil
	}
}

func buildEsTestInfra(t *testing.T) *esTestInfra {
	t.Helper()

	elasticsearchContainer, err := containers.StartWithRetry(t.Context(), func(ctx context.Context) (*elasticsearchcontainers.ElasticsearchContainer, error) {
		return elasticsearchcontainers.Run(
			ctx,
			elasticsearchImage,
			elasticsearchcontainers.WithPassword("arbitraryPassword"),
			testcontainers.WithAdditionalLifecycleHooks(testcontainers.ContainerLifecycleHooks{
				PostCreates: []testcontainers.ContainerHook{
					extendWaitStrategyTimeout(5 * time.Minute),
				},
			}),
		)
	})
	must.NoError(t, err)
	must.NotNil(t, elasticsearchContainer)

	cfg := &Config{
		Address:               elasticsearchContainer.Settings.Address,
		IndexOperationTimeout: 0,
		Username:              "elastic",
		Password:              elasticsearchContainer.Settings.Password,
		CACert:                elasticsearchContainer.Settings.CACert,
	}

	return &esTestInfra{
		cfg:      cfg,
		shutdown: func(ctx context.Context) error { return elasticsearchContainer.Terminate(ctx) },
	}
}

// TestElasticsearch_Container holds every subtest that needs a real
// Elasticsearch container. They all share one container so we pay the
// pull/start cost once per package run. Each subtest creates its own
// index via unique identifiers.New() names to stay isolated.
func TestElasticsearch_Container(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	// The elasticsearch:8.x images crash with SIGILL inside the bundled JDK
	// when run under linux/arm64 on Docker Desktop for Mac, so the cert wait
	// strategy times out and the suite flakes. Skip until ES ships a JDK
	// that tolerates this host.
	if runtime.GOARCH == "arm64" {
		T.Skip("elasticsearch JDK crashes on linux/arm64 under Docker Desktop; skipping")
	}

	infra := buildEsTestInfra(T)
	T.Cleanup(func() { _ = infra.shutdown(context.Background()) })

	// --- ensureIndices ---

	T.Run("ensureIndices creates index when it does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		indexName := "ensure_create_" + identifiers.New()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, indexName, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		test.NotNil(t, im)

		searchable := &example{
			ID:   identifiers.New(),
			Name: "test document",
		}

		test.NoError(t, im.Index(ctx, searchable.ID, searchable))
	})

	T.Run("ensureIndices handles existing index", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		indexName := "ensure_existing_" + identifiers.New()
		im1, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, indexName, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		im2, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, indexName, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		test.NotNil(t, im1)
		test.NotNil(t, im2)

		searchable := &example{
			ID:   identifiers.New(),
			Name: "test document",
		}

		test.NoError(t, im1.Index(ctx, searchable.ID, searchable))
		test.NoError(t, im2.Index(ctx, searchable.ID+"_2", searchable))
	})

	// --- NewIndexManager ---

	T.Run("NewIndexManager standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "provide_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		test.NoError(t, err)
		test.NotNil(t, im)
	})

	T.Run("NewIndexManager with logger and tracer", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		im, err := NewIndexManager[example](ctx, logger, tracerProvider, infra.cfg, "provide_lt_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		test.NoError(t, err)
		test.NotNil(t, im)
	})

	// --- elasticsearchIsReadyToInit ---

	T.Run("elasticsearchIsReadyToInit returns true with valid config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		ready := elasticsearchIsReadyToInit(ctx, infra.cfg, logger, 5)
		test.True(t, ready)
	})

	// --- provideElasticsearchClient ---

	T.Run("provideElasticsearchClient succeeds", func(t *testing.T) {
		t.Parallel()

		client, err := provideElasticsearchClient(infra.cfg)
		test.NoError(t, err)
		test.NotNil(t, client)
	})

	// --- complete lifecycle ---

	T.Run("complete lifecycle", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "lifecycle_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		test.NoError(t, err)
		test.NotNil(t, im)

		searchable := &example{
			ID:   identifiers.New(),
			Name: t.Name(),
		}

		test.NoError(t, im.Index(ctx, searchable.ID, searchable))

		time.Sleep(5 * time.Second)

		results, err := im.Search(ctx, searchable.Name)
		test.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.Eq(t, searchable, results[0])

		test.NoError(t, im.Delete(ctx, searchable.ID))
	})

	// --- Index ---

	T.Run("Index successful", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "idx_ok_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		searchable := &example{
			ID:   identifiers.New(),
			Name: t.Name(),
		}

		test.NoError(t, im.Index(ctx, searchable.ID, searchable))
	})

	T.Run("Index json marshaling error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "idx_json_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		invalid := &invalidJSON{
			Channel: make(chan int),
		}

		test.Error(t, im.Index(ctx, "test-id", invalid))
	})

	T.Run("Index with noop circuit breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cb := cbnoop.NewCircuitBreaker()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "idx_cb_"+identifiers.New(), cb)
		must.NoError(t, err)

		searchable := &example{
			ID:   identifiers.New(),
			Name: t.Name(),
		}

		test.NoError(t, im.Index(ctx, searchable.ID, searchable))
	})

	// --- Search ---

	T.Run("Search successful", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "search_ok_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		searchable := &example{
			ID:   identifiers.New(),
			Name: "test search document",
		}
		must.NoError(t, im.Index(ctx, searchable.ID, searchable))

		time.Sleep(2 * time.Second)

		results, err := im.Search(ctx, "test")
		test.NoError(t, err)
		test.SliceLen(t, 1, results)
		test.EqOp(t, searchable.ID, results[0].ID)
	})

	T.Run("Search empty query error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "search_empty_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		results, err := im.Search(ctx, "")
		test.Error(t, err)
		test.Nil(t, results)
		test.ErrorIs(t, err, ErrEmptyQueryProvided)
	})

	T.Run("Search no results found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "search_noresult_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		results, err := im.Search(ctx, "nonexistent document")
		test.NoError(t, err)
		test.SliceLen(t, 0, results)
	})

	// --- Delete ---

	T.Run("Delete successful", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "del_ok_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		searchable := &example{
			ID:   identifiers.New(),
			Name: "test delete document",
		}
		must.NoError(t, im.Index(ctx, searchable.ID, searchable))

		test.NoError(t, im.Delete(ctx, searchable.ID))
	})

	T.Run("Delete non-existent document", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "del_nf_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		test.NoError(t, im.Delete(ctx, "non-existent-id"))
	})

	// --- Wipe ---

	T.Run("Wipe removes all documents", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		im, err := NewIndexManager[example](ctx, nil, nil, infra.cfg, "wipe_"+identifiers.New(), cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		searchable := &example{
			ID:   identifiers.New(),
			Name: "test wipe document",
		}
		must.NoError(t, im.Index(ctx, searchable.ID, searchable))

		time.Sleep(2 * time.Second)

		results, err := im.Search(ctx, "wipe")
		must.NoError(t, err)
		must.SliceLen(t, 1, results)

		test.NoError(t, im.Wipe(ctx))

		time.Sleep(2 * time.Second)

		results, err = im.Search(ctx, "wipe")
		test.NoError(t, err)
		test.SliceLen(t, 0, results)
	})
}

func TestIndexManager_ensureIndices_CircuitBroken(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		err := im.ensureIndices(context.Background())
		test.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with unreachable server", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		err := im.ensureIndices(context.Background())
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_ensureIndices_Unit(T *testing.T) {
	T.Parallel()

	T.Run("index exists", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			if r.Method == http.MethodHead && r.URL.Path == "/test" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		err := im.ensureIndices(context.Background())
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("index does not exist and create succeeds", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			if r.Method == http.MethodHead && r.URL.Path == "/test" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodPut && r.URL.Path == "/test" {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"acknowledged":true}`)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		err := im.ensureIndices(context.Background())
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("index does not exist and create fails", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			if r.Method == http.MethodHead && r.URL.Path == "/test" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodPut && r.URL.Path == "/test" {
				// close connection to cause an error
				hj, ok := w.(http.Hijacker)
				if ok {
					conn, _, _ := hj.Hijack()
					conn.Close()
				}
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		err := im.ensureIndices(context.Background())
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("existence check server error is not treated as index-exists", func(t *testing.T) {
		t.Parallel()

		// IndicesExists returns 200 (exists) or 404 (missing). A 500 (or 401) must be
		// surfaced as an error rather than silently taken to mean the index exists.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			if r.Method == http.MethodHead && r.URL.Path == "/test" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		err := im.ensureIndices(context.Background())
		test.Error(t, err)
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func Test_provideElasticsearchClient_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Address: "http://localhost:9200",
		}

		client, err := provideElasticsearchClient(cfg)
		test.NoError(t, err)
		test.NotNil(t, client)
	})

	T.Run("with credentials", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Address:  "http://localhost:9200",
			Username: "elastic",
			Password: "password",
		}

		client, err := provideElasticsearchClient(cfg)
		test.NoError(t, err)
		test.NotNil(t, client)
	})
}

func Test_elasticsearchIsReadyToInit_Unit(T *testing.T) {
	T.Parallel()

	T.Run("returns false with unreachable server", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Address: "http://localhost:19291",
		}

		logger := loggingnoop.NewLogger()
		ready := elasticsearchIsReadyToInit(context.Background(), cfg, logger, 1)
		test.False(t, ready)
	})

	T.Run("returns true with reachable server", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"name":"node","cluster_name":"test","version":{"number":"8.10.2"}}`)
		}))
		t.Cleanup(server.Close)

		cfg := &Config{
			Address: server.URL,
		}

		logger := loggingnoop.NewLogger()
		ready := elasticsearchIsReadyToInit(context.Background(), cfg, logger, 3)
		test.True(t, ready)
	})
}

func TestNewIndexManager_Unit(T *testing.T) {
	T.Parallel()

	T.Run("succeeds with mock server", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")

			// Info request from elasticsearchIsReadyToInit
			if r.Method == http.MethodGet && r.URL.Path == "/" {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"name":"node","cluster_name":"test","version":{"number":"8.10.2"}}`)
				return
			}

			// Index exists check from ensureIndices
			if r.Method == http.MethodHead && r.URL.Path == "/test" {
				w.WriteHeader(http.StatusOK)
				return
			}

			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		cfg := &Config{
			Address: server.URL,
		}

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, err := NewIndexManager[example](context.Background(), logger, tracerProvider, cfg, "test", cb)
		test.NoError(t, err)
		test.NotNil(t, im)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("fails when ensureIndices fails", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")

			// Info request succeeds
			if r.Method == http.MethodGet && r.URL.Path == "/" {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{"name":"node","cluster_name":"test","version":{"number":"8.10.2"}}`)
				return
			}

			// Index existence check returns 404
			if r.Method == http.MethodHead && r.URL.Path == "/test" {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			// Index creation: close connection to trigger error
			if r.Method == http.MethodPut && r.URL.Path == "/test" {
				hj, ok := w.(http.Hijacker)
				if ok {
					conn, _, _ := hj.Hijack()
					conn.Close()
				}
				return
			}

			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)

		cfg := &Config{
			Address: server.URL,
		}

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, err := NewIndexManager[example](context.Background(), logger, tracerProvider, cfg, "test", cb)
		test.Error(t, err)
		test.Nil(t, im)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}
