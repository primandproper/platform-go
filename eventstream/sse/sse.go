package sse

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/eventstream"
	"github.com/primandproper/platform-go/observability/tracing"
)

var (
	_ eventstream.EventStreamUpgrader = (*Upgrader)(nil)
	_ eventstream.EventStream         = (*sseStream)(nil)
)

// Upgrader upgrades HTTP connections to SSE event streams.
type Upgrader struct {
	tracer tracing.Tracer
}

// NewUpgrader creates a new SSE Upgrader.
func NewUpgrader(tracerProvider tracing.TracerProvider) *Upgrader {
	return &Upgrader{
		tracer: tracing.NewNamedTracer(tracerProvider, "sse_stream"),
	}
}

// UpgradeToEventStream upgrades an HTTP connection to a unidirectional SSE event stream.
func (u *Upgrader) UpgradeToEventStream(w http.ResponseWriter, r *http.Request) (eventstream.EventStream, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported by response writer")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())

	return &sseStream{
		w:       w,
		flusher: flusher,
		cancel:  cancel,
		done:    ctx.Done(),
		tracer:  u.tracer,
	}, nil
}

type sseStream struct {
	tracer  tracing.Tracer
	w       http.ResponseWriter
	flusher http.Flusher
	cancel  context.CancelFunc
	done    <-chan struct{}
	mu      sync.Mutex
}

// Send writes an event to the SSE stream in standard SSE format.
func (s *sseStream) Send(ctx context.Context, event *eventstream.Event) error {
	_, span := s.tracer.StartCustomSpan(ctx, "sse_send")
	defer span.End()

	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case <-s.done:
		return errors.New("stream closed")
	default:
	}

	if event.Type != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", event.Type); err != nil {
			return errors.Wrap(err, "writing event type")
		}
	}

	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", event.Payload); err != nil {
		return errors.Wrap(err, "writing event data")
	}

	s.flusher.Flush()

	return nil
}

// Done returns a channel that closes when the stream terminates.
func (s *sseStream) Done() <-chan struct{} {
	return s.done
}

// Close terminates the SSE stream.
func (s *sseStream) Close() error {
	s.cancel()
	return nil
}
