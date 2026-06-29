package algolia

import (
	"fmt"

	"github.com/primandproper/platform-go/v2/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"
	textsearch "github.com/primandproper/platform-go/v2/search/text"

	algolia "github.com/algolia/algoliasearch-client-go/v3/algolia/search"
)

var (
	_ textsearch.Index[any] = (*indexManager[any])(nil)

	ErrNilConfig = platformerrors.New("nil config provided")
)

type (
	indexManager[T any] struct {
		o11y           observability.Observer
		circuitBreaker circuitbreaking.CircuitBreaker
		client         *algolia.Index
		DataType       *T
	}
)

func ProvideIndexManager[T any](
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	cfg *Config,
	indexName string,
	circuitBreaker circuitbreaking.CircuitBreaker,
) (textsearch.Index[T], error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	im := &indexManager[T]{
		o11y:           observability.NewObserver(fmt.Sprintf("search_%s", indexName), logger, tracerProvider),
		client:         algolia.NewClient(cfg.AppID, cfg.APIKey).InitIndex(indexName),
		circuitBreaker: circuitBreaker,
	}

	return im, nil
}
