package pgvector

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v6/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v6/circuitbreaking/config"
	"github.com/primandproper/platform-go/v6/database"
	platformerrors "github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"
	vectorsearch "github.com/primandproper/platform-go/v6/search/vector"
)

const serviceName = "pgvector_index"

// ErrInvalidIdentifier indicates an index or column name does not meet the bare-identifier
// constraint required by this provider.
var ErrInvalidIdentifier = platformerrors.New("identifier must match [A-Za-z_][A-Za-z0-9_]*")

// ErrInvalidFilter indicates QueryRequest.Filter was a non-nil value of a type this
// provider cannot interpret. The pgvector provider only accepts a string SQL fragment;
// any other type is rejected rather than silently ignored, so a caller that mistakenly
// passes (for example) a structured filter meant for another provider gets a loud error
// instead of an unfiltered query that could leak rows across a tenant boundary.
var ErrInvalidFilter = platformerrors.New("pgvector filter must be a string SQL fragment")

// safeIdentifier matches a Postgres identifier safe to use after quoting; we still
// quoteIdent everywhere we interpolate, but we also reject obvious garbage early so
// callers get a clear error rather than a SQL parse failure.
var safeIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type indexManager[T any] struct {
	o11y              observability.Observer
	db                database.Client
	circuitBreaker    circuitbreaking.CircuitBreaker
	upsertCounter     metrics.Int64Counter
	deleteCounter     metrics.Int64Counter
	wipeCounter       metrics.Int64Counter
	queryCounter      metrics.Int64Counter
	errCounter        metrics.Int64Counter
	latencyHist       metrics.Float64Histogram
	indexName         string
	quotedIndex       string
	quotedMetadataCol string
	distanceOperator  string
	indexOpsClass     string
	dimension         int
}

var _ vectorsearch.Index[any] = (*indexManager[any])(nil)

// NewIndex builds a pgvector-backed vectorsearch.Index. It runs an idempotent
// schema migration on construction (CREATE EXTENSION + CREATE TABLE + CREATE INDEX)
// so the table for indexName is guaranteed to exist after the constructor returns.
func NewIndex[T any](
	ctx context.Context,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	cfg *Config,
	db database.Client,
	indexName string,
	cb circuitbreaking.CircuitBreaker,
) (vectorsearch.Index[T], error) {
	if cfg == nil {
		return nil, vectorsearch.ErrNilConfig
	}
	if db == nil {
		return nil, vectorsearch.ErrNilDatabaseClient
	}
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating pgvector config")
	}
	if !safeIdentifier.MatchString(indexName) {
		return nil, platformerrors.Wrapf(ErrInvalidIdentifier, "index name %q", indexName)
	}
	metaCol := cfg.MetadataColumn
	if metaCol == "" {
		metaCol = "metadata"
	}
	if !safeIdentifier.MatchString(metaCol) {
		return nil, platformerrors.Wrapf(ErrInvalidIdentifier, "metadata column %q", metaCol)
	}

	op, ops, err := operatorAndOpClass(cfg.Metric)
	if err != nil {
		return nil, err
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	upsertCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_upserts", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating upsert counter")
	}
	deleteCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_deletes", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating delete counter")
	}
	wipeCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_wipes", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating wipe counter")
	}
	queryCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_queries", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating query counter")
	}
	errCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}
	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	im := &indexManager[T]{
		o11y:              observability.NewObserver(fmt.Sprintf("%s_%s", serviceName, indexName), logger, tracerProvider),
		db:                db,
		circuitBreaker:    circuitbreakingcfg.EnsureCircuitBreaker(cb),
		upsertCounter:     upsertCounter,
		deleteCounter:     deleteCounter,
		wipeCounter:       wipeCounter,
		queryCounter:      queryCounter,
		errCounter:        errCounter,
		latencyHist:       latencyHist,
		indexName:         indexName,
		quotedIndex:       quoteIdent(indexName),
		quotedMetadataCol: quoteIdent(metaCol),
		distanceOperator:  op,
		indexOpsClass:     ops,
		dimension:         cfg.Dimension,
	}

	if migrateErr := im.ensureTable(ctx); migrateErr != nil {
		return nil, migrateErr
	}

	return im, nil
}

// operatorAndOpClass returns the pgvector operator and the index ops class for the
// chosen distance metric.
func operatorAndOpClass(metric vectorsearch.DistanceMetric) (op, opsClass string, err error) {
	switch metric {
	case vectorsearch.DistanceCosine:
		return "<=>", "vector_cosine_ops", nil
	case vectorsearch.DistanceDotProduct:
		return "<#>", "vector_ip_ops", nil
	case vectorsearch.DistanceEuclidean:
		return "<->", "vector_l2_ops", nil
	default:
		return "", "", platformerrors.Wrapf(vectorsearch.ErrInvalidMetric, "metric %q", metric)
	}
}

// ensureSchemaLockKey is the constant int64 used for a Postgres transaction-scoped
// advisory lock around schema migrations. Without serialization, concurrent calls to
// CREATE EXTENSION IF NOT EXISTS race against themselves: Postgres' existence check
// is not atomic with the catalog insert, so racing transactions can both pass the
// check and one of them then collides on pg_extension_name_index. The constant value
// is arbitrary but must be stable across processes that share a database.
const ensureSchemaLockKey int64 = 0x7067766563746f72 // "pgvector" as ASCII

// ensureTable runs the idempotent schema migration. It is safe to call repeatedly
// and concurrently — concurrent callers serialize via a transaction-scoped advisory
// lock so they observe each other's CREATE EXTENSION as already-done.
func (i *indexManager[T]) ensureTable(ctx context.Context) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.indexName)

	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (
				id text PRIMARY KEY,
				embedding vector(%d) NOT NULL,
				%s jsonb NOT NULL DEFAULT '{}'::jsonb
			)`,
			i.quotedIndex, i.dimension, i.quotedMetadataCol,
		),
		fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (embedding %s)`,
			quoteIdent(i.indexName+"_embedding_idx"), i.quotedIndex, i.indexOpsClass,
		),
	}

	// The advisory lock is transaction-scoped, so concurrent callers serialize until this
	// transaction commits or rolls back — both released for us by WithTransaction.
	if err := i.db.WithTransaction(ctx, func(tx database.SQLQueryExecutor) error {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, ensureSchemaLockKey); err != nil {
			return op.Error(err, "acquiring pgvector schema advisory lock")
		}

		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return op.Error(err, "ensuring pgvector schema (%s)", firstWords(stmt))
			}
		}

		return nil
	}); err != nil {
		i.errCounter.Add(ctx, 1)
		return err
	}

	return nil
}

// Upsert implements vectorsearch.Index.
func (i *indexManager[T]) Upsert(ctx context.Context, vectors ...vectorsearch.Vector[T]) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.indexName)
	op.Set(keys.LengthKey, len(vectors))

	if len(vectors) == 0 {
		return nil
	}
	if i.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	// Validate dimensions and prepare per-row payloads up front so we don't open a
	// transaction we then have to roll back.
	type row struct {
		id        string
		embedding string
		payload   []byte
	}
	rows := make([]row, 0, len(vectors))
	for n := range vectors {
		v := vectors[n]
		if v.ID == "" {
			i.errCounter.Add(ctx, 1)
			return platformerrors.ErrInvalidIDProvided
		}
		if len(v.Embedding) == 0 {
			i.errCounter.Add(ctx, 1)
			return vectorsearch.ErrEmptyEmbedding
		}
		if len(v.Embedding) != i.dimension {
			i.errCounter.Add(ctx, 1)
			return platformerrors.Wrapf(vectorsearch.ErrDimensionMismatch, "got %d, want %d", len(v.Embedding), i.dimension)
		}
		payload, err := marshalMetadata(v.Metadata)
		if err != nil {
			i.errCounter.Add(ctx, 1)
			return op.Error(err, "marshaling metadata for id %q", v.ID)
		}
		rows = append(rows, row{
			id:        v.ID,
			embedding: encodeVector(v.Embedding),
			payload:   payload,
		})
	}

	// The only interpolated values are internally-quoted identifiers (quotedIndex/
	// quotedMetadataCol), not user input; row values are bound as parameters.
	stmt := fmt.Sprintf(
		`INSERT INTO %s (id, embedding, %s) VALUES ($1, $2::vector, $3::jsonb)
		 ON CONFLICT (id) DO UPDATE SET embedding = EXCLUDED.embedding, %s = EXCLUDED.%s`,
		i.quotedIndex, i.quotedMetadataCol, i.quotedMetadataCol, i.quotedMetadataCol,
	)

	// Run the whole batch in one transaction so a mid-batch failure rolls back the
	// rows already written rather than leaving a partial batch committed.
	if err := i.db.WithTransaction(ctx, func(tx database.SQLQueryExecutor) error {
		for n := range rows {
			r := &rows[n]
			if _, err := tx.ExecContext(ctx, stmt, r.id, r.embedding, r.payload); err != nil {
				return op.Error(err, "upserting vector %q", r.id)
			}
		}
		return nil
	}); err != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return err
	}

	i.upsertCounter.Add(ctx, int64(len(rows)))
	i.circuitBreaker.Succeeded()
	return nil
}

// Delete implements vectorsearch.Index.
func (i *indexManager[T]) Delete(ctx context.Context, ids ...string) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.indexName)
	op.Set(keys.LengthKey, len(ids))

	if len(ids) == 0 {
		return nil
	}
	if i.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	stmt := fmt.Sprintf(`DELETE FROM %s WHERE id = ANY($1)`, i.quotedIndex)
	if _, err := i.db.Writer().ExecContext(ctx, stmt, pgTextArray(ids)); err != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(err, "deleting vectors")
	}

	i.deleteCounter.Add(ctx, int64(len(ids)))
	i.circuitBreaker.Succeeded()
	return nil
}

// Wipe implements vectorsearch.Index.
func (i *indexManager[T]) Wipe(ctx context.Context) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.indexName)

	if i.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	stmt := fmt.Sprintf(`TRUNCATE TABLE %s`, i.quotedIndex)
	if _, err := i.db.Writer().ExecContext(ctx, stmt); err != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(err, "wiping pgvector index")
	}

	i.wipeCounter.Add(ctx, 1)
	i.circuitBreaker.Succeeded()
	return nil
}

// Query implements vectorsearch.Index.
func (i *indexManager[T]) Query(ctx context.Context, req vectorsearch.QueryRequest) ([]vectorsearch.QueryResult[T], error) {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.indexName)

	if len(req.Embedding) == 0 {
		return nil, vectorsearch.ErrEmptyEmbedding
	}
	if len(req.Embedding) != i.dimension {
		return nil, platformerrors.Wrapf(vectorsearch.ErrDimensionMismatch, "got %d, want %d", len(req.Embedding), i.dimension)
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}
	op.Set(keys.FilterLimitKey, req.TopK)
	if i.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	where := ""
	if req.Filter != nil {
		filterFragment, ok := req.Filter.(string)
		if !ok {
			return nil, platformerrors.Wrapf(ErrInvalidFilter, "got %T", req.Filter)
		}
		if trimmed := strings.TrimSpace(filterFragment); trimmed != "" {
			// SECURITY: the filter is concatenated verbatim into the WHERE clause — it is
			// raw, unparameterized SQL executed against the write-capable read connection.
			// It is a trusted, caller-supplied SQL fragment, NEVER end-user input. Callers
			// MUST sanitize/whitelist anything they interpolate (including tenant scoping)
			// before passing it here. See the package doc for the filter contract. A
			// parameterized builder is intentionally not used because the shared
			// vectorsearch.QueryRequest carries no args slice; adding one would change the
			// cross-provider interface, which this fix deliberately avoids.
			where = " WHERE " + trimmed
		}
	}

	stmt := fmt.Sprintf(
		`SELECT id, %s, embedding %s $1::vector AS distance FROM %s%s ORDER BY distance ASC LIMIT $2`,
		i.quotedMetadataCol, i.distanceOperator, i.quotedIndex, where,
	)

	rows, err := i.db.Reader().QueryContext(ctx, stmt, encodeVector(req.Embedding), req.TopK)
	if err != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return nil, op.Error(err, "querying pgvector")
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "closing pgvector query rows")
		}
	}()

	var results []vectorsearch.QueryResult[T]
	for rows.Next() {
		var (
			id      string
			rawMeta []byte
			dist    float64
		)
		if scanErr := rows.Scan(&id, &rawMeta, &dist); scanErr != nil {
			i.errCounter.Add(ctx, 1)
			i.circuitBreaker.Failed()
			return nil, op.Error(scanErr, "scanning pgvector row")
		}

		meta, unmarshalErr := unmarshalMetadata[T](rawMeta)
		if unmarshalErr != nil {
			i.errCounter.Add(ctx, 1)
			i.circuitBreaker.Failed()
			return nil, op.Error(unmarshalErr, "decoding pgvector metadata")
		}

		results = append(results, vectorsearch.QueryResult[T]{
			ID:       id,
			Metadata: meta,
			Distance: float32(dist),
		})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return nil, op.Error(rowsErr, "iterating pgvector rows")
	}

	op.Set(keys.LengthKey, len(results))

	i.queryCounter.Add(ctx, 1)
	i.circuitBreaker.Succeeded()
	return results, nil
}

// encodeVector formats a []float32 as a pgvector text literal: [1.5,2.5,3.5].
func encodeVector(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for n, f := range v {
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// pgTextArray formats a []string as a Postgres text[] literal: {a,b,"c with comma"}.
// Use only for ANY($1) ID lookups where IDs are caller-supplied strings.
func pgTextArray(ids []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for n, id := range ids {
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(id, `\`, `\\`), `"`, `\"`))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// marshalMetadata returns a JSON-encoded representation of the metadata payload.
// nil payloads round-trip as the empty object so the column NOT NULL constraint
// is satisfied without forcing callers to construct one.
func marshalMetadata[T any](metadata *T) ([]byte, error) {
	if metadata == nil {
		return []byte(`{}`), nil
	}
	return json.Marshal(metadata)
}

// unmarshalMetadata decodes a JSON byte slice into a *T. Empty/null payloads return
// nil so callers can distinguish "no metadata" from a populated struct.
//
//nolint:nilnil // (nil, nil) is the documented "no metadata" signal; callers rely on it
func unmarshalMetadata[T any](data []byte) (*T, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var t T
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// quoteIdent safely wraps a Postgres identifier in double-quotes, doubling any
// embedded double-quotes per the SQL spec.
func quoteIdent(id string) string {
	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}

// firstWords returns the first few words of a SQL statement for use in error
// messages, so we don't dump multi-line CREATE statements into log lines.
func firstWords(stmt string) string {
	stmt = strings.TrimSpace(stmt)
	if idx := strings.IndexAny(stmt, " \t\n"); idx > 0 {
		next := strings.IndexAny(stmt[idx+1:], " \t\n")
		if next > 0 {
			return stmt[:idx+1+next]
		}
	}
	return stmt
}
