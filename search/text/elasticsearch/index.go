package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
	textsearch "github.com/primandproper/platform-go/v13/search/text"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

const (
	// backendName tags cursors this package issues, so one cannot be resumed
	// against a different backend.
	backendName = "elasticsearch"

	// maxSearchLimit caps a single page. Elasticsearch will serve more, but a
	// page this size is already past the point where a caller wants one.
	maxSearchLimit = 200

	// maxResultWindow mirrors Elasticsearch's index.max_result_window default.
	// from + size beyond it is rejected by the server, so pagination stops here
	// with an error a caller can recognize rather than a 500 from the cluster.
	maxResultWindow = 10000
)

// Index implements textsearch.Index.
func (sm *IndexManager[T]) Index(ctx context.Context, id string, value any) (err error) {
	ctx, op := sm.o11y.Begin(ctx)
	defer op.End()

	started := time.Now()
	defer func() { sm.instruments.Record(ctx, textsearch.OperationIndex, started, err) }()

	if sm.circuitBreaker.CannotProceed() {
		return op.Error(circuitbreaking.ErrCircuitBroken, "indexing value")
	}

	op.Set("id", id)
	op.Logger().Debug("adding to index")

	b, err := json.Marshal(value)
	if err != nil {
		return op.Error(err, "encoding value for indexing")
	}

	res, err := esapi.IndexRequest{
		Index:               sm.indexName,
		DocumentID:          id,
		Body:                bytes.NewReader(b),
		Timeout:             sm.indexOperationTimeout,
		Version:             nil,
		VersionType:         "",
		WaitForActiveShards: "",
		Pretty:              false,
		Human:               false,
		ErrorTrace:          false,
		FilterPath:          nil,
		Header:              nil,
	}.Do(ctx, sm.esClient)
	if err != nil {
		sm.circuitBreaker.Failed()
		return op.Error(err, "indexing value")
	}
	defer func() {
		if closeErr := res.Body.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "closing response body")
		}
	}()

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		sm.circuitBreaker.Failed()
		return op.Error(platformerrors.New(res.String()), "indexing value")
	}

	sm.circuitBreaker.Succeeded()
	return nil
}

// search executes search queries.
func (sm *IndexManager[T]) search(ctx context.Context, req textsearch.SearchRequest) (_ *textsearch.SearchResults[T], err error) {
	ctx, op := sm.o11y.Begin(ctx)
	defer op.End()

	started := time.Now()
	defer func() { sm.instruments.Record(ctx, textsearch.OperationSearch, started, err) }()

	if sm.circuitBreaker.CannotProceed() {
		return nil, op.Error(circuitbreaking.ErrCircuitBroken, "searching index")
	}

	op.Set(keys.SearchQueryKey, req.Query)

	if req.Query == "" {
		return nil, op.Error(textsearch.ErrEmptyQueryProvided, "searching index")
	}

	from, err := textsearch.DecodeCursor(backendName, req.Cursor)
	if err != nil {
		return nil, op.Error(err, "decoding cursor")
	}

	size := textsearch.EffectiveLimit(req.Limit, maxSearchLimit)

	// from + size cannot exceed index.max_result_window (10,000 by default);
	// past that Elasticsearch rejects the request outright rather than
	// returning a short page, so the refusal is raised here with a name. Deep
	// paging past it needs search_after or a PIT, which this backend does not
	// use yet — the opaque cursor exists so adding them will not change the
	// interface.
	if from+size > maxResultWindow {
		return nil, op.Error(textsearch.ErrResultWindowExceeded, "paginating beyond the result window")
	}

	op.Set("search.from", from).Set("search.size", size)

	resultIDs := []*T{}
	q := searchQuery{
		Query: queryContainer{
			MultiMatch: multiMatchQuery{
				Query:  req.Query,
				Type:   "best_fields",
				Fields: []string{"*"},
			},
		},
		From: from,
		Size: size,
	}

	queryBody, err := json.Marshal(q)
	if err != nil {
		return nil, op.Error(err, "encodign search query")
	}

	res, err := sm.esClient.Search(
		sm.esClient.Search.WithContext(ctx),
		sm.esClient.Search.WithIndex(sm.indexName),
		sm.esClient.Search.WithBody(bytes.NewReader(queryBody)),
	)
	defer func() {
		if res != nil {
			if closeErr := res.Body.Close(); closeErr != nil {
				op.Acknowledge(closeErr, "closing response body")
			}
		}
	}()

	if err != nil {
		sm.circuitBreaker.Failed()
		return nil, op.Error(err, "querying elasticsearch successfully")
	}

	if res.IsError() {
		sm.circuitBreaker.Failed()

		// The body is where Elasticsearch says what was wrong with the query —
		// which field does not exist, which shard failed — and it used to be
		// decoded and thrown away in favor of res.Warnings(), a slice that is
		// empty on almost every error. What came back was an error whose whole
		// message was the empty string.
		return nil, op.Error(decodeErrorBody(res.Body, res.Status()), "querying elasticsearch")
	}

	var r esResponse
	if err = json.NewDecoder(res.Body).Decode(&r); err != nil {
		sm.circuitBreaker.Failed()
		return nil, op.Error(err, "decoding response")
	}

	for _, hit := range r.Hits.Hits {
		var c *T
		if err = json.Unmarshal(hit.Source, &c); err != nil {
			sm.circuitBreaker.Failed()
			return nil, op.Error(err, "decoding response")
		}
		resultIDs = append(resultIDs, c)
	}

	op.Set(keys.LengthKey, len(resultIDs))

	out := &textsearch.SearchResults[T]{Hits: resultIDs}

	// The next cursor is issued from the total, not from the page being short:
	// Elasticsearch can return fewer hits than requested and still have more,
	// so a short page is not the end of the result set.
	if next := from + len(resultIDs); len(resultIDs) > 0 && next < r.Hits.Total.Value && next < maxResultWindow {
		if out.NextCursor, err = textsearch.EncodeCursor(backendName, next); err != nil {
			return nil, op.Error(err, "encoding next cursor")
		}
	}

	sm.circuitBreaker.Succeeded()

	return out, nil
}

// Search implements our IndexSearcher interface.
func (sm *IndexManager[T]) Search(ctx context.Context, req textsearch.SearchRequest) (*textsearch.SearchResults[T], error) {
	return sm.search(ctx, req)
}

// errorResponse is the shape Elasticsearch reports a rejected request in.
type errorResponse struct {
	Error struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	} `json:"error"`
}

// decodeErrorBody turns an error response into an error that says what
// Elasticsearch objected to.
//
// The status line alone does not: a 400 covers a malformed query, an unmapped
// field, and a page past max_result_window, and only the body distinguishes
// them. A body that will not decode is reported as such rather than swallowed,
// because "the cluster answered something this client cannot read" is itself
// worth knowing.
func decodeErrorBody(body io.Reader, status string) error {
	var decoded errorResponse
	if err := json.NewDecoder(body).Decode(&decoded); err != nil {
		return platformerrors.Wrapf(err, "elasticsearch responded %s with an undecodable body", status)
	}

	switch {
	case decoded.Error.Type != "" && decoded.Error.Reason != "":
		return platformerrors.Newf("elasticsearch responded %s: %s: %s", status, decoded.Error.Type, decoded.Error.Reason)
	case decoded.Error.Reason != "":
		return platformerrors.Newf("elasticsearch responded %s: %s", status, decoded.Error.Reason)
	default:
		return platformerrors.Newf("elasticsearch responded %s", status)
	}
}

// Wipe implements textsearch.Index. It removes all documents from the
// index, leaving the index itself in place (matching the algolia/pgvector/qdrant
// backends), via a match-all delete-by-query with an immediate refresh.
func (sm *IndexManager[T]) Wipe(ctx context.Context) (err error) {
	ctx, op := sm.o11y.Begin(ctx)
	defer op.End()

	started := time.Now()
	defer func() { sm.instruments.Record(ctx, textsearch.OperationWipe, started, err) }()

	if sm.circuitBreaker.CannotProceed() {
		return op.Error(circuitbreaking.ErrCircuitBroken, "wiping index")
	}

	refresh := true

	res, err := esapi.DeleteByQueryRequest{
		Index:   []string{sm.indexName},
		Body:    strings.NewReader(`{"query":{"match_all":{}}}`),
		Refresh: &refresh,
	}.Do(ctx, sm.esClient)
	if err != nil {
		sm.circuitBreaker.Failed()
		return op.Error(err, "wiping index")
	}
	defer func() {
		if closeErr := res.Body.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "closing response body")
		}
	}()

	if res.IsError() {
		sm.circuitBreaker.Failed()
		return op.Error(platformerrors.New(res.String()), "wiping index")
	}

	sm.circuitBreaker.Succeeded()
	return nil
}

// Delete implements textsearch.Index.
func (sm *IndexManager[T]) Delete(ctx context.Context, id string) (err error) {
	ctx, op := sm.o11y.Begin(ctx)
	defer op.End()

	started := time.Now()
	defer func() { sm.instruments.Record(ctx, textsearch.OperationDelete, started, err) }()

	if sm.circuitBreaker.CannotProceed() {
		return op.Error(circuitbreaking.ErrCircuitBroken, "removing from index")
	}

	op.Set("id", id)

	res, err := esapi.DeleteRequest{
		Index:      sm.indexName,
		DocumentID: id,
	}.Do(ctx, sm.esClient)
	if err != nil {
		sm.circuitBreaker.Failed()
		return op.Error(err, "deleting from elasticsearch")
	}
	defer func() {
		if closeErr := res.Body.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "closing response body")
		}
	}()

	// A delete targeting a document that does not exist returns 404 with
	// result "not_found". Treat that as success: the desired end state (document
	// absent) already holds, so Delete is idempotent for callers that retry or
	// delete speculatively.
	if res.StatusCode == http.StatusNotFound {
		op.Logger().Debug("document not found, treating delete as no-op")
		sm.circuitBreaker.Succeeded()
		return nil
	}

	// esapi only returns a non-nil err for transport-level failures; an HTTP error
	// status (401/500) surfaces on the response itself. Without this check a
	// failed delete would count as a success and leave the document in place.
	if res.IsError() {
		sm.circuitBreaker.Failed()
		return op.Error(platformerrors.New(res.String()), "deleting from elasticsearch")
	}

	op.Logger().Debug("removed from index")

	sm.circuitBreaker.Succeeded()
	return nil
}
