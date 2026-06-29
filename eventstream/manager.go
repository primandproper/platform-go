package eventstream

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

const (
	managerObservabilityName = "event_stream_manager"
)

// StreamManager manages active event streams grouped by group ID and member ID.
type StreamManager[S EventStream] struct {
	o11y    observability.Observer
	streams map[string]map[string]S
	mu      sync.RWMutex
}

// NewStreamManager creates a new StreamManager.
func NewStreamManager[S EventStream](
	tracerProvider tracing.TracerProvider,
	logger logging.Logger,
) *StreamManager[S] {
	return &StreamManager[S]{
		o11y:    observability.NewObserver(managerObservabilityName, logger, tracerProvider),
		streams: make(map[string]map[string]S),
	}
}

// Add registers a stream for a group and member.
func (m *StreamManager[S]) Add(ctx context.Context, groupID, memberID string, stream S) {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("group_id", groupID).Set("member_id", memberID)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.streams[groupID] == nil {
		m.streams[groupID] = make(map[string]S)
	}
	m.streams[groupID][memberID] = stream
}

// Remove removes a stream.
func (m *StreamManager[S]) Remove(ctx context.Context, groupID, memberID string) {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("group_id", groupID).Set("member_id", memberID)

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
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("group_id", groupID).Set("member_id", memberID)

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
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	var streams []S
	if groupStreams, ok := m.streams[groupID]; ok {
		for _, s := range groupStreams {
			streams = append(streams, s)
		}
	}

	op.Set("group_id", groupID).Set(keys.LengthKey, len(streams))

	return streams
}

// BroadcastToGroup sends an event to all streams in a group.
//
// TODO: this is intentionally fire-and-forget; a single stream's Send failure
// shouldn't halt the broadcast. Revisit whether per-stream failures should be
// aggregated and returned (as SendToMember returns its error).
func (m *StreamManager[S]) BroadcastToGroup(ctx context.Context, groupID string, event *Event) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("group_id", groupID).Set("event.type", event.Type)

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		op.Set(keys.LengthKey, len(groupStreams))
		for _, s := range groupStreams {
			if err := s.Send(ctx, event); err != nil {
				op.Acknowledge(err, "sending event to stream")
			}
		}
	}
}

// BroadcastToGroupFiltered sends an event to streams in a group for which includeFunc returns true.
//
// TODO: this is intentionally fire-and-forget; a single stream's Send failure
// shouldn't halt the broadcast. Revisit whether per-stream failures should be
// aggregated and returned (as SendToMember returns its error).
func (m *StreamManager[S]) BroadcastToGroupFiltered(ctx context.Context, groupID string, event *Event, includeFunc func(memberID string) bool) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("group_id", groupID).Set("event.type", event.Type)

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		for memberID, s := range groupStreams {
			if includeFunc(memberID) {
				if err := s.Send(ctx, event); err != nil {
					op.Acknowledge(err, "sending event to stream")
				}
			}
		}
	}
}

// SendToMember sends an event to a specific member in a group.
func (m *StreamManager[S]) SendToMember(ctx context.Context, groupID, memberID string, event *Event) error {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("group_id", groupID).Set("member_id", memberID).Set("event.type", event.Type)

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
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("group_id", groupID)

	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupStreams, ok := m.streams[groupID]; ok {
		return len(groupStreams) > 0
	}
	return false
}

// GetStreamCount returns the number of streams for a group.
func (m *StreamManager[S]) GetStreamCount(ctx context.Context, groupID string) int {
	_, op := m.o11y.Begin(ctx)
	defer op.End()

	m.mu.RLock()
	defer m.mu.RUnlock()

	var count int
	if groupStreams, ok := m.streams[groupID]; ok {
		count = len(groupStreams)
	}

	op.Set("group_id", groupID).Set(keys.LengthKey, count)

	return count
}
