package algolia

import (
	"fmt"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	textsearch "github.com/primandproper/platform-go/v13/search/text"

	algolia "github.com/algolia/algoliasearch-client-go/v3/algolia/search"
)

// serviceName scopes this backend's instrument names.
const serviceName = "algolia_index"

var (
	_ textsearch.Index[any] = (*IndexManager[any])(nil)

	ErrNilConfig = platformerrors.New("nil config provided")
)

type (
	// IndexManager is the Algolia textsearch.Index. It is exported, and returned
	// by NewIndexManager, so a caller who has chosen Algolia can depend on that
	// choice rather than on the seam every text index shares.
	IndexManager[T any] struct {
		o11y           observability.Observer
		circuitBreaker circuitbreaking.CircuitBreaker
		client         *algolia.Index
		instruments    *textsearch.Instruments
	}
)

func NewIndexManager[T any](
	cfg *Config,
	indexName string,
	circuitBreaker circuitbreaking.CircuitBreaker,
	opts ...Option,
) (*IndexManager[T], error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	o := newOptions(opts)

	clientConfig := algolia.Configuration{
		AppID:  cfg.AppID,
		APIKey: cfg.APIKey,
	}
	// Honor a configured timeout for both read and write operations; leave the
	// SDK's own defaults in place when unset.
	if cfg.Timeout > 0 {
		clientConfig.ReadTimeout = cfg.Timeout
		clientConfig.WriteTimeout = cfg.Timeout
	}

	instruments, err := textsearch.NewInstruments(serviceName, indexName, o.metricsProvider)
	if err != nil {
		return nil, err
	}

	im := &IndexManager[T]{
		o11y: observability.NewObserverWithValues(fmt.Sprintf("search_%s", indexName), o.logger, o.tracerProvider,
			map[string]any{keys.IndexNameKey: indexName}),
		client:         algolia.NewClientWithConfig(clientConfig).InitIndex(indexName),
		circuitBreaker: circuitBreaker,
		instruments:    instruments,
	}

	return im, nil
}
