package pgvector

import (
	"context"

	"github.com/primandproper/platform-go/v2/errors"
	vectorsearch "github.com/primandproper/platform-go/v2/search/vector"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures the pgvector-backed vectorsearch.Index.
//
// Dimension must match the embedding dimension produced by the upstream model and
// is enforced at index creation time via vector(<Dimension>).
//
// MetadataColumn is the JSONB column used to store the per-vector payload (the
// generic T type). It defaults to "metadata" and must be a bare identifier.
type Config struct {
	MetadataColumn string                      `env:"METADATA_COLUMN" envDefault:"metadata" json:"metadataColumn"`
	Metric         vectorsearch.DistanceMetric `env:"METRIC"          envDefault:"cosine"   json:"metric"`
	Dimension      int                         `env:"DIMENSION"       json:"dimension"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Dimension, validation.Required, validation.Min(1)),
		validation.Field(&cfg.Metric, validation.Required, validation.In(
			vectorsearch.DistanceCosine,
			vectorsearch.DistanceDotProduct,
			vectorsearch.DistanceEuclidean,
		)),
	)
}
