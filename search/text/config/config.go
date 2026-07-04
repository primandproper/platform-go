package textsearchcfg

import (
	"context"
	"strings"

	circuitbreakingcfg "github.com/primandproper/platform-go/v3/circuitbreaking/config"
	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"
	textsearch "github.com/primandproper/platform-go/v3/search/text"
	"github.com/primandproper/platform-go/v3/search/text/algolia"
	"github.com/primandproper/platform-go/v3/search/text/elasticsearch"
	"github.com/primandproper/platform-go/v3/search/text/noop"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ElasticsearchProvider represents the elasticsearch search index provider.
	ElasticsearchProvider = "elasticsearch"
	// AlgoliaProvider represents the algolia search index provider.
	AlgoliaProvider = "algolia"
)

// Config contains settings regarding search indices.
type Config struct {
	_ struct{} `json:"-"`

	Algolia        *algolia.Config           `env:"init"     envPrefix:"ALGOLIA_"         json:"algolia"`
	Elasticsearch  *elasticsearch.Config     `env:"init"     envPrefix:"ELASTICSEARCH_"   json:"elasticsearch"`
	Provider       string                    `env:"PROVIDER" json:"provider"`
	CircuitBreaker circuitbreakingcfg.Config `env:"init"     envPrefix:"CIRCUIT_BREAKER_" json:"circuitBreakerConfig"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct. Provider is canonicalized (trim + lowercase)
// first so validation matches the same normalization ProvideIndex dispatches on.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	cfg.Provider = strings.TrimSpace(strings.ToLower(cfg.Provider))

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(ElasticsearchProvider, AlgoliaProvider)),
		validation.Field(&cfg.Algolia, validation.When(cfg.Provider == AlgoliaProvider, validation.Required)),
		validation.Field(&cfg.Elasticsearch, validation.When(cfg.Provider == ElasticsearchProvider, validation.Required)),
	)
}

// ProvideIndex validates a Config struct.
func ProvideIndex[T any](
	ctx context.Context,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	cfg *Config,
	indexName string,
) (textsearch.Index[T], error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	circuitBreaker, err := circuitbreakingcfg.ProvideCircuitBreakerFromConfig(ctx, &cfg.CircuitBreaker, logger, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize text search circuit breaker")
	}

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ElasticsearchProvider:
		return elasticsearch.ProvideIndexManager[T](ctx, logger, tracerProvider, cfg.Elasticsearch, indexName, circuitBreaker)
	case AlgoliaProvider:
		return algolia.ProvideIndexManager[T](logger, tracerProvider, cfg.Algolia, indexName, circuitBreaker)
	default:
		return noop.NewIndexManager[T](), nil
	}
}
