package pgvector

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v4/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v4/database"
	"github.com/primandproper/platform-go/v4/identifiers"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v4/observability/metrics/mock"
	vectorsearch "github.com/primandproper/platform-go/v4/search/vector"
	"github.com/primandproper/platform-go/v4/testutils/containers"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

// counterResult bundles the values a mocked NewInt64Counter call returns.
type counterResult struct {
	counter metrics.Int64Counter
	err     error
}

// newCounterProviderMock returns a metrics.Provider mock whose NewInt64Counter
// implementation looks up the result keyed on the counter name. Unknown names
// fail the test.
func newCounterProviderMock(t *testing.T, results map[string]counterResult) *mockmetrics.ProviderMock {
	t.Helper()
	return &mockmetrics.ProviderMock{
		NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			res, ok := results[name]
			if !ok {
				t.Fatalf("unexpected NewInt64Counter call: %q", name)
			}
			return res.counter, res.err
		},
	}
}

const pgvectorImage = "pgvector/pgvector:pg17"

// testDBClient is a minimal database.Client backed by a single *sql.DB. It exists
// only to avoid pulling in database/postgres for tests in this leaf package.
type testDBClient struct {
	db *sql.DB
}

func (c *testDBClient) WriteDB() *sql.DB { return c.db }
func (c *testDBClient) ReadDB() *sql.DB  { return c.db }
func (c *testDBClient) Close() error     { return c.db.Close() }
func (c *testDBClient) CurrentTime() time.Time {
	return time.Now()
}
func (c *testDBClient) RollbackTransaction(_ context.Context, tx database.SQLQueryExecutorAndTransactionManager) {
	_ = tx.Rollback()
}

func buildContainerBackedPgvector(t *testing.T) (client *testDBClient, shutdown func(context.Context) error) {
	t.Helper()

	ctx := t.Context()
	container, err := containers.StartWithRetry(ctx, func(ctx context.Context) (*postgrescontainer.PostgresContainer, error) {
		return postgrescontainer.Run(
			ctx,
			pgvectorImage,
			postgrescontainer.WithDatabase("vectortest"),
			postgrescontainer.WithUsername("vectortest"),
			postgrescontainer.WithPassword("vectortest"),
			testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
		)
	})
	must.NoError(t, err)
	must.NotNil(t, container)

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	must.NoError(t, err)

	db, err := sql.Open("pgx", connStr)
	must.NoError(t, err)
	must.NoError(t, db.PingContext(ctx))

	return &testDBClient{db: db}, func(ctx context.Context) error {
		_ = db.Close()
		return container.Terminate(ctx)
	}
}

type doc struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

func provideTestIndex(t *testing.T, client database.Client, indexName string, dim int, distanceMetric vectorsearch.DistanceMetric) vectorsearch.Index[doc] {
	t.Helper()

	cfg := &Config{
		Dimension: dim,
		Metric:    distanceMetric,
	}
	im, err := ProvideIndex[doc](t.Context(), nil, nil, nil, cfg, client, indexName, cbnoop.NewCircuitBreaker())
	must.NoError(t, err)
	must.NotNil(t, im)
	return im
}

// newRecordingIndex builds an indexManager with a RecordingObserver swapped in, so
// a test can drive an operation and assert which fields it observed. It bypasses
// ProvideIndex (and therefore the container-backed schema migration) by building
// the struct directly, which the in-package test can do.
func newRecordingIndex(t *testing.T) (*indexManager[doc], *observability.RecordingObserver) {
	t.Helper()

	obs := observability.NewRecordingObserver()
	im := &indexManager[doc]{
		o11y:           obs,
		circuitBreaker: cbnoop.NewCircuitBreaker(),
		upsertCounter:  metricnoop.Int64Counter{},
		deleteCounter:  metricnoop.Int64Counter{},
		wipeCounter:    metricnoop.Int64Counter{},
		queryCounter:   metricnoop.Int64Counter{},
		errCounter:     metricnoop.Int64Counter{},
		latencyHist:    metricnoop.Float64Histogram{},
		indexName:      t.Name(),
		dimension:      3,
	}

	return im, obs
}

func TestIndexManager_ObservesIndexName(T *testing.T) {
	T.Parallel()

	T.Run("Upsert observes the index name", func(t *testing.T) {
		t.Parallel()

		im, obs := newRecordingIndex(t)

		// An empty Upsert short-circuits before touching the database, so the
		// only side effect is the observed index name on a completed operation.
		must.NoError(t, im.Upsert(t.Context()))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: im.indexName,
		})
	})

	T.Run("Delete observes the index name", func(t *testing.T) {
		t.Parallel()

		im, obs := newRecordingIndex(t)

		must.NoError(t, im.Delete(t.Context()))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: im.indexName,
		})
	})

	T.Run("Query records the failure while still observing the index name", func(t *testing.T) {
		t.Parallel()

		im, obs := newRecordingIndex(t)

		// A DB pointed at an unreachable address connects lazily, so QueryContext
		// fails cleanly and we exercise the error path: the index name must still
		// be observed and the failure recorded on the operation.
		db, err := sql.Open("pgx", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
		must.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		im.db = &testDBClient{db: db}

		_, err = im.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 1})
		must.Error(t, err)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: im.indexName,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestIndexManager_Query_RejectsNonStringFilter(T *testing.T) {
	T.Parallel()

	T.Run("non-string filter is rejected rather than silently ignored", func(t *testing.T) {
		t.Parallel()

		im, _ := newRecordingIndex(t)

		// A structured filter (e.g. one meant for the qdrant provider) must not be
		// silently dropped — dropping a tenant-scoping filter could leak rows across
		// tenants. It must fail loudly before any SQL is built or executed.
		_, err := im.Query(t.Context(), vectorsearch.QueryRequest{
			Embedding: []float32{1, 0, 0},
			TopK:      1,
			Filter:    map[string]any{"tenant": "acme"},
		})
		must.ErrorIs(t, err, ErrInvalidFilter)
	})
}

func TestProvideIndex(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, nil, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.ErrorIs(t, err, vectorsearch.ErrNilConfig)
	})

	T.Run("nil database", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}, nil, "idx", cbnoop.NewCircuitBreaker())
		must.ErrorIs(t, err, vectorsearch.ErrNilDatabaseClient)
	})

	T.Run("invalid dimension", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{Dimension: 0, Metric: vectorsearch.DistanceCosine}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
	})

	T.Run("invalid metric", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{Dimension: 3, Metric: "weird"}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
	})

	T.Run("invalid index name", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}, &testDBClient{}, "no-dashes", cbnoop.NewCircuitBreaker())
		must.ErrorIs(t, err, ErrInvalidIdentifier)
	})

	T.Run("invalid metadata column", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine, MetadataColumn: "weird-col"}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.ErrorIs(t, err, ErrInvalidIdentifier)
	})

	T.Run("error creating upsert counter", func(t *testing.T) {
		t.Parallel()

		mp := newCounterProviderMock(t, map[string]counterResult{
			"pgvector_index_upserts": {counter: metricnoop.Int64Counter{}, err: errors.New("forced error")},
		})

		_, err := ProvideIndex[doc](t.Context(), nil, nil, mp, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("error creating delete counter", func(t *testing.T) {
		t.Parallel()

		mp := newCounterProviderMock(t, map[string]counterResult{
			"pgvector_index_upserts": {counter: metricnoop.Int64Counter{}},
			"pgvector_index_deletes": {counter: metricnoop.Int64Counter{}, err: errors.New("forced error")},
		})

		_, err := ProvideIndex[doc](t.Context(), nil, nil, mp, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("error creating wipe counter", func(t *testing.T) {
		t.Parallel()

		mp := newCounterProviderMock(t, map[string]counterResult{
			"pgvector_index_upserts": {counter: metricnoop.Int64Counter{}},
			"pgvector_index_deletes": {counter: metricnoop.Int64Counter{}},
			"pgvector_index_wipes":   {counter: metricnoop.Int64Counter{}, err: errors.New("forced error")},
		})

		_, err := ProvideIndex[doc](t.Context(), nil, nil, mp, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		test.SliceLen(t, 3, mp.NewInt64CounterCalls())
	})

	T.Run("error creating query counter", func(t *testing.T) {
		t.Parallel()

		mp := newCounterProviderMock(t, map[string]counterResult{
			"pgvector_index_upserts": {counter: metricnoop.Int64Counter{}},
			"pgvector_index_deletes": {counter: metricnoop.Int64Counter{}},
			"pgvector_index_wipes":   {counter: metricnoop.Int64Counter{}},
			"pgvector_index_queries": {counter: metricnoop.Int64Counter{}, err: errors.New("forced error")},
		})

		_, err := ProvideIndex[doc](t.Context(), nil, nil, mp, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		test.SliceLen(t, 4, mp.NewInt64CounterCalls())
	})

	T.Run("error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := newCounterProviderMock(t, map[string]counterResult{
			"pgvector_index_upserts": {counter: metricnoop.Int64Counter{}},
			"pgvector_index_deletes": {counter: metricnoop.Int64Counter{}},
			"pgvector_index_wipes":   {counter: metricnoop.Int64Counter{}},
			"pgvector_index_queries": {counter: metricnoop.Int64Counter{}},
			"pgvector_index_errors":  {counter: metricnoop.Int64Counter{}, err: errors.New("forced error")},
		})

		_, err := ProvideIndex[doc](t.Context(), nil, nil, mp, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		test.SliceLen(t, 5, mp.NewInt64CounterCalls())
	})

	T.Run("error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, nil
			},
			NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				return metricnoop.Float64Histogram{}, errors.New("forced error")
			},
		}

		_, err := ProvideIndex[doc](t.Context(), nil, nil, mp, &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}, &testDBClient{}, "idx", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		test.SliceLen(t, 5, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func Test_operatorAndOpClass(T *testing.T) {
	T.Parallel()

	T.Run("cosine", func(t *testing.T) {
		t.Parallel()

		op, opsClass, err := operatorAndOpClass(vectorsearch.DistanceCosine)
		must.NoError(t, err)
		test.EqOp(t, "<=>", op)
		test.EqOp(t, "vector_cosine_ops", opsClass)
	})

	T.Run("dot product", func(t *testing.T) {
		t.Parallel()

		op, opsClass, err := operatorAndOpClass(vectorsearch.DistanceDotProduct)
		must.NoError(t, err)
		test.EqOp(t, "<#>", op)
		test.EqOp(t, "vector_ip_ops", opsClass)
	})

	T.Run("euclidean", func(t *testing.T) {
		t.Parallel()

		op, opsClass, err := operatorAndOpClass(vectorsearch.DistanceEuclidean)
		must.NoError(t, err)
		test.EqOp(t, "<->", op)
		test.EqOp(t, "vector_l2_ops", opsClass)
	})

	T.Run("invalid metric", func(t *testing.T) {
		t.Parallel()

		_, _, err := operatorAndOpClass("bogus")
		must.ErrorIs(t, err, vectorsearch.ErrInvalidMetric)
	})
}

func TestEncodeVector(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "[0.1,0.2,0.3]", encodeVector([]float32{0.1, 0.2, 0.3}))
	})

	T.Run("empty", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "[]", encodeVector(nil))
	})

	T.Run("integer-valued", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "[1,2,3]", encodeVector([]float32{1, 2, 3}))
	})
}

func TestQuoteIdent(T *testing.T) {
	T.Parallel()

	T.Run("simple", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, `"users"`, quoteIdent("users"))
	})

	T.Run("with embedded quote", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, `"foo""bar"`, quoteIdent(`foo"bar`))
	})
}

func TestPgTextArray(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, `{"a","b","c"}`, pgTextArray([]string{"a", "b", "c"}))
	})

	T.Run("with quotes", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, `{"a\"b","c"}`, pgTextArray([]string{`a"b`, "c"}))
	})
}

func TestMarshalUnmarshalMetadata(T *testing.T) {
	T.Parallel()

	T.Run("nil round-trip", func(t *testing.T) {
		t.Parallel()
		raw, err := marshalMetadata[doc](nil)
		must.NoError(t, err)
		test.Eq(t, []byte(`{}`), raw)

		out, err := unmarshalMetadata[doc](raw)
		must.NoError(t, err)
		must.NotNil(t, out)
	})

	T.Run("populated round-trip", func(t *testing.T) {
		t.Parallel()
		original := &doc{Kind: "doc", Title: "hello"}
		raw, err := marshalMetadata(original)
		must.NoError(t, err)

		out, err := unmarshalMetadata[doc](raw)
		must.NoError(t, err)
		must.NotNil(t, out)
		test.Eq(t, *original, *out)
	})

	T.Run("null is treated as nil", func(t *testing.T) {
		t.Parallel()
		out, err := unmarshalMetadata[doc]([]byte("null"))
		must.NoError(t, err)
		test.Nil(t, out)
	})

	T.Run("empty is treated as nil", func(t *testing.T) {
		t.Parallel()
		out, err := unmarshalMetadata[doc]([]byte{})
		must.NoError(t, err)
		test.Nil(t, out)
	})

	T.Run("invalid JSON returns error", func(t *testing.T) {
		t.Parallel()
		_, err := unmarshalMetadata[doc]([]byte(`{not json`))
		must.Error(t, err)
	})
}

func Test_firstWords(T *testing.T) {
	T.Parallel()

	T.Run("multi-word statement", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, "CREATE EXTENSION", firstWords("CREATE EXTENSION IF NOT EXISTS vector"))
	})

	T.Run("single word", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, "TRUNCATE", firstWords("TRUNCATE"))
	})

	T.Run("two words only", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, "DROP TABLE", firstWords("DROP TABLE"))
	})

	T.Run("leading whitespace is trimmed", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, "CREATE TABLE", firstWords("  CREATE TABLE foo"))
	})
}

func TestValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
	})

	T.Run("valid config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Dimension: 3, Metric: vectorsearch.DistanceCosine}
		err := cfg.ValidateWithContext(t.Context())
		must.NoError(t, err)
	})

	T.Run("missing dimension", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Metric: vectorsearch.DistanceCosine}
		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
	})

	T.Run("invalid metric", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Dimension: 3, Metric: "bogus"}
		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
	})
}

func TestPgvectorIndex_Container(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	client, shutdown := buildContainerBackedPgvector(T)
	T.Cleanup(func() {
		_ = shutdown(context.Background())
	})

	T.Run("Upsert and Query roundtrip", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "rt_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "a", Embedding: []float32{1, 0, 0}, Metadata: &doc{Kind: "doc", Title: "alpha"}},
			vectorsearch.Vector[doc]{ID: "b", Embedding: []float32{0, 1, 0}, Metadata: &doc{Kind: "doc", Title: "beta"}},
			vectorsearch.Vector[doc]{ID: "c", Embedding: []float32{0, 0, 1}, Metadata: &doc{Kind: "doc", Title: "gamma"}},
		))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{
			Embedding: []float32{1, 0, 0},
			TopK:      3,
		})
		must.NoError(t, err)
		must.SliceLen(t, 3, results)
		test.EqOp(t, "a", results[0].ID)
		must.NotNil(t, results[0].Metadata)
		test.EqOp(t, "alpha", results[0].Metadata.Title)
		test.InDelta(t, float32(0.0), results[0].Distance, float32(1e-5))
	})

	T.Run("Upsert updates existing row", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "upd_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		must.NoError(t, idx.Upsert(ctx, vectorsearch.Vector[doc]{ID: "x", Embedding: []float32{1, 0, 0}, Metadata: &doc{Title: "first"}}))
		must.NoError(t, idx.Upsert(ctx, vectorsearch.Vector[doc]{ID: "x", Embedding: []float32{0, 1, 0}, Metadata: &doc{Title: "second"}}))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{
			Embedding: []float32{0, 1, 0},
			TopK:      1,
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "x", results[0].ID)
		must.NotNil(t, results[0].Metadata)
		test.EqOp(t, "second", results[0].Metadata.Title)
	})

	T.Run("TopK is respected", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "topk_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "a", Embedding: []float32{1, 0, 0}},
			vectorsearch.Vector[doc]{ID: "b", Embedding: []float32{0, 1, 0}},
			vectorsearch.Vector[doc]{ID: "c", Embedding: []float32{0, 0, 1}},
		))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{
			Embedding: []float32{1, 0, 0},
			TopK:      2,
		})
		must.NoError(t, err)
		test.SliceLen(t, 2, results)
	})

	T.Run("filter clause is applied", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "filt_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "a", Embedding: []float32{1, 0, 0}, Metadata: &doc{Kind: "doc"}},
			vectorsearch.Vector[doc]{ID: "b", Embedding: []float32{1, 0, 0}, Metadata: &doc{Kind: "image"}},
		))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{
			Embedding: []float32{1, 0, 0},
			TopK:      10,
			Filter:    "metadata->>'kind' = 'doc'",
		})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "a", results[0].ID)
	})

	T.Run("Query rejects empty embedding", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "emb_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		_, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: nil, TopK: 5})
		must.ErrorIs(t, err, vectorsearch.ErrEmptyEmbedding)
	})

	T.Run("Query rejects wrong dimension", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "dim_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		_, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0}, TopK: 5})
		must.ErrorIs(t, err, vectorsearch.ErrDimensionMismatch)
	})

	T.Run("Upsert rejects wrong dimension", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "udim_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		err := idx.Upsert(ctx, vectorsearch.Vector[doc]{ID: "a", Embedding: []float32{1, 0}})
		must.ErrorIs(t, err, vectorsearch.ErrDimensionMismatch)
	})

	T.Run("Delete removes specific rows", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "del_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "a", Embedding: []float32{1, 0, 0}},
			vectorsearch.Vector[doc]{ID: "b", Embedding: []float32{0, 1, 0}},
			vectorsearch.Vector[doc]{ID: "c", Embedding: []float32{0, 0, 1}},
		))
		must.NoError(t, idx.Delete(ctx, "a", "c"))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{0, 1, 0}, TopK: 10})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "b", results[0].ID)
	})

	T.Run("Wipe empties the index", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provideTestIndex(t, client, "wipe_"+identifiers.New(), 3, vectorsearch.DistanceCosine)

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "a", Embedding: []float32{1, 0, 0}},
			vectorsearch.Vector[doc]{ID: "b", Embedding: []float32{0, 1, 0}},
		))
		must.NoError(t, idx.Wipe(ctx))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 10})
		must.NoError(t, err)
		test.SliceEmpty(t, results)
	})

	T.Run("ProvideIndex is idempotent for the same index", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		name := "idem_" + identifiers.New()
		idx1 := provideTestIndex(t, client, name, 3, vectorsearch.DistanceCosine)
		idx2 := provideTestIndex(t, client, name, 3, vectorsearch.DistanceCosine)
		test.NotNil(t, idx1)
		test.NotNil(t, idx2)

		must.NoError(t, idx1.Upsert(ctx, vectorsearch.Vector[doc]{ID: "shared", Embedding: []float32{1, 0, 0}}))

		results, err := idx2.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 1})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "shared", results[0].ID)
	})
}
