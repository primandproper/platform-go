// Package textsearchcfg selects and builds a text search index from
// configuration: Elasticsearch, Algolia, or noop.
//
// The index is generic over the document type and one index is built per index
// name, so a service with several corpora calls NewIndex once each rather than
// configuring several providers. Whichever backend is selected is wrapped in a
// circuit breaker built from the same config, since both of them are a network
// away.
package textsearchcfg

import (
	"context"
	"strings"

	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
	"github.com/primandproper/platform-go/v13/search/text/algolia"
	"github.com/primandproper/platform-go/v13/search/text/elasticsearch"
	"github.com/primandproper/platform-go/v13/search/text/noop"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ElasticsearchProvider represents the elasticsearch search index provider.
	ElasticsearchProvider = "elasticsearch"
	// AlgoliaProvider represents the algolia search index provider.
	AlgoliaProvider = "algolia"
)

// ProviderNoop indexes and searches nothing. It must be selected deliberately —
// an unset or typo'd provider is an error, because an index that quietly accepts
// every write and returns no hits looks like an empty corpus.
const ProviderNoop = "noop"

// Config contains settings regarding search indices.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	Algolia        *algolia.Config           `env:",init"    envPrefix:"ALGOLIA_"         json:"algolia,omitempty"             yaml:"algolia,omitempty"`
	Elasticsearch  *elasticsearch.Config     `env:",init"    envPrefix:"ELASTICSEARCH_"   json:"elasticsearch,omitempty"       yaml:"elasticsearch,omitempty"`
	Provider       string                    `env:"PROVIDER" json:"provider,omitempty"    yaml:"provider,omitempty"`
	CircuitBreaker circuitbreakingcfg.Config `env:",init"    envPrefix:"CIRCUIT_BREAKER_" json:"circuitBreakerConfig,omitzero" yaml:"circuitBreakerConfig,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct. Provider is canonicalized (trim + lowercase)
// first so validation matches the same normalization NewIndex dispatches on.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	// Release the sub-configs env parsing's ",init" allocated and nothing filled in.
	// Without this they reach their own validation, which requires the credentials
	// they would need if they were the selected provider — and rejects every config
	// that named a different one.
	cfgnorm.ZeroToNil(&cfg.Algolia)
	cfgnorm.ZeroToNil(&cfg.Elasticsearch)

	cfg.Provider = strings.TrimSpace(strings.ToLower(cfg.Provider))

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.In(ElasticsearchProvider, AlgoliaProvider, ProviderNoop)),
		validation.Field(&cfg.Algolia, validation.When(cfg.Provider == AlgoliaProvider, validation.Required)),
		validation.Field(&cfg.Elasticsearch, validation.When(cfg.Provider == ElasticsearchProvider, validation.Required)),
	)
}

// NewIndex validates a Config struct.
func NewIndex[T any](
	ctx context.Context,
	cfg *Config,
	indexName string,
	opts ...Option,
) (textsearch.Index[T], error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	// The package doc has always claimed this validates; now it does.
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating text search config")
	}

	circuitBreaker, err := circuitbreakingcfg.NewCircuitBreaker(ctx, &cfg.CircuitBreaker, circuitbreakingcfg.WithLogger(logger), circuitbreakingcfg.WithMetricsProvider(metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize text search circuit breaker")
	}

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ElasticsearchProvider:
		index, indexErr := elasticsearch.NewIndexManager[T](ctx, cfg.Elasticsearch, indexName, circuitBreaker,
			elasticsearch.WithLogger(logger),
			elasticsearch.WithTracerProvider(tracerProvider),
			elasticsearch.WithMetricsProvider(metricsProvider),
		)
		if indexErr != nil {
			return nil, indexErr
		}

		return index, nil
	case AlgoliaProvider:
		index, indexErr := algolia.NewIndexManager[T](cfg.Algolia, indexName, circuitBreaker,
			algolia.WithLogger(logger),
			algolia.WithTracerProvider(tracerProvider),
			algolia.WithMetricsProvider(metricsProvider),
		)
		if indexErr != nil {
			return nil, indexErr
		}

		return index, nil
	case ProviderNoop:
		return noop.NewIndexManager[T](), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "text search provider %q", cfg.Provider)
	}
}
