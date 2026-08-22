// Package circuitbreakingcfg builds a circuitbreaking.CircuitBreaker from
// configuration. There is one implementation and so no provider to name: what
// is configured here is the trip threshold and the breaker's identity.
//
// The name is the load-bearing field. It is the breaker's identity in every log
// line it writes and the value of the attribute on every counter it records, so
// an unnamed breaker gets a numbered placeholder — enough to keep two of them
// out of one series, and no help to whoever reads that series later. The
// instrument names are exported here for the dashboards that query them.
//
// EnsureCircuitBreaker is the other half of this package: it substitutes a noop
// for a nil breaker and says so through the caller's logger, because a
// component that believes it is protected and is not is exactly what
// circuitbreaking exists to prevent.
package circuitbreakingcfg

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	circuit "github.com/rubyist/circuitbreaker"
	"go.opentelemetry.io/otel/metric"
)

const (
	// DefaultName is what a breaker with no configured name is defaulted to. It
	// is a placeholder, not an identity: see NewCircuitBreaker.
	DefaultName = "UNKNOWN"

	// The instruments every breaker records into. They do not carry the
	// breaker's name — that is NameAttributeKey — so that "how often is anything
	// tripping" is one series to read rather than one per breaker, and so a
	// breaker nobody named cannot create an instrument nobody expected.
	//
	// They are exported for the same reason the attribute key is: a dashboard
	// querying them has to spell them, and a constant is better than a string
	// repeated across a query builder and a test.
	TrippedCounterName = "circuit_breaker_tripped"
	FailedCounterName  = "circuit_breaker_failed"
	ResetCounterName   = "circuit_breaker_reset"
)

// unnamedBreakers numbers the breakers built without a name, so that two of them
// are two series instead of one.
var unnamedBreakers atomic.Uint64

type Config struct {
	Name                   string  `env:"NAME"                     json:"name,omitempty"                                     yaml:"name,omitempty"`
	ErrorRate              float64 `env:"ERROR_RATE"               json:"circuitBreakerErrorPercentage,omitempty"            yaml:"circuitBreakerErrorPercentage,omitempty"`
	MinimumSampleThreshold uint64  `env:"MINIMUM_SAMPLE_THRESHOLD" json:"circuitBreakerMinimumOccurrenceThreshold,omitempty" yaml:"circuitBreakerMinimumOccurrenceThreshold,omitempty"`
}

func (cfg *Config) EnsureDefaults() {
	if cfg.Name == "" {
		cfg.Name = DefaultName
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

// identityFor returns the name a breaker records and logs under.
//
// A configured name is that name. An unconfigured one has been defaulted to the
// shared placeholder by EnsureDefaults, and two breakers nobody named are still
// two breakers — their trips summed together describe neither of them — so each
// gets an ordinal instead. The ordinal is not a name: it says only which unnamed
// breaker this process built first. It is an identity, which is what the shared
// placeholder was not.
func identityFor(configured string) string {
	if configured != DefaultName {
		return configured
	}

	return fmt.Sprintf("%s_%d", DefaultName, unnamedBreakers.Add(1))
}

// EnsureCircuitBreaker ensures a valid CircuitBreaker is made available.
//
// Substituting a noop is worth saying out loud — a component that believes it is
// protected and is not is the whole failure mode this package exists to prevent
// — so pass WithLogger and hear about it. It used to say so through a bare
// slog.Info, which meant a library writing to whatever the process had made
// global rather than to the logger the caller configured, in the format the
// caller configured it in.
func EnsureCircuitBreaker(breaker circuitbreaking.CircuitBreaker, opts ...Option) circuitbreaking.CircuitBreaker {
	if breaker == nil {
		logging.EnsureLogger(newOptions(opts).logger).Info("NOOP CircuitBreaker implementation in use.")

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
//
// Name it. A breaker's name is its identity in every measurement it records and
// every line it logs, and an unnamed one gets a numbered placeholder — enough to
// keep two of them from adding into the same series, and no help at all to
// whoever is looking at the series later.
func (cfg *Config) NewCircuitBreaker(ctx context.Context, opts ...Option) (circuitbreaking.CircuitBreaker, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	options := newOptions(opts)

	// Apply defaults before validating: otherwise an unset NAME (the common case)
	// fails the Required check and silently degrades to a noop breaker — protection
	// that looks wired but does nothing. Defaulting first makes the "UNKNOWN" name
	// take effect and validation pass.
	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating circuit breaker config")
	}

	name := identityFor(cfg.Name)

	logger := logging.EnsureLogger(options.logger).WithValue("circuit_breaker", name)

	metricsProvider := metrics.EnsureMetricsProvider(options.metricsProvider)

	brokenCounter, err := metricsProvider.NewInt64Counter(TrippedCounterName)
	if err != nil {
		return nil, err
	}

	failureCounter, err := metricsProvider.NewInt64Counter(FailedCounterName)
	if err != nil {
		return nil, err
	}

	resetCounter, err := metricsProvider.NewInt64Counter(ResetCounterName)
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

	go handleCircuitBreakerEvents(ctx, logger, events, failureCounter, resetCounter, brokenCounter, options.addOptions(name)...)

	return &baseImplementation{
		circuitBreaker: cb,
	}, nil
}

// NewCircuitBreaker provides a CircuitBreaker from config.
func NewCircuitBreaker(ctx context.Context, cfg *Config, opts ...Option) (circuitbreaking.CircuitBreaker, error) {
	return cfg.NewCircuitBreaker(ctx, opts...)
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
				// Logged, not just counted. A breaker tripping is the moment a
				// dependency stopped being usable and this process started
				// refusing work on its behalf, and reading that off a counter
				// means already suspecting it; the log line is what puts it in
				// front of whoever is reading the logs for another reason.
				logger.Info("circuit breaker tripped")
			case circuit.BreakerReset:
				resetCounter.Add(ctx, 1, addOptions...)
				logger.Info("circuit breaker reset")
			case circuit.BreakerFail:
				// Not logged: a failure is per-request, and a dependency that is
				// down produces one per attempt for as long as it stays down.
				// The trip above is the event; these are the evidence for it.
				failureCounter.Add(ctx, 1, addOptions...)
			case circuit.BreakerReady:
				logger.Debug("circuit breaker is ready")
			}
		}
	}
}
