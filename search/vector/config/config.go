package vectorsearchcfg

import (
	"context"
	"strings"

	circuitbreakingcfg "github.com/primandproper/platform-go/v4/circuitbreaking/config"
	"github.com/primandproper/platform-go/v4/database"
	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"
	vectorsearch "github.com/primandproper/platform-go/v4/search/vector"
	"github.com/primandproper/platform-go/v4/search/vector/noop"
	"github.com/primandproper/platform-go/v4/search/vector/pgvector"
	"github.com/primandproper/platform-go/v4/search/vector/qdrant"

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
	_              struct{}                  `json:"-"`
	Pgvector       *pgvector.Config          `env:"init"     envPrefix:"PGVECTOR_"        json:"pgvector"`
	Qdrant         *qdrant.Config            `env:"init"     envPrefix:"QDRANT_"          json:"qdrant"`
	Provider       string                    `env:"PROVIDER" json:"provider"`
	CircuitBreaker circuitbreakingcfg.Config `env:"init"     envPrefix:"CIRCUIT_BREAKER_" json:"circuitBreakerConfig"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct. Provider is canonicalized (trim + lowercase)
// first so validation matches the same normalization NewIndex dispatches on.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	cfg.Provider = strings.TrimSpace(strings.ToLower(cfg.Provider))

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(PGvectorProvider, QdrantProvider)),
		validation.Field(&cfg.Pgvector, validation.When(cfg.Provider == PGvectorProvider, validation.Required)),
		validation.Field(&cfg.Qdrant, validation.When(cfg.Provider == QdrantProvider, validation.Required)),
	)
}

// NewIndex builds a vectorsearch.Index for the configured provider. The db
// argument is required only when Provider is PGvectorProvider; pass nil otherwise.
// Unknown or empty providers fall back to a noop index, matching the search/text
// dispatch convention.
func NewIndex[T any](
	ctx context.Context,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	cfg *Config,
	db database.Client,
	indexName string,
) (vectorsearch.Index[T], error) {
	if cfg == nil {
		return nil, vectorsearch.ErrNilConfig
	}

	circuitBreaker, err := circuitbreakingcfg.NewCircuitBreaker(ctx, &cfg.CircuitBreaker, logger, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "initializing vector search circuit breaker")
	}

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case PGvectorProvider:
		return pgvector.NewIndex[T](ctx, logger, tracerProvider, metricsProvider, cfg.Pgvector, db, indexName, circuitBreaker)
	case QdrantProvider:
		return qdrant.NewIndex[T](ctx, logger, tracerProvider, metricsProvider, cfg.Qdrant, indexName, circuitBreaker)
	default:
		return noop.NewIndex[T](), nil
	}
}
