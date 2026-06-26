package eventstream

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
)

const (
	managerObservabilityName = "event_stream_manager"
)

// StreamManager manages active event streams grouped by group ID and member ID.
type StreamManager[S EventStream] struct {
	logger  logging.Logger
	tracer  tracing.Tracer
	streams map[string]map[string]S
	mu      sync.RWMutex
}

// NewStreamManager creates a new StreamManager.
func NewStreamManager[S EventStream](
	tracerProvider tracing.TracerProvider,
	logger logging.Logger,
) *StreamManager[S] {
	return &StreamManager[S]{
		logger:  logging.NewNamedLogger(logger, managerObservabilityName),
		tracer:  tracing.NewNamedTracer(tracerProvider, managerObservabilityName),
		streams: make(map[string]map[string]S),
	}
}

// Add registers a stream for a group and member.
func (m *StreamManager[S]) Add(ctx context.Context, groupID, memberID string, stream S) {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.streams[groupID] == nil {
		m.streams[groupID] = make(map[string]S)
	}
	m.streams[groupID][memberID] = stream
}

// Remove removes a stream.
func (m *StreamManager[S]) Remove(ctx context.Context, groupID, memberID string) {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.Lock()
	defer m.mu.Unlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		delete(groupStreams, memberID)
		if len(groupStreams) == 0 {
			delete(m.streams, groupID)
		}
	}
}

// Get returns a specific stream, or the zero value if not found.
func (m *StreamManager[S]) Get(ctx context.Context, groupID, memberID string) S {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		return groupStreams[memberID]
	}

	var zero S
	return zero
}

// GetGroupStreams returns all streams for a group.
func (m *StreamManager[S]) GetGroupStreams(ctx context.Context, groupID string) []S {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	var streams []S
	if groupStreams, ok := m.streams[groupID]; ok {
		for _, s := range groupStreams {
			streams = append(streams, s)
		}
	}
	return streams
}

// BroadcastToGroup sends an event to all streams in a group.
func (m *StreamManager[S]) BroadcastToGroup(ctx context.Context, groupID string, event *Event) {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		for _, s := range groupStreams {
			if err := s.Send(ctx, event); err != nil {
				observability.AcknowledgeError(err, m.logger, span, "sending event to stream")
			}
		}
	}
}

// BroadcastToGroupFiltered sends an event to streams in a group for which includeFunc returns true.
func (m *StreamManager[S]) BroadcastToGroupFiltered(ctx context.Context, groupID string, event *Event, includeFunc func(memberID string) bool) {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		for memberID, s := range groupStreams {
			if includeFunc(memberID) {
				if err := s.Send(ctx, event); err != nil {
					observability.AcknowledgeError(err, m.logger, span, "sending event to stream")
				}
			}
		}
	}
}

// SendToMember sends an event to a specific member in a group.
func (m *StreamManager[S]) SendToMember(ctx context.Context, groupID, memberID string, event *Event) error {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		if s, found := groupStreams[memberID]; found {
			return s.Send(ctx, event)
		}
	}
	return nil
}

// GroupHasStreams returns whether a group has any active streams.
func (m *StreamManager[S]) GroupHasStreams(ctx context.Context, groupID string) bool {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		return len(groupStreams) > 0
	}
	return false
}

// GetStreamCount returns the number of streams for a group.
func (m *StreamManager[S]) GetStreamCount(ctx context.Context, groupID string) int {
	_, span := m.tracer.StartSpan(ctx)
	defer span.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		return len(groupStreams)
	}
	return 0
}
