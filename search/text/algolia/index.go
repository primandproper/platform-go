package algolia

import (
	"context"
	"encoding/json"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/observability/keys"
	textsearch "github.com/primandproper/platform-go/v13/search/text"

	"github.com/algolia/algoliasearch-client-go/v3/algolia/opt"
)

const (
	objectIDKey = "objectID"
	idKey       = "id"

	// backendName tags cursors this package issues, so one cannot be resumed
	// against a different backend.
	backendName = "algolia"

	// maxSearchLimit caps a single page. Algolia's own ceiling for
	// hitsPerPage is 1000, which is far past useful.
	maxSearchLimit = 200
)

// Index implements textsearch.Index.
func (m *IndexManager[T]) Index(ctx context.Context, id string, value any) (err error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	started := time.Now()
	defer func() { m.instruments.Record(ctx, textsearch.OperationIndex, started, err) }()

	if m.circuitBreaker.CannotProceed() {
		return op.Error(circuitbreaking.ErrCircuitBroken, "indexing value")
	}

	op.Set(idKey, id)
	op.Logger().Debug("adding to index")

	jsonEncoded, err := json.Marshal(value)
	if err != nil {
		return op.Error(err, "encoding value for indexing")
	}

	var newValue map[string]any
	if err = json.Unmarshal(jsonEncoded, &newValue); err != nil {
		return op.Error(err, "decoding value for indexing")
	}

	// we make a huge, albeit safe assumption here.
	newValue[objectIDKey] = newValue[idKey]
	delete(newValue, idKey)

	if _, err = m.client.SaveObject(newValue); err != nil {
		m.circuitBreaker.Failed()

		return op.Error(err, "indexing value")
	}

	m.circuitBreaker.Succeeded()

	return nil
}

// Search implements our IndexSearcher interface.
func (m *IndexManager[T]) Search(ctx context.Context, req textsearch.SearchRequest) (_ *textsearch.SearchResults[T], err error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	started := time.Now()
	defer func() { m.instruments.Record(ctx, textsearch.OperationSearch, started, err) }()

	if m.circuitBreaker.CannotProceed() {
		return nil, op.Error(circuitbreaking.ErrCircuitBroken, "searching index")
	}

	op.Set(keys.SearchQueryKey, req.Query)

	if req.Query == "" {
		return nil, op.Error(textsearch.ErrEmptyQueryProvided, "searching index")
	}

	// Algolia paginates by page number, not document offset, so that is what
	// this backend's cursor carries.
	page, err := textsearch.DecodeCursor(backendName, req.Cursor)
	if err != nil {
		// Not the backend's failure — a cursor this backend did not issue, or one
		// that has been tampered with — so the breaker is left alone. Tripping on
		// it would take a healthy index out of service over a bad request.
		return nil, op.Error(err, "decoding cursor")
	}

	hitsPerPage := textsearch.EffectiveLimit(req.Limit, maxSearchLimit)

	op.Set("search.page", page).Set("search.hitsPerPage", hitsPerPage)

	res, err := m.client.Search(req.Query, opt.Page(page), opt.HitsPerPage(hitsPerPage))
	if err != nil {
		m.circuitBreaker.Failed()

		return nil, op.Error(err, "searching index")
	}

	results := []*T{}

	for _, hit := range res.Hits {
		var x *T

		// we make the same assumption here, sort of
		if _, ok := hit[objectIDKey]; ok {
			hit[idKey] = hit[objectIDKey]
			delete(hit, objectIDKey)
		}

		var encodedAsJSON []byte
		if encodedAsJSON, err = json.Marshal(hit); err != nil {
			return nil, op.Error(err, "encoding search hit")
		}

		if err = json.Unmarshal(encodedAsJSON, &x); err != nil {
			return nil, op.Error(err, "decoding search hit")
		}

		results = append(results, x)
	}

	op.Set(keys.LengthKey, len(results))
	op.Logger().Debug("search performed")

	out := &textsearch.SearchResults[T]{Hits: results}

	// NbPages is authoritative for whether another page exists; a short page is
	// not the signal, since Algolia can return fewer hits than hitsPerPage and
	// still have more pages behind it.
	if next := page + 1; len(results) > 0 && next < res.NbPages {
		if out.NextCursor, err = textsearch.EncodeCursor(backendName, next); err != nil {
			return nil, op.Error(err, "encoding next cursor")
		}
	}

	m.circuitBreaker.Succeeded()

	return out, nil
}

// Delete implements textsearch.Index.
func (m *IndexManager[T]) Delete(ctx context.Context, id string) (err error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	started := time.Now()
	defer func() { m.instruments.Record(ctx, textsearch.OperationDelete, started, err) }()

	if m.circuitBreaker.CannotProceed() {
		return op.Error(circuitbreaking.ErrCircuitBroken, "removing from index")
	}

	op.Set(idKey, id)

	if _, err = m.client.DeleteObject(id); err != nil {
		m.circuitBreaker.Failed()

		return op.Error(err, "removing from index")
	}

	op.Logger().Debug("removed from index")

	m.circuitBreaker.Succeeded()

	return nil
}

// Wipe implements textsearch.Index.
func (m *IndexManager[T]) Wipe(ctx context.Context) (err error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	started := time.Now()
	defer func() { m.instruments.Record(ctx, textsearch.OperationWipe, started, err) }()

	if m.circuitBreaker.CannotProceed() {
		return op.Error(circuitbreaking.ErrCircuitBroken, "wiping index")
	}

	if _, err = m.client.ClearObjects(); err != nil {
		m.circuitBreaker.Failed()

		return op.Error(err, "wiping index")
	}

	m.circuitBreaker.Succeeded()

	return nil
}
