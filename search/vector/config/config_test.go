package vectorsearchcfg

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"
	"github.com/primandproper/platform-go/v13/search/vector/pgvector"
	"github.com/primandproper/platform-go/v13/search/vector/qdrant"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

type testStruct struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("pgvector provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: PGvectorProvider,
			Pgvector: &pgvector.Config{
				Dimension: 3,
				Metric:    vectorsearch.DistanceCosine,
			},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("qdrant provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: QdrantProvider,
			Qdrant: &qdrant.Config{
				BaseURL:   "http://localhost:6333",
				Dimension: 3,
				Metric:    vectorsearch.DistanceCosine,
			},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "made-up"}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("pgvector provider without config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: PGvectorProvider}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("qdrant provider without config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: QdrantProvider}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	// The noop index is reachable by name; an unset provider is not, because an
	// index that accepts every write and returns no hits reads as an empty corpus.
	T.Run("empty provider is invalid", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("the noop provider is a valid choice", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestConfig_NewIndex(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		idx, err := NewIndex[testStruct](
			t.Context(),
			nil,
			nil,
			"idx",
		)
		test.ErrorIs(t, err, vectorsearch.ErrNilConfig)
		test.Nil(t, idx)
	})

	T.Run("unknown provider returns noop", func(t *testing.T) {
		t.Parallel()

		idx, err := NewIndex[testStruct](
			t.Context(),
			&Config{Provider: "unknown"},
			nil,
			"idx",
		)
		test.Error(t, err)
		test.Nil(t, idx)
	})

	T.Run("the noop provider returns the noop index", func(t *testing.T) {
		t.Parallel()

		idx, err := NewIndex[testStruct](
			t.Context(),
			&Config{Provider: ProviderNoop},
			nil,
			"idx",
		)
		must.NoError(t, err)
		must.NotNil(t, idx)
		test.NoError(t, idx.Wipe(t.Context()))
	})

	for _, provider := range []string{"", "   "} {
		T.Run("rejects provider "+strconv.Quote(provider), func(t *testing.T) {
			t.Parallel()

			idx, err := NewIndex[testStruct](
				t.Context(),
				&Config{Provider: provider},
				nil,
				"idx",
			)
			test.Error(t, err)
			test.Nil(t, idx)
		})
	}

	T.Run("pgvector provider with nil db returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: PGvectorProvider,
			Pgvector: &pgvector.Config{
				Dimension: 3,
				Metric:    vectorsearch.DistanceCosine,
			},
		}

		idx, err := NewIndex[testStruct](
			t.Context(),
			cfg,
			nil,
			"idx",
		)
		test.Error(t, err)
		test.Nil(t, idx)
	})

	T.Run("qdrant provider via httptest server", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/collections/stub"):
				w.WriteHeader(http.StatusNotFound)
			case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/collections/stub"):
				_, _ = json.Marshal(r.Body)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result":true,"status":"ok","time":0}`))
			default:
				http.Error(w, "unexpected", http.StatusBadRequest)
			}
		}))
		t.Cleanup(srv.Close)

		cfg := &Config{
			Provider: QdrantProvider,
			Qdrant: &qdrant.Config{
				BaseURL:   srv.URL,
				Dimension: 3,
				Metric:    vectorsearch.DistanceCosine,
				Timeout:   time.Second,
			},
		}

		idx, err := NewIndex[testStruct](
			t.Context(),
			cfg,
			nil,
			"stub",
		)
		must.NoError(t, err)
		must.NotNil(t, idx)
	})

	T.Run("circuit breaker init failure", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderNoop,
			CircuitBreaker: circuitbreakingcfg.Config{
				Name:                   "test-breaker",
				ErrorRate:              50,
				MinimumSampleThreshold: 10,
			},
		}

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, circuitbreakingcfg.TrippedCounterName, counterName)
				return &metricsmock.Int64CounterMock{}, errors.New("counter init failure")
			},
		}

		idx, err := NewIndex[testStruct](
			ctx,
			cfg,
			nil,
			"idx",
			WithMetricsProvider(mp),
		)
		test.Error(t, err)
		test.Nil(t, idx)
		test.StrContains(t, err.Error(), "circuit breaker")

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}
