package sse

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/eventstream"
	"github.com/primandproper/platform-go/v3/observability"
	"github.com/primandproper/platform-go/v3/observability/keys"
	"github.com/primandproper/platform-go/v3/observability/tracing"
)

const (
	name = "sse_stream"
)

var (
	_ eventstream.EventStreamUpgrader = (*Upgrader)(nil)
	_ eventstream.EventStream         = (*sseStream)(nil)
)

// Upgrader upgrades HTTP connections to SSE event streams.
type Upgrader struct {
	o11y observability.Observer
}

// NewUpgrader creates a new SSE Upgrader.
func NewUpgrader(tracerProvider tracing.TracerProvider) *Upgrader {
	return &Upgrader{
		o11y: observability.NewObserver(name, nil, tracerProvider),
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
		o11y:    u.o11y,
	}, nil
}

type sseStream struct {
	o11y    observability.Observer
	w       http.ResponseWriter
	flusher http.Flusher
	cancel  context.CancelFunc
	done    <-chan struct{}
	mu      sync.Mutex
}

// Send writes an event to the SSE stream in standard SSE format.
func (s *sseStream) Send(ctx context.Context, event *eventstream.Event) error {
	_, op := s.o11y.BeginCustom(ctx, "sse_send")
	defer op.End()

	op.Set("event.type", event.Type).Set(keys.LengthKey, len(event.Payload))

	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case <-s.done:
		return errors.New("stream closed")
	default:
	}

	if event.Type != "" {
		// Event types are single-line tokens; strip CR/LF so a newline-bearing type
		// can't inject additional SSE fields.
		eventType := strings.NewReplacer("\r", "", "\n", "").Replace(event.Type)
		if _, err := fmt.Fprintf(s.w, "event: %s\n", eventType); err != nil {
			return op.Error(err, "writing event type")
		}
	}

	// Emit the payload as one `data:` line per source line. Splitting on newlines
	// keeps a payload that contains "\n" (or an embedded "event:"/"data:" line) from
	// breaking the SSE framing or injecting control fields. CR and CRLF are
	// normalized to LF first so a lone "\r" can't split a line either.
	normalized := bytes.ReplaceAll(bytes.ReplaceAll(event.Payload, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
	for line := range bytes.SplitSeq(normalized, []byte("\n")) {
		if _, err := fmt.Fprintf(s.w, "data: %s\n", line); err != nil {
			return op.Error(err, "writing event data")
		}
	}
	if _, err := fmt.Fprint(s.w, "\n"); err != nil {
		return op.Error(err, "writing event terminator")
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
