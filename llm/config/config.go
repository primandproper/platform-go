// Package llmcfg selects and builds an llm.Provider from configuration: OpenAI,
// Anthropic, or the noop provider.
//
// The selected vendor's sub-config is validated here rather than trusted to have
// been validated upstream, because that block is what carries the API key: a
// deployment naming openai and supplying nothing used to get as far as its first
// completion before finding out.
package llmcfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/llm"
	"github.com/primandproper/platform-go/v13/llm/anthropic"
	llmnoop "github.com/primandproper/platform-go/v13/llm/noop"
	"github.com/primandproper/platform-go/v13/llm/openai"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderOpenAI is the OpenAI provider.
	ProviderOpenAI = "openai"
	// ProviderAnthropic is the Anthropic provider.
	ProviderAnthropic = "anthropic"
	// ProviderNoop answers every request from a canned response and calls nothing.
	// It must be selected deliberately — an unset or typo'd provider is an error,
	// because an LLM that silently stops calling anything looks like a working
	// deployment whose answers have quietly become useless.
	ProviderNoop = "noop"
)

// Config is the configuration for the LLM provider.
type Config struct {
	OpenAI    *openai.Config    `env:",init"    envPrefix:"OPENAI_"       json:"openai,omitempty"    yaml:"openai,omitempty"`
	Anthropic *anthropic.Config `env:",init"    envPrefix:"ANTHROPIC_"    json:"anthropic,omitempty" yaml:"anthropic,omitempty"`
	Provider  string            `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
}

// providers are every provider this package implements. Validation and
// NewLLMProvider both read it.
var providers = []string{ProviderOpenAI, ProviderAnthropic, ProviderNoop}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the config.
//
// The sub-config for a provider that was not selected is skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so both
// providers' API keys were required at once and no config could load.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(c.Provider)

	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "OpenAI" and " openai " while NewLLMProvider built them.
			//
			// The sentinel is wrapped for its text, not for errors.Is:
			// ozzo's validation.Errors is a map with no Unwrap, so what
			// reaches the caller from here is a string. NewLLMProvider
			// checks the same list before this runs, which is what makes
			// errors.Is(err, ErrUnknownProvider) hold for a constructor.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "llm provider %q", c.Provider)
			}

			return nil
		})),
		validation.Field(&c.OpenAI, validation.Skip.When(provider != ProviderOpenAI), validation.Required),
		validation.Field(&c.Anthropic, validation.Skip.When(provider != ProviderAnthropic), validation.Required),
	)
}

// NewLLMProvider provides an LLM provider based on config.
//
// The config is validated here rather than trusted to have been validated
// upstream: the provider sub-configs are what carry the API keys, and skipping
// their rules meant a deployment that named openai and supplied nothing got as
// far as its first completion before finding out.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *openai.Provider into a
// non-nil llm.Provider on the error path, and a caller testing the result against
// nil would find a provider that panics on first use.
func (c *Config) NewLLMProvider(ctx context.Context, opts ...Option) (llm.Provider, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if c == nil {
		return nil, errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(c.Provider, providers, "llm provider")
	if err != nil {
		return nil, err
	}

	if err = c.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating llm config")
	}

	switch provider {
	case ProviderOpenAI:
		p, provErr := openai.NewProvider(c.OpenAI, openai.WithLogger(logger), openai.WithTracerProvider(tracerProvider), openai.WithMetricsProvider(metricsProvider))
		if provErr != nil {
			return nil, provErr
		}

		return p, nil
	case ProviderAnthropic:
		p, provErr := anthropic.NewProvider(c.Anthropic, anthropic.WithLogger(logger), anthropic.WithTracerProvider(tracerProvider), anthropic.WithMetricsProvider(metricsProvider))
		if provErr != nil {
			return nil, provErr
		}

		return p, nil
	case ProviderNoop:
		return llmnoop.NewProvider(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "llm provider %q", c.Provider)
	}
}
