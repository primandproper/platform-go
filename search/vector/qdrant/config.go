package qdrant

import (
	"context"
	"net/url"
	"time"

	"github.com/primandproper/platform-go/v4/errors"
	vectorsearch "github.com/primandproper/platform-go/v4/search/vector"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures the qdrant-backed vectorsearch.Index. The provider speaks REST,
// so BaseURL must point at the qdrant HTTP endpoint (default port 6333), not the
// gRPC port.
type Config struct {
	BaseURL   string                      `env:"BASE_URL"  json:"baseURL"`
	APIKey    string                      `env:"API_KEY"   json:"apiKey,omitempty"`
	Metric    vectorsearch.DistanceMetric `env:"METRIC"    envDefault:"cosine"     json:"metric"`
	Timeout   time.Duration               `env:"TIMEOUT"   envDefault:"30s"        json:"timeout"`
	Dimension int                         `env:"DIMENSION" json:"dimension"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.BaseURL, validation.Required, validation.By(func(value any) error {
			s, ok := value.(string)
			if !ok {
				return errors.New("base URL must be a string")
			}
			if _, parseErr := url.Parse(s); parseErr != nil {
				return parseErr
			}
			return nil
		})),
		validation.Field(&cfg.Dimension, validation.Required, validation.Min(1)),
		validation.Field(&cfg.Metric, validation.Required, validation.In(
			vectorsearch.DistanceCosine,
			vectorsearch.DistanceDotProduct,
			vectorsearch.DistanceEuclidean,
		)),
	)
}
