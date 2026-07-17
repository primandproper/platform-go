package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v5/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v5/circuitbreaking/config"
	platformerrors "github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/keys"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	vectorsearch "github.com/primandproper/platform-go/v5/search/vector"
)

const serviceName = "qdrant_index"

// ErrUnexpectedStatus indicates qdrant returned a non-2xx response.
var ErrUnexpectedStatus = platformerrors.New("qdrant returned an unexpected status code")

// ErrUnsupportedID indicates a point ID is neither an unsigned integer nor a UUID —
// the only two forms qdrant accepts. The platform's identifiers.New() (an xid) is one
// such rejected form: pgvector stores it fine, but qdrant would 4xx. We reject it up
// front so callers get a clear error instead of a late, opaque server failure.
var ErrUnsupportedID = platformerrors.New("qdrant point id must be an unsigned integer or a UUID")

// uuidPattern matches the canonical hyphenated UUID form qdrant accepts as a string ID.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// pointID validates a caller-supplied ID against the two forms qdrant accepts for a
// point ID and returns the JSON value to send. Numeric IDs are returned as a uint64 so
// they serialize as a JSON number — qdrant rejects a numeric string like "42" — while
// UUIDs are returned unchanged. Anything else (e.g. an xid) is rejected up front.
func pointID(id string) (any, error) {
	if id == "" {
		return nil, platformerrors.ErrInvalidIDProvided
	}
	if n, parseErr := strconv.ParseUint(id, 10, 64); parseErr == nil {
		return n, nil
	}
	if uuidPattern.MatchString(id) {
		return id, nil
	}
	return nil, platformerrors.Wrapf(ErrUnsupportedID, "id %q", id)
}

// scoreToDistance converts a qdrant similarity score into a distance consistent with
// the vectorsearch.QueryResult.Distance contract, where LOWER means more similar (the
// same convention the pgvector provider follows). Qdrant reports Cosine and Dot as
// similarities (higher = closer), whereas Euclid/Manhattan are already distances
// (lower = closer), so only the similarity metrics are inverted:
//   - Cosine: 1 - score  (score in [-1,1] → distance in [0,2], mirroring pgvector's <=>)
//   - Dot:    -score      (mirroring pgvector's <#> negative inner product)
//   - Euclid: score       (already a distance)
//
// distance is the qdrant distance string produced by metricToDistance.
func scoreToDistance(distance string, score float32) float32 {
	switch distance {
	case "Cosine":
		return 1 - score
	case "Dot":
		return -score
	default:
		return score
	}
}

type indexManager[T any] struct {
	o11y           observability.Observer
	httpClient     *http.Client
	circuitBreaker circuitbreaking.CircuitBreaker
	upsertCounter  metrics.Int64Counter
	deleteCounter  metrics.Int64Counter
	wipeCounter    metrics.Int64Counter
	queryCounter   metrics.Int64Counter
	errCounter     metrics.Int64Counter
	latencyHist    metrics.Float64Histogram
	baseURL        string
	apiKey         string
	collection     string
	distance       string
	dimension      int
}

var _ vectorsearch.Index[any] = (*indexManager[any])(nil)

// NewIndex builds a qdrant-backed vectorsearch.Index. The constructor performs
// an idempotent collection-creation step (PUT /collections/{name}); existing
// collections with the same name and shape are left untouched.
func NewIndex[T any](
	ctx context.Context,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	cfg *Config,
	collection string,
	cb circuitbreaking.CircuitBreaker,
) (vectorsearch.Index[T], error) {
	if cfg == nil {
		return nil, vectorsearch.ErrNilConfig
	}
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating qdrant config")
	}
	if collection == "" {
		return nil, platformerrors.ErrEmptyInputProvided
	}
	distance, err := metricToDistance(cfg.Metric)
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

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	im := &indexManager[T]{
		o11y:           observability.NewObserver(fmt.Sprintf("%s_%s", serviceName, collection), logger, tracerProvider),
		httpClient:     &http.Client{Timeout: timeout},
		circuitBreaker: circuitbreakingcfg.EnsureCircuitBreaker(cb),
		upsertCounter:  upsertCounter,
		deleteCounter:  deleteCounter,
		wipeCounter:    wipeCounter,
		queryCounter:   queryCounter,
		errCounter:     errCounter,
		latencyHist:    latencyHist,
		baseURL:        strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:         cfg.APIKey,
		collection:     collection,
		distance:       distance,
		dimension:      cfg.Dimension,
	}

	if ensureErr := im.ensureCollection(ctx); ensureErr != nil {
		return nil, ensureErr
	}

	return im, nil
}

func metricToDistance(m vectorsearch.DistanceMetric) (string, error) {
	switch m {
	case vectorsearch.DistanceCosine:
		return "Cosine", nil
	case vectorsearch.DistanceDotProduct:
		return "Dot", nil
	case vectorsearch.DistanceEuclidean:
		return "Euclid", nil
	default:
		return "", platformerrors.Wrapf(vectorsearch.ErrInvalidMetric, "metric %q", m)
	}
}

// ensureCollection creates the collection if it does not exist. PUT /collections/{name}
// is idempotent in qdrant when the body matches the existing collection.
func (i *indexManager[T]) ensureCollection(ctx context.Context) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.collection)

	exists, err := i.collectionExists(ctx)
	if err != nil {
		i.errCounter.Add(ctx, 1)
		return op.Error(err, "checking qdrant collection")
	}
	if exists {
		return nil
	}

	body := map[string]any{
		"vectors": map[string]any{
			"size":     i.dimension,
			"distance": i.distance,
		},
	}
	status, respBody, err := i.jsonReq(ctx, http.MethodPut, i.collectionPath(""), body)
	if err != nil {
		i.errCounter.Add(ctx, 1)
		return op.Error(err, "creating qdrant collection")
	}
	if status/100 != 2 {
		i.errCounter.Add(ctx, 1)
		return op.Error(wrapStatusError(status, respBody), "creating qdrant collection")
	}
	return nil
}

func (i *indexManager[T]) collectionExists(ctx context.Context) (bool, error) {
	status, body, err := i.jsonReq(ctx, http.MethodGet, i.collectionPath(""), nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, wrapStatusError(status, body)
	}
}

// Upsert implements vectorsearch.Index.
func (i *indexManager[T]) Upsert(ctx context.Context, vectors ...vectorsearch.Vector[T]) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.collection)
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

	type point struct {
		Payload any       `json:"payload,omitempty"`
		ID      any       `json:"id"`
		Vector  []float32 `json:"vector"`
	}

	points := make([]point, 0, len(vectors))
	for n := range vectors {
		v := vectors[n]
		pid, idErr := pointID(v.ID)
		if idErr != nil {
			i.errCounter.Add(ctx, 1)
			return idErr
		}
		if len(v.Embedding) == 0 {
			i.errCounter.Add(ctx, 1)
			return vectorsearch.ErrEmptyEmbedding
		}
		if len(v.Embedding) != i.dimension {
			i.errCounter.Add(ctx, 1)
			return platformerrors.Wrapf(vectorsearch.ErrDimensionMismatch, "got %d, want %d", len(v.Embedding), i.dimension)
		}
		points = append(points, point{
			ID:      pid,
			Vector:  v.Embedding,
			Payload: payloadFromMetadata(v.Metadata),
		})
	}

	body := map[string]any{"points": points}
	status, respBody, err := i.jsonReq(ctx, http.MethodPut, i.collectionPath("/points?wait=true"), body)
	if err != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(err, "upserting qdrant points")
	}
	if status/100 != 2 {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(wrapStatusError(status, respBody), "upserting qdrant points")
	}

	i.upsertCounter.Add(ctx, int64(len(points)))
	i.circuitBreaker.Succeeded()
	return nil
}

// Delete implements vectorsearch.Index.
func (i *indexManager[T]) Delete(ctx context.Context, ids ...string) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.collection)
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

	points := make([]any, 0, len(ids))
	for _, id := range ids {
		pid, idErr := pointID(id)
		if idErr != nil {
			i.errCounter.Add(ctx, 1)
			return idErr
		}
		points = append(points, pid)
	}

	body := map[string]any{"points": points}
	status, respBody, err := i.jsonReq(ctx, http.MethodPost, i.collectionPath("/points/delete?wait=true"), body)
	if err != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(err, "deleting qdrant points")
	}
	if status/100 != 2 {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(wrapStatusError(status, respBody), "deleting qdrant points")
	}

	i.deleteCounter.Add(ctx, int64(len(ids)))
	i.circuitBreaker.Succeeded()
	return nil
}

// Wipe implements vectorsearch.Index. Qdrant has no native "delete all points"
// operation that doesn't require a non-empty filter, so we drop and recreate the
// collection. This is faster than scrolling all IDs and batching deletes, and is
// atomic from the caller's perspective since they hold the only handle to the
// collection name.
func (i *indexManager[T]) Wipe(ctx context.Context) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.collection)

	if i.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	status, respBody, err := i.jsonReq(ctx, http.MethodDelete, i.collectionPath(""), nil)
	if err != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(err, "dropping qdrant collection")
	}
	if status/100 != 2 && status != http.StatusNotFound {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(wrapStatusError(status, respBody), "dropping qdrant collection")
	}

	if recreateErr := i.ensureCollection(ctx); recreateErr != nil {
		i.circuitBreaker.Failed()
		return recreateErr
	}

	i.wipeCounter.Add(ctx, 1)
	i.circuitBreaker.Succeeded()
	return nil
}

// Query implements vectorsearch.Index.
func (i *indexManager[T]) Query(ctx context.Context, req vectorsearch.QueryRequest) ([]vectorsearch.QueryResult[T], error) {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.IndexNameKey, i.collection)

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

	body := map[string]any{
		"vector":       req.Embedding,
		"limit":        req.TopK,
		"with_payload": true,
		"with_vector":  false,
	}
	if req.Filter != nil {
		body["filter"] = req.Filter
		op.Set(keys.SearchQueryKey, req.Filter)
	}

	status, respBody, err := i.jsonReq(ctx, http.MethodPost, i.collectionPath("/points/search"), body)
	if err != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return nil, op.Error(err, "qdrant search request")
	}
	if status/100 != 2 {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return nil, op.Error(wrapStatusError(status, respBody), "qdrant search request")
	}

	var decoded struct {
		Result []struct {
			ID      any             `json:"id"`
			Payload json.RawMessage `json:"payload"`
			Score   float32         `json:"score"`
		} `json:"result"`
	}
	if decodeErr := json.Unmarshal(respBody, &decoded); decodeErr != nil {
		i.errCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return nil, op.Error(decodeErr, "decoding qdrant response")
	}

	results := make([]vectorsearch.QueryResult[T], 0, len(decoded.Result))
	for n := range decoded.Result {
		r := &decoded.Result[n]
		idStr, idErr := stringifyID(r.ID)
		if idErr != nil {
			i.errCounter.Add(ctx, 1)
			i.circuitBreaker.Failed()
			return nil, op.Error(idErr, "decoding qdrant point id")
		}
		meta, unmarshalErr := unmarshalPayload[T](r.Payload)
		if unmarshalErr != nil {
			i.errCounter.Add(ctx, 1)
			i.circuitBreaker.Failed()
			return nil, op.Error(unmarshalErr, "decoding qdrant payload")
		}
		results = append(results, vectorsearch.QueryResult[T]{
			ID:       idStr,
			Metadata: meta,
			Distance: scoreToDistance(i.distance, r.Score),
		})
	}

	op.Set(keys.LengthKey, len(results))

	i.queryCounter.Add(ctx, 1)
	i.circuitBreaker.Succeeded()
	return results, nil
}

// maxResponseBytes caps the response body size we will read into memory. Qdrant
// query responses are bounded by TopK and metadata payload size, so 10MB is well
// above any reasonable result set.
const maxResponseBytes = 10 * 1024 * 1024

// jsonReq makes a JSON HTTP request and returns the status code and response body
// bytes. The response body is always closed before returning. Transport, marshal,
// and read errors are returned; HTTP status errors are NOT — callers inspect the
// returned status code themselves so they can distinguish 404 from other failures.
func (i *indexManager[T]) jsonReq(ctx context.Context, method, fullURL string, in any) (httpStatus int, respBody []byte, requestErr error) {
	var reader io.Reader
	if in != nil {
		buf, marshalErr := json.Marshal(in)
		if marshalErr != nil {
			return 0, nil, platformerrors.Wrap(marshalErr, "marshaling qdrant request body")
		}
		reader = bytes.NewReader(buf)
	}
	req, reqErr := http.NewRequestWithContext(ctx, method, fullURL, reader)
	if reqErr != nil {
		return 0, nil, platformerrors.Wrap(reqErr, "constructing qdrant request")
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if i.apiKey != "" {
		req.Header.Set("api-key", i.apiKey)
	}

	// URL is constructed from operator-controlled config; not user input.
	resp, doErr := i.httpClient.Do(req) //nolint:gosec // SSRF false positive — URL is from trusted config
	if doErr != nil {
		return 0, nil, platformerrors.Wrap(doErr, "executing qdrant request")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			i.o11y.Logger().Error("closing qdrant response body", closeErr)
		}
	}()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if readErr != nil {
		return resp.StatusCode, nil, platformerrors.Wrap(readErr, "reading qdrant response")
	}
	return resp.StatusCode, body, nil
}

// wrapStatusError formats a qdrant non-2xx status into a wrapped sentinel error.
func wrapStatusError(status int, body []byte) error {
	return platformerrors.Wrapf(ErrUnexpectedStatus, "status=%d body=%s", status, strings.TrimSpace(string(body)))
}

func (i *indexManager[T]) collectionPath(suffix string) string {
	return i.baseURL + "/collections/" + url.PathEscape(i.collection) + suffix
}

// payloadFromMetadata returns the value to send as the qdrant point payload, or nil
// if the metadata pointer is nil. JSON encoding handles the rest.
func payloadFromMetadata[T any](metadata *T) any {
	if metadata == nil {
		return nil
	}
	return metadata
}

// unmarshalPayload decodes a qdrant payload (raw JSON object) into *T. Empty/null
// payloads return nil so callers can distinguish "no payload" from a populated one.
//
//nolint:nilnil // (nil, nil) is the documented "no payload" signal; callers rely on it
func unmarshalPayload[T any](data json.RawMessage) (*T, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var t T
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// stringifyID converts a qdrant point ID (which is either a number or a string in
// the JSON response) to its string form.
func stringifyID(raw any) (string, error) {
	switch v := raw.(type) {
	case string:
		return v, nil
	case float64:
		// Qdrant point IDs are integers; format without scientific notation so a large
		// ID like 10000000 round-trips as "10000000" rather than fmt's "%v" → "1e+07",
		// which would target a different point on a later Delete/Upsert.
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case json.Number:
		return v.String(), nil
	default:
		return "", platformerrors.Wrapf(platformerrors.ErrEmptyInputProvided, "unexpected qdrant id type %T", raw)
	}
}
