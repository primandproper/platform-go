package algolia

import (
	"fmt"

	"github.com/primandproper/platform-go/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
	textsearch "github.com/primandproper/platform-go/search/text"

	algolia "github.com/algolia/algoliasearch-client-go/v3/algolia/search"
)

var (
	_ textsearch.Index[any] = (*indexManager[any])(nil)

	ErrNilConfig = platformerrors.New("nil config provided")
)

type (
	indexManager[T any] struct {
		logger         logging.Logger
		tracer         tracing.Tracer
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
		tracer:         tracing.NewNamedTracer(tracerProvider, fmt.Sprintf("search_%s", indexName)),
		logger:         logging.NewNamedLogger(logger, indexName),
		client:         algolia.NewClient(cfg.AppID, cfg.APIKey).InitIndex(indexName),
		circuitBreaker: circuitBreaker,
	}

	return im, nil
}
