package kubectl

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures the Kubernetes secret source.
type Config struct {
	Namespace  string `env:"NAMESPACE"  json:"namespace"            yaml:"namespace"`
	Kubeconfig string `env:"KUBECONFIG" json:"kubeconfig,omitempty" yaml:"kubeconfig,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Namespace, validation.Required),
	)
}
