package profilingcfg

import (
	"context"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/profiling"
	profilingnoop "github.com/primandproper/platform-go/v4/observability/profiling/noop"
	"github.com/primandproper/platform-go/v4/observability/profiling/pprof"
	"github.com/primandproper/platform-go/v4/observability/profiling/pyroscope"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderPyroscope represents Grafana Pyroscope.
	ProviderPyroscope = "pyroscope"
	// ProviderPprof represents Go-native pprof HTTP server.
	ProviderPprof = "pprof"
)

type (
	// Config contains settings related to profiling.
	Config struct {
		_           struct{}          `json:"-"`
		Pyroscope   *pyroscope.Config `env:"init"         envPrefix:"PYROSCOPE_"    json:"pyroscope,omitempty"`
		Pprof       *pprof.Config     `env:"init"         envPrefix:"PPROF_"        json:"pprof,omitempty"`
		ServiceName string            `env:"SERVICE_NAME" json:"serviceName"`
		Provider    string            `env:"PROVIDER"     json:"provider,omitempty"`
	}
)

// NewProfilingProvider provides a profiling provider based on config.
func (c *Config) NewProfilingProvider(ctx context.Context, logger logging.Logger) (profiling.Provider, error) {
	p := strings.TrimSpace(strings.ToLower(c.Provider))

	switch p {
	case ProviderPyroscope:
		if c.Pyroscope == nil {
			return profilingnoop.NewProvider(), nil
		}
		// Set default upload rate if not specified.
		if c.Pyroscope.UploadRate == 0 {
			c.Pyroscope.UploadRate = 15 * time.Second
		}
		return pyroscope.NewProfilingProvider(ctx, logger, c.ServiceName, c.Pyroscope)
	case ProviderPprof:
		if c.Pprof == nil {
			c.Pprof = &pprof.Config{Port: pprof.DefaultPort}
		}
		return pprof.NewProfilingProvider(ctx, logger, c.Pprof)
	default:
		return profilingnoop.NewProvider(), nil
	}
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.In("", ProviderPyroscope, ProviderPprof)),
		validation.Field(&c.Pyroscope, validation.When(c.Provider == ProviderPyroscope, validation.Required).Else(validation.Nil)),
		validation.Field(&c.Pprof, validation.When(c.Provider == ProviderPyroscope || c.Provider == "", validation.Nil)),
	)
}
