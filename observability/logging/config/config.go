// Package loggingcfg selects and builds a logging.Logger from configuration:
// zerolog, zap, slog, the OTel-exporting slog, or none at all.
//
// It is one of the four pillar config packages, none of which offer the
// WithPillars option their siblings do — observability imports them to build a
// Pillars, so they cannot take one. This one goes further and declares no
// Option type at all: the thing it builds is the logger every other constructor
// would have been handed.
//
// The empty provider and "noop" both select no logging, and that opt-out stays
// supported. What is not supported is a provider name this package does not
// recognize, which used to disable logging silently and was indistinguishable
// from asking for it.
package loggingcfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/logging/otelgrpc"
	"github.com/primandproper/platform-go/v13/observability/logging/slog"
	"github.com/primandproper/platform-go/v13/observability/logging/zap"
	"github.com/primandproper/platform-go/v13/observability/logging/zerolog"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderZerolog indicates you'd like to use the zerolog logger.
	ProviderZerolog = "zerolog"
	// ProviderZap indicates you'd like to use the zap logger.
	ProviderZap = "zap"
	// ProviderSlog indicates you'd like to use the slog logger.
	ProviderSlog = "slog"
	// ProviderOtelSlog indicates you'd like to use the otel-enabled slog logger.
	ProviderOtelSlog = "otelslog"
	// ProviderNoop, and the empty string, select no logging at all. That is the
	// deliberate opt-out and stays supported; what is no longer supported is a
	// provider name this package does not recognize, which used to disable
	// logging silently and looked exactly like the opt-out.
	ProviderNoop = "noop"
)

// providers are every provider this package implements, plus the empty string,
// which selects the noop logger — the deliberate opt-out. Validation and
// NewLogger both read it.
var providers = []string{"", ProviderNoop, ProviderZerolog, ProviderZap, ProviderSlog, ProviderOtelSlog}

type (
	// Config configures a Logger.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		ServiceName string           `env:"SERVICE_NAME" json:"serviceName,omitempty" yaml:"serviceName,omitempty"`
		Level       logging.Level    `env:"LEVEL"        json:"level,omitempty"       yaml:"level,omitempty"`
		OtelSlog    *otelgrpc.Config `env:",init"        envPrefix:"OTEL_SLOG_"       json:"otelslog,omitempty"    yaml:"otelslog,omitempty"`
		Provider    string           `env:"PROVIDER"     json:"provider,omitempty"    yaml:"provider,omitempty"`
	}
)

// ValidateWithContext validates the config struct.
//
// The sub-config for a provider that was not selected is skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so the
// unselected provider's own rules were enforced and a service logging with slog
// could not load.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	// Release the sub-config env parsing's ",init" allocated and nothing filled
	// in, so the rule below reads "the operator configured this" rather than
	// "env parsing ran". Without it a logger naming no provider at all fails
	// validation on an otelslog endpoint nobody asked for, which is every
	// config that has been through env.Parse. The three sibling pillars each
	// do the same for theirs.
	cfgnorm.ZeroToNil(&cfg.OtelSlog)

	provider := cfgnorm.Provider(cfg.Provider)

	return validation.ValidateStructWithContext(ctx, cfg,
		// Required only for the provider that sends it anywhere. It was
		// unconditional, and unreachable, until NewLogger started calling this;
		// reachable and unconditional would have made a service that logs to
		// stdout with zerolog name a service to nobody.
		validation.Field(&cfg.ServiceName, validation.Required.When(provider == ProviderOtelSlog)),
		validation.Field(&cfg.Level, validation.By(validateLevel)),
		validation.Field(&cfg.Provider, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "Zerolog" and " slog " while NewLogger built them.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "logging provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.OtelSlog, validation.Skip.When(provider != ProviderOtelSlog), validation.Required),
	)
}

// validateLevel accepts the zero Level — which every implementation reads as
// InfoLevel — or one of the known levels.
func validateLevel(value any) error {
	lvl, ok := value.(logging.Level)
	if !ok || lvl == "" || lvl.Valid() {
		return nil
	}

	return validation.NewError("validation_invalid_log_level", "must be a valid log level")
}

// NewLogger builds a logger according to the provided config.
//
// The fallible providers are built into a variable and returned only once their
// error is known to be nil. The backend constructors return their own concrete
// types, so assigning one straight into the logging.Logger result would leave a
// nil *zap.Logger as a non-nil logging.Logger alongside the error — a value a
// caller's != nil check accepts and the first Info panics on.
func (cfg *Config) NewLogger(ctx context.Context) (logging.Logger, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "logging provider")
	if err != nil {
		return nil, err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating logging config")
	}

	switch provider {
	case ProviderZerolog:
		return zerolog.NewZerologLogger(cfg.Level), nil
	case ProviderZap:
		logger, zapErr := zap.NewZapLogger(cfg.Level)
		if zapErr != nil {
			return nil, zapErr
		}

		return logger, nil
	case ProviderSlog:
		return slog.NewSlogLogger(cfg.Level), nil
	case ProviderOtelSlog:
		logger, otelErr := otelgrpc.NewOtelSlogLogger(ctx, cfg.Level, cfg.ServiceName, cfg.OtelSlog)
		if otelErr != nil {
			return nil, otelErr
		}

		return logger, nil
	case "", ProviderNoop:
		return loggingnoop.NewLogger(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "logging provider %q", cfg.Provider)
	}
}
