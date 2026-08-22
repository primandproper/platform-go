// Package profilingcfg selects and builds a profiling.Provider from
// configuration: Grafana Pyroscope, the Go-native pprof HTTP server, or no
// profiling at all.
//
// One of the four pillar config packages, so it has no WithPillars option —
// observability imports it to build a Pillars.
//
// It supplies Pyroscope's upload rate default, which is unusual: defaults
// normally live with the config they belong to, and this one lives here because
// pyroscope.Config has no defaults of its own to apply it alongside. Like every
// default in this module it is applied before validation, so an unset rate is a
// configured deployment rather than a rejected one.
package profilingcfg

import (
	"context"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/profiling"
	profilingnoop "github.com/primandproper/platform-go/v13/observability/profiling/noop"
	"github.com/primandproper/platform-go/v13/observability/profiling/pprof"
	"github.com/primandproper/platform-go/v13/observability/profiling/pyroscope"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderPyroscope represents Grafana Pyroscope.
	ProviderPyroscope = "pyroscope"
	// ProviderPprof represents Go-native pprof HTTP server.
	ProviderPprof = "pprof"
	// ProviderNoop, and the empty string, select no profiling at all. That is the
	// deliberate opt-out and stays supported; what is no longer supported is a
	// provider name this package does not recognize, which used to disable
	// profiling silently and looked exactly like the opt-out.
	ProviderNoop = "noop"
)

type (
	// Config contains settings related to profiling.
	Config struct {
		_           struct{}          `json:"-"           yaml:"-"`
		Pyroscope   *pyroscope.Config `env:",init"        envPrefix:"PYROSCOPE_"       json:"pyroscope,omitempty"   yaml:"pyroscope,omitempty"`
		Pprof       *pprof.Config     `env:",init"        envPrefix:"PPROF_"           json:"pprof,omitempty"       yaml:"pprof,omitempty"`
		ServiceName string            `env:"SERVICE_NAME" json:"serviceName,omitempty" yaml:"serviceName,omitempty"`
		Provider    string            `env:"PROVIDER"     json:"provider,omitempty"    yaml:"provider,omitempty"`
	}
)

// providers are every provider this package implements, plus the empty string,
// which selects no profiling — the deliberate opt-out. Validation and
// NewProfilingProvider both read it.
var providers = []string{"", ProviderNoop, ProviderPyroscope, ProviderPprof}

// defaultPyroscopeUploadRate is how often the agent ships profiles when the
// operator did not say. It lives here rather than in the pyroscope package
// because that config has no defaults of its own to apply it alongside.
const defaultPyroscopeUploadRate = 15 * time.Second

// EnsureDefaults fills in the fields this package supplies a default for.
//
// The upload rate used to be defaulted inside NewProfilingProvider, after the
// point where a validation call would have run, so pyroscope's own Required
// rule and the constructor disagreed about whether an unset rate was a
// configuration or a mistake. Defaults belong before validation, which is what
// this is for.
func (c *Config) EnsureDefaults() {
	if c == nil {
		return
	}

	if cfgnorm.Provider(c.Provider) == ProviderPyroscope && c.Pyroscope != nil && c.Pyroscope.UploadRate == 0 {
		c.Pyroscope.UploadRate = defaultPyroscopeUploadRate
	}
}

// NewProfilingProvider provides a profiling provider based on config.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *pyroscope.Provider
// into a non-nil profiling.Provider on the error path, and a caller testing the
// result against nil would find a provider that panics on first use.
func (c *Config) NewProfilingProvider(ctx context.Context, opts ...Option) (profiling.Provider, error) {
	if c == nil {
		return nil, errors.ErrNilInputParameter
	}

	// EnsureLogger, not the raw option: the logger is optional now, and both
	// real providers log what they started.
	logger := logging.EnsureLogger(newOptions(opts).logger)

	p, err := cfgnorm.SelectProvider(c.Provider, providers, "profiling provider")
	if err != nil {
		return nil, err
	}

	c.EnsureDefaults()

	if err = c.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating profiling config")
	}

	switch p {
	case ProviderPyroscope:
		// Validation requires the block for this provider, so this cannot be
		// nil. It used to be, and the answer was a noop provider — profiling
		// silently off for exactly the deployment that asked for it.
		p, provErr := pyroscope.NewProfilingProvider(ctx, logger, c.ServiceName, c.Pyroscope)
		if provErr != nil {
			return nil, provErr
		}

		return p, nil
	case ProviderPprof:
		if c.Pprof == nil {
			c.Pprof = &pprof.Config{Port: pprof.DefaultPort}
		}

		p, provErr := pprof.NewProfilingProvider(ctx, logger, c.Pprof)
		if provErr != nil {
			return nil, provErr
		}

		return p, nil
	case "", ProviderNoop:
		return profilingnoop.NewProvider(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "profiling provider %q", c.Provider)
	}
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config struct.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	// Release the sub-configs env parsing's ",init" allocated and nothing filled
	// in, so the Nil rules below read "the operator configured this" rather than
	// "env parsing ran".
	cfgnorm.ZeroToNil(&c.Pyroscope)
	cfgnorm.ZeroToNil(&c.Pprof)

	provider := cfgnorm.Provider(c.Provider)

	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "Pyroscope" while NewProfilingProvider built it.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "profiling provider %q", c.Provider)
			}

			return nil
		})),
		validation.Field(&c.Pyroscope, validation.When(provider == ProviderPyroscope, validation.Required).Else(validation.Nil)),
		validation.Field(&c.Pprof, validation.When(provider == ProviderPyroscope || provider == "", validation.Nil)),
	)
}
