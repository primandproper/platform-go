package algolia

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v5/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability/keys"
)

const (
	objectIDKey = "objectID"
	idKey       = "id"
)

var (
	// ErrEmptyQueryProvided indicates an empty query was provided as input.
	ErrEmptyQueryProvided = platformerrors.New("empty search query provided")
)

// Index implements our indexManager interface.
func (m *indexManager[T]) Index(ctx context.Context, id string, value any) error {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	if m.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	op.Set(idKey, id)
	op.Logger().Debug("adding to index")

	jsonEncoded, err := json.Marshal(value)
	if err != nil {
		return err
	}

	var newValue map[string]any
	if unmarshalErr := json.Unmarshal(jsonEncoded, &newValue); unmarshalErr != nil {
		return unmarshalErr
	}

	// we make a huge, albeit safe assumption here.
	newValue[objectIDKey] = newValue[idKey]
	delete(newValue, idKey)

	if _, err = m.client.SaveObject(newValue); err != nil {
		m.circuitBreaker.Failed()
		return err
	}

	m.circuitBreaker.Succeeded()
	return nil
}

// Search implements our indexManager interface.
func (m *indexManager[T]) Search(ctx context.Context, query string) ([]*T, error) {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	if m.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	op.Set(keys.SearchQueryKey, query)

	if query == "" {
		return nil, ErrEmptyQueryProvided
	}

	res, searchErr := m.client.Search(query)
	if searchErr != nil {
		m.circuitBreaker.Failed()
		return nil, searchErr
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
		encodedAsJSON, err := json.Marshal(hit)
		if err != nil {
			return nil, err
		}

		if unmarshalErr := json.Unmarshal(encodedAsJSON, &x); unmarshalErr != nil {
			return nil, unmarshalErr
		}

		results = append(results, x)
	}

	op.Set(keys.LengthKey, len(results))
	op.Logger().Debug("search performed")

	m.circuitBreaker.Succeeded()
	return results, nil
}

// Delete implements our indexManager interface.
func (m *indexManager[T]) Delete(ctx context.Context, id string) error {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	if m.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	op.Set(idKey, id)

	if _, err := m.client.DeleteObject(id); err != nil {
		m.circuitBreaker.Failed()
		return err
	}

	op.Logger().Debug("removed from index")

	m.circuitBreaker.Succeeded()
	return nil
}

// Wipe implements our indexManager interface.
func (m *indexManager[T]) Wipe(ctx context.Context) error {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	if m.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	if _, err := m.client.ClearObjects(); err != nil {
		m.circuitBreaker.Failed()
		return err
	}

	m.circuitBreaker.Succeeded()
	return nil
}
