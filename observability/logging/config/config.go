package loggingcfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v3/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/logging/otelgrpc"
	"github.com/primandproper/platform-go/v3/observability/logging/slog"
	"github.com/primandproper/platform-go/v3/observability/logging/zap"
	"github.com/primandproper/platform-go/v3/observability/logging/zerolog"

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
)

type (
	// Config configures a Logger.
	Config struct {
		_ struct{} `json:"-"`

		ServiceName string           `env:"SERVICE_NAME" json:"serviceName"`
		Level       logging.Level    `env:"LEVEL"        json:"level,omitempty"`
		OtelSlog    *otelgrpc.Config `env:"init"         envPrefix:"OTEL_SLOG_"    json:"otelslog,omitempty"`
		Provider    string           `env:"PROVIDER"     json:"provider,omitempty"`
	}
)

func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.ServiceName, validation.Required),
		validation.Field(&cfg.Level, validation.By(validateLevel)),
		validation.Field(&cfg.Provider, validation.In(ProviderZerolog, ProviderZap, ProviderSlog, ProviderOtelSlog)),
		validation.Field(&cfg.OtelSlog, validation.When(cfg.Provider == ProviderOtelSlog, validation.Required)),
	)
}

// validateLevel accepts a nil/empty Level (the noop path) or one of the known levels.
// logging.Level is a pointer type, so validation.In (reflect.DeepEqual) can't match a
// decoded level against the package singletons — compare by value instead.
func validateLevel(value any) error {
	lvl, ok := value.(logging.Level)
	if !ok || lvl == nil {
		return nil
	}

	for _, known := range logging.AllLevels() {
		if logging.LevelsEqual(lvl, known) {
			return nil
		}
	}

	return validation.NewError("validation_invalid_log_level", "must be a valid log level")
}

// ProvideLogger builds a logger according to the provided config.
func (cfg *Config) ProvideLogger(ctx context.Context) (logger logging.Logger, err error) {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ProviderZerolog:
		logger = zerolog.NewZerologLogger(cfg.Level)
	case ProviderZap:
		logger = zap.NewZapLogger(cfg.Level)
	case ProviderSlog:
		logger = slog.NewSlogLogger(cfg.Level)
	case ProviderOtelSlog:
		logger, err = otelgrpc.NewOtelSlogLogger(ctx, cfg.Level, cfg.ServiceName, cfg.OtelSlog)
	default:
		logger = loggingnoop.NewLogger()
	}

	return logger, err
}
