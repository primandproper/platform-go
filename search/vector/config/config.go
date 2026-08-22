// Package vectorsearchcfg selects and builds a vector search index from
// configuration: pgvector, Qdrant, or noop.
//
// Only pgvector needs the database.Client NewIndex takes — it stores vectors in
// the service's own Postgres rather than in a separate system — so every other
// provider is passed nil. The embedding dimension is part of the selected
// provider's config and is not negotiable after the fact: it has to match
// whatever produced the vectors being written.
package vectorsearchcfg

import (
	"context"
	"strings"

	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"
	"github.com/primandproper/platform-go/v13/search/vector/noop"
	"github.com/primandproper/platform-go/v13/search/vector/pgvector"
	"github.com/primandproper/platform-go/v13/search/vector/qdrant"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// PGvectorProvider selects the pgvector-backed vectorsearch.Index implementation.
	PGvectorProvider = "pgvector"
	// QdrantProvider selects the Qdrant-backed vectorsearch.Index implementation.
	QdrantProvider = "qdrant"
)

// Config dispatches to a vectorsearch provider implementation.
type Config struct {
	_              struct{}                  `json:"-"       yaml:"-"`
	Pgvector       *pgvector.Config          `env:",init"    envPrefix:"PGVECTOR_"        json:"pgvector,omitempty"            yaml:"pgvector,omitempty"`
	Qdrant         *qdrant.Config            `env:",init"    envPrefix:"QDRANT_"          json:"qdrant,omitempty"              yaml:"qdrant,omitempty"`
	Provider       string                    `env:"PROVIDER" json:"provider,omitempty"    yaml:"provider,omitempty"`
	CircuitBreaker circuitbreakingcfg.Config `env:",init"    envPrefix:"CIRCUIT_BREAKER_" json:"circuitBreakerConfig,omitzero" yaml:"circuitBreakerConfig,omitempty"`
}

// ProviderNoop indexes and searches nothing. It must be selected deliberately —
// an unset or typo'd provider is an error, because an index that quietly accepts
// every write and returns no hits looks like an empty corpus.
const ProviderNoop = "noop"

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct. Provider is canonicalized (trim + lowercase)
// first so validation matches the same normalization NewIndex dispatches on.
//
// The sub-config for a provider that was not selected is skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so both
// providers' dimensions and endpoints were required at once and no config could
// load. Releasing the zero sub-configs instead would not do: both carry
// envDefault fields, so neither is zero once the environment has been parsed.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	cfg.Provider = strings.TrimSpace(strings.ToLower(cfg.Provider))

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.In(PGvectorProvider, QdrantProvider, ProviderNoop)),
		validation.Field(&cfg.Pgvector, validation.Skip.When(cfg.Provider != PGvectorProvider), validation.Required),
		validation.Field(&cfg.Qdrant, validation.Skip.When(cfg.Provider != QdrantProvider), validation.Required),
	)
}

// NewIndex builds a vectorsearch.Index for the configured provider. The db
// argument is required only when Provider is PGvectorProvider; pass nil otherwise.
// An unknown or empty provider is an error; the noop index is reachable by
// naming it, matching the search/text dispatch convention.
func NewIndex[T any](
	ctx context.Context,
	cfg *Config,
	db database.Client,
	indexName string,
	opts ...Option,
) (vectorsearch.Index[T], error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, vectorsearch.ErrNilConfig
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating vector search config")
	}

	circuitBreaker, err := circuitbreakingcfg.NewCircuitBreaker(ctx, &cfg.CircuitBreaker, circuitbreakingcfg.WithLogger(logger), circuitbreakingcfg.WithMetricsProvider(metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "initializing vector search circuit breaker")
	}

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case PGvectorProvider:
		index, indexErr := pgvector.NewIndex[T](ctx, cfg.Pgvector, db, indexName, circuitBreaker, pgvector.WithLogger(logger), pgvector.WithTracerProvider(tracerProvider), pgvector.WithMetricsProvider(metricsProvider))
		if indexErr != nil {
			return nil, indexErr
		}

		return index, nil
	case QdrantProvider:
		index, indexErr := qdrant.NewIndex[T](ctx, cfg.Qdrant, indexName, circuitBreaker, qdrant.WithLogger(logger), qdrant.WithTracerProvider(tracerProvider), qdrant.WithMetricsProvider(metricsProvider))
		if indexErr != nil {
			return nil, indexErr
		}

		return index, nil
	case ProviderNoop:
		return noop.NewIndex[T](), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "vector search provider %q", cfg.Provider)
	}
}
