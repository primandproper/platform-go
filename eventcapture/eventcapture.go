package eventcapture

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

const (
	// DefaultBufferSize caps the in-flight event channel when WithBufferSize
	// is not supplied.
	DefaultBufferSize = 1024
	// DefaultFlushInterval is the flusher tick cadence when WithFlushInterval
	// is not supplied.
	DefaultFlushInterval = 5 * time.Second

	// serviceName names the Recorder's logger, span, and metrics.
	serviceName = "eventcapture"
)

// ErrEventTypeMismatch indicates WithTransform or WithObserver was given a
// function for an event type other than the Recorder's. Option carries no type
// parameter, so the compiler cannot catch this; NewRecorder reports it instead.
var ErrEventTypeMismatch = platformerrors.New("function event type does not match recorder type")

// Sink persists captured records. Calls arrive from the Recorder's single
// flusher goroutine, so implementations need no locking for Write and Flush,
// though Close may race a final flush and should guard itself. Write receives
// whatever record types the composition emits — raw events, aggregate
// rollups — and must not retain the value past the call.
type Sink interface {
	Write(record any) error
	// Flush pushes buffered records toward durable storage; the Recorder
	// calls it on every tick so a tail -f of a file sink stays current.
	Flush() error
	Close() error
}

// Recorder is the bridge between a hot path and a Sink: Record is a
// non-blocking bounded-channel send (a full buffer drops the event and counts
// it — capture never slows a request), and a single flusher goroutine (Run)
// consumes the channel, writing raw events and running the configured hooks.
// See the package documentation for the lifecycle rationale.
type Recorder[E any] struct {
	clock           clock.Clock
	events          chan E
	sink            Sink
	o11y            observability.Observer
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	stop            chan struct{}
	done            chan struct{}
	observe         func(*E)
	onFlush         func(now time.Time, final bool, emit func(record any))
	transform       func(*E) any
	overflow        func() uint64
	writtenCounter  metrics.Int64Counter
	droppedCounter  metrics.Int64Counter
	overflowCounter metrics.Int64Counter
	errCounter      metrics.Int64Counter
	flushHist       metrics.Float64Histogram
	flushInterval   time.Duration
	dropped         atomic.Uint64
	loggedDropped   uint64 // flusher-goroutine only: high-water mark already reported
	raw             bool
	stopOnce        sync.Once
}
