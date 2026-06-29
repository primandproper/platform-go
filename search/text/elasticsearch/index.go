package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/primandproper/platform-go/v2/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

var (
	// ErrEmptyQueryProvided indicates an empty query was provided as input.
	ErrEmptyQueryProvided = platformerrors.New("empty search query provided")
)

// Index implements our IndexManager interface.
func (sm *indexManager[T]) Index(ctx context.Context, id string, value any) error {
	ctx, op := sm.o11y.Begin(ctx)
	defer op.End()

	if sm.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	op.Set("id", id).Set(keys.IndexNameKey, sm.indexName)
	op.Logger().Debug("adding to index")

	b, err := json.Marshal(value)
	if err != nil {
		return err
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
		return observability.PrepareError(err, op.Span(), "indexing value")
	}

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		sm.circuitBreaker.Failed()
		return observability.PrepareError(platformerrors.New(res.String()), op.Span(), "indexing value")
	}

	sm.circuitBreaker.Succeeded()
	return nil
}

// search executes search queries.
func (sm *indexManager[T]) search(ctx context.Context, query string) (results []*T, err error) {
	_, op := sm.o11y.Begin(ctx)
	defer op.End()

	if sm.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	op.Set(keys.SearchQueryKey, query)

	if query == "" {
		return nil, ErrEmptyQueryProvided
	}

	resultIDs := []*T{}
	q := searchQuery{
		Query: queryContainer{
			Bool: should{
				Should: []condition{},
			},
		},
	}

	queryBody, err := json.Marshal(q)
	if err != nil {
		return nil, observability.PrepareError(err, op.Span(), "encodign search query")
	}

	res, err := sm.esClient.Search(
		sm.esClient.Search.WithIndex(sm.indexName),
		sm.esClient.Search.WithBody(bytes.NewReader(queryBody)),
	)
	defer func() {
		if res != nil {
			if err = res.Body.Close(); err != nil {
				op.Acknowledge(err, "closing response body")
			}
		}
	}()

	if err != nil {
		sm.circuitBreaker.Failed()
		return nil, observability.PrepareError(err, op.Span(), "querying elasticsearch successfully")
	}

	if res.IsError() {
		var e map[string]any
		if err = json.NewDecoder(res.Body).Decode(&e); err != nil {
			sm.circuitBreaker.Failed()
			return nil, observability.PrepareError(err, op.Span(), "invalid response from elasticsearch")
		}

		err = platformerrors.New(strings.Join(res.Warnings(), ", "))
		sm.circuitBreaker.Failed()
		return nil, observability.PrepareError(err, op.Span(), "querying elasticsearch")
	}

	var r esResponse
	if err = json.NewDecoder(res.Body).Decode(&r); err != nil {
		sm.circuitBreaker.Failed()
		return nil, observability.PrepareError(err, op.Span(), "decoding response")
	}

	for _, hit := range r.Hits.Hits {
		var c *T
		if err = json.Unmarshal(hit.Source, &c); err != nil {
			sm.circuitBreaker.Failed()
			return nil, observability.PrepareError(err, op.Span(), "decoding response")
		}
		resultIDs = append(resultIDs, c)
	}

	op.Set(keys.IndexNameKey, sm.indexName).Set(keys.LengthKey, len(resultIDs))

	sm.circuitBreaker.Succeeded()
	return resultIDs, nil
}

// Search implements our IndexManager interface.
func (sm *indexManager[T]) Search(ctx context.Context, query string) (ids []*T, err error) {
	return sm.search(ctx, query)
}

// Wipe implements our IndexManager interface.
func (sm *indexManager[T]) Wipe(_ context.Context) (err error) {
	return platformerrors.New("unimplemented")
}

// Delete implements our IndexManager interface.
func (sm *indexManager[T]) Delete(ctx context.Context, id string) error {
	ctx, op := sm.o11y.Begin(ctx)
	defer op.End()

	if sm.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	op.Set("id", id).Set(keys.IndexNameKey, sm.indexName)

	_, err := esapi.DeleteRequest{
		Index:      sm.indexName,
		DocumentID: id,
	}.Do(ctx, sm.esClient)
	if err != nil {
		sm.circuitBreaker.Failed()
		return observability.PrepareError(err, op.Span(), "deleting from elasticsearch")
	}

	op.Logger().Debug("removed from index")

	sm.circuitBreaker.Succeeded()
	return nil
}
