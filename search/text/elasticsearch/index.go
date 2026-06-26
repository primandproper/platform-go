package elasticsearch

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/primandproper/platform-go/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

var (
	// ErrEmptyQueryProvided indicates an empty query was provided as input.
	ErrEmptyQueryProvided = platformerrors.New("empty search query provided")
)

// Index implements our IndexManager interface.
func (sm *indexManager[T]) Index(ctx context.Context, id string, value any) error {
	_, span := sm.tracer.StartSpan(ctx)
	defer span.End()

	if sm.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	logger := sm.logger.WithValue("id", id).WithValue("value", value)
	logger.Debug("adding to index")

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
		return observability.PrepareError(err, span, "indexing value")
	}

	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusOK {
		sm.circuitBreaker.Failed()
		return observability.PrepareError(platformerrors.New(res.String()), span, "indexing value")
	}

	sm.circuitBreaker.Succeeded()
	return nil
}

// search executes search queries.
func (sm *indexManager[T]) search(ctx context.Context, query string) (results []*T, err error) {
	_, span := sm.tracer.StartSpan(ctx)
	defer span.End()

	if sm.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	tracing.AttachToSpan(span, keys.SearchQueryKey, query)
	logger := sm.logger.WithValue(keys.SearchQueryKey, query)

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
		return nil, observability.PrepareError(err, span, "encodign search query")
	}

	res, err := sm.esClient.Search(
		sm.esClient.Search.WithIndex(sm.indexName),
		sm.esClient.Search.WithBody(bytes.NewReader(queryBody)),
	)
	defer func() {
		if res != nil {
			if err = res.Body.Close(); err != nil {
				observability.AcknowledgeError(err, logger, span, "closing response body")
			}
		}
	}()

	if err != nil {
		sm.circuitBreaker.Failed()
		return nil, observability.PrepareError(err, span, "querying elasticsearch successfully")
	}

	if res.IsError() {
		var e map[string]any
		if err = json.NewDecoder(res.Body).Decode(&e); err != nil {
			sm.circuitBreaker.Failed()
			return nil, observability.PrepareError(err, span, "invalid response from elasticsearch")
		}

		err = platformerrors.New(strings.Join(res.Warnings(), ", "))
		sm.circuitBreaker.Failed()
		return nil, observability.PrepareError(err, span, "querying elasticsearch")
	}

	var r esResponse
	if err = json.NewDecoder(res.Body).Decode(&r); err != nil {
		sm.circuitBreaker.Failed()
		return nil, observability.PrepareError(err, span, "decoding response")
	}

	for _, hit := range r.Hits.Hits {
		var c *T
		if err = json.Unmarshal(hit.Source, &c); err != nil {
			sm.circuitBreaker.Failed()
			return nil, observability.PrepareError(err, span, "decoding response")
		}
		resultIDs = append(resultIDs, c)
	}

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
	_, span := sm.tracer.StartSpan(ctx)
	defer span.End()

	if sm.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	logger := sm.logger.WithValue("id", id)

	_, err := esapi.DeleteRequest{
		Index:      sm.indexName,
		DocumentID: id,
	}.Do(ctx, sm.esClient)
	if err != nil {
		sm.circuitBreaker.Failed()
		return observability.PrepareError(err, span, "deleting from elasticsearch")
	}

	logger.Debug("removed from index")

	sm.circuitBreaker.Succeeded()
	return nil
}
