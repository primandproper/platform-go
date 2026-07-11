package circuitbreakingcfg

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/primandproper/platform-go/v4/circuitbreaking"
	"github.com/primandproper/platform-go/v4/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	circuit "github.com/rubyist/circuitbreaker"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Option customizes how a CircuitBreaker is provided.
type Option func(*provideOptions)

type provideOptions struct {
	metricAttributes []attribute.KeyValue
}

func (o *provideOptions) addOptions() []metric.AddOption {
	if len(o.metricAttributes) == 0 {
		return nil
	}

	return []metric.AddOption{metric.WithAttributes(o.metricAttributes...)}
}

// WithMetricAttributes attaches a fixed set of attributes to every metric the
// circuit breaker emits. It is used to distinguish breakers that share counter
// names (for example, tagging a per-key breaker with its partition).
func WithMetricAttributes(attrs ...attribute.KeyValue) Option {
	return func(o *provideOptions) {
		o.metricAttributes = append(o.metricAttributes, attrs...)
	}
}

type Config struct {
	Name                   string  `env:"NAME"                     json:"name"`
	ErrorRate              float64 `env:"ERROR_RATE"               json:"circuitBreakerErrorPercentage"`
	MinimumSampleThreshold uint64  `env:"MINIMUM_SAMPLE_THRESHOLD" json:"circuitBreakerMinimumOccurrenceThreshold"`
}

func (cfg *Config) EnsureDefaults() {
	if cfg.Name == "" {
		cfg.Name = "UNKNOWN"
	}

	if cfg.ErrorRate == 0 {
		cfg.ErrorRate = 100
	}

	if cfg.MinimumSampleThreshold == 0 {
		cfg.MinimumSampleThreshold = 20
	}
}

func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Name, validation.Required),
		validation.Field(&cfg.ErrorRate, validation.Min(0.00), validation.Max(100.0)),
		validation.Field(&cfg.MinimumSampleThreshold),
	)
}

// EnsureCircuitBreaker ensures a valid CircuitBreaker is made available.
func EnsureCircuitBreaker(breaker circuitbreaking.CircuitBreaker) circuitbreaking.CircuitBreaker {
	if breaker == nil {
		slog.Info("NOOP CircuitBreaker implementation in use.")
		return noop.NewCircuitBreaker()
	}

	return breaker
}

type baseImplementation struct {
	circuitBreaker *circuit.Breaker
}

func (b *baseImplementation) Failed() {
	b.circuitBreaker.Fail()
}

func (b *baseImplementation) Succeeded() {
	b.circuitBreaker.Success()
}

func (b *baseImplementation) CanProceed() bool {
	return b.circuitBreaker.Ready()
}

func (b *baseImplementation) CannotProceed() bool {
	return !b.circuitBreaker.Ready()
}

// NewCircuitBreaker provides a CircuitBreaker.
func (cfg *Config) NewCircuitBreaker(ctx context.Context, logger logging.Logger, metricsProvider metrics.Provider, opts ...Option) (circuitbreaking.CircuitBreaker, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	options := &provideOptions{}
	for _, opt := range opts {
		opt(options)
	}

	// Apply defaults before validating: otherwise an unset NAME (the common case)
	// fails the Required check and silently degrades to a noop breaker — protection
	// that looks wired but does nothing. Defaulting first makes the "UNKNOWN" name
	// take effect and validation pass.
	cfg.EnsureDefaults()

	logger = logging.EnsureLogger(logger).WithValue("circuit_breaker", cfg.Name)

	if err := cfg.ValidateWithContext(ctx); err != nil {
		logger.Error("invalid config passed, providing noop circuit breaker", err)
		return noop.NewCircuitBreaker(), nil
	}

	metricsProvider = metrics.EnsureMetricsProvider(metricsProvider)

	brokenCounter, err := metricsProvider.NewInt64Counter(fmt.Sprintf("%s_circuit_breaker_tripped", cfg.Name))
	if err != nil {
		return nil, err
	}

	failureCounter, err := metricsProvider.NewInt64Counter(fmt.Sprintf("%s_circuit_breaker_failed", cfg.Name))
	if err != nil {
		return nil, err
	}

	resetCounter, err := metricsProvider.NewInt64Counter(fmt.Sprintf("%s_circuit_breaker_reset", cfg.Name))
	if err != nil {
		return nil, err
	}

	cb := circuit.NewBreakerWithOptions(&circuit.Options{
		ShouldTrip: func(cb *circuit.Breaker) bool {
			// cb.ErrorRate() is a fraction (0.9 == 90%); cfg.ErrorRate is a percentage
			// (0–100), so convert before comparing or any configured rate >1 is unreachable.
			return uint64(cb.Failures()+cb.Successes()) >= cfg.MinimumSampleThreshold && cb.ErrorRate() >= cfg.ErrorRate/100.0
		},
		WindowTime:    circuit.DefaultWindowTime,
		WindowBuckets: circuit.DefaultWindowBuckets,
	})

	events := cb.Subscribe()

	go handleCircuitBreakerEvents(ctx, logger, events, failureCounter, resetCounter, brokenCounter, options.addOptions()...)

	return &baseImplementation{
		circuitBreaker: cb,
	}, nil
}

// NewCircuitBreaker provides a CircuitBreaker from config.
func NewCircuitBreaker(ctx context.Context, cfg *Config, logger logging.Logger, metricsProvider metrics.Provider, opts ...Option) (circuitbreaking.CircuitBreaker, error) {
	return cfg.NewCircuitBreaker(ctx, logger, metricsProvider, opts...)
}

func handleCircuitBreakerEvents(
	ctx context.Context,
	logger logging.Logger,
	events <-chan circuit.BreakerEvent,
	failureCounter,
	resetCounter,
	brokenCounter metrics.Int64Counter,
	addOptions ...metric.AddOption,
) {
	// Exit when the caller's context is canceled so this goroutine doesn't leak for
	// the life of the process (one per breaker). The breaker's event channel is
	// buffered and drops on overflow, so abandoning it here is safe.
	for {
		select {
		case <-ctx.Done():
			return
		case be, ok := <-events:
			if !ok {
				return
			}
			switch be {
			case circuit.BreakerTripped:
				brokenCounter.Add(ctx, 1, addOptions...)
			case circuit.BreakerReset:
				resetCounter.Add(ctx, 1, addOptions...)
			case circuit.BreakerFail:
				failureCounter.Add(ctx, 1, addOptions...)
			case circuit.BreakerReady:
				logger.Debug("circuit breaker is ready")
			}
		}
	}
}
