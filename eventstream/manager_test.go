package eventstream

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var errStub = errors.New("stub error")

// newRecordingManager builds a StreamManager with a RecordingObserver swapped in,
// so a test can both drive a method and assert which fields it observed.
func newRecordingManager(t *testing.T) (*StreamManager[EventStream], *observability.RecordingObserver) {
	t.Helper()

	m := NewStreamManager[EventStream](nil, nil)
	obs := observability.NewRecordingObserver()
	m.o11y = obs

	return m, obs
}

// mockStream implements EventStream for testing.
type mockStream struct {
	done   chan struct{}
	events []*Event
	mu     sync.Mutex
	closed bool
}

func newMockStream() *mockStream {
	return &mockStream{done: make(chan struct{})}
}

func (m *mockStream) Send(_ context.Context, event *Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockStream) Done() <-chan struct{} {
	return m.done
}

func (m *mockStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		close(m.done)
	}
	return nil
}

func (m *mockStream) sentEvents() []*Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Event, len(m.events))
	copy(out, m.events)
	return out
}

func TestNewStreamManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m := NewStreamManager[EventStream](nil, nil)
		must.NotNil(t, m)

		obs := observability.NewRecordingObserver()
		m.o11y = obs

		test.False(t, m.GroupHasStreams(ctx, "any"))
		test.EqOp(t, 0, m.GetStreamCount(ctx, "any"))
		test.Nil(t, m.Get(ctx, "any", "any"))
		test.SliceEmpty(t, m.GetGroupStreams(ctx, "any"))

		// On an empty manager, the read methods still observe the group they were
		// queried with, and the counting methods observe a zero length.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":  "any",
			"member_id": "any",
		})
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "any",
			keys.LengthKey: 0,
		})
	})
}

func TestStreamManager_Add_Get_Remove(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		stream := newMockStream()
		m, obs := newRecordingManager(t)

		m.Add(ctx, "g1", "m1", stream)
		test.True(t, m.GroupHasStreams(ctx, "g1"))
		test.EqOp(t, 1, m.GetStreamCount(ctx, "g1"))
		test.True(t, EventStream(stream) == m.Get(ctx, "g1", "m1"))
		test.SliceLen(t, 1, m.GetGroupStreams(ctx, "g1"))

		m.Remove(ctx, "g1", "m1")
		test.False(t, m.GroupHasStreams(ctx, "g1"))
		test.EqOp(t, 0, m.GetStreamCount(ctx, "g1"))
		test.Nil(t, m.Get(ctx, "g1", "m1"))
		test.SliceEmpty(t, m.GetGroupStreams(ctx, "g1"))

		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":  "g1",
			"member_id": "m1",
		})
	})
}

func TestStreamManager_Remove_empties_group(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)
		m.Add(ctx, "g1", "m1", newMockStream())
		m.Add(ctx, "g1", "m2", newMockStream())
		test.EqOp(t, 2, m.GetStreamCount(ctx, "g1"))

		m.Remove(ctx, "g1", "m1")
		test.EqOp(t, 1, m.GetStreamCount(ctx, "g1"))
		test.NotNil(t, m.Get(ctx, "g1", "m2"))

		m.Remove(ctx, "g1", "m2")
		test.False(t, m.GroupHasStreams(ctx, "g1"))
		test.EqOp(t, 0, m.GetStreamCount(ctx, "g1"))

		// GetStreamCount observes group_id alongside the measured length.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			keys.LengthKey: 2,
		})
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			keys.LengthKey: 0,
		})
	})
}

func TestStreamManager_Get_nonexistent(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)
		test.Nil(t, m.Get(ctx, "g1", "m1"))
		test.Nil(t, m.Get(ctx, "", ""))

		// Get observes the looked-up identifiers even when nothing is found.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":  "g1",
			"member_id": "m1",
		})
	})
}

func TestStreamManager_BroadcastToGroup(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		s1 := newMockStream()
		s2 := newMockStream()
		m, obs := newRecordingManager(t)
		m.Add(ctx, "g1", "m1", s1)
		m.Add(ctx, "g1", "m2", s2)

		event := &Event{
			Type:    "test",
			Payload: json.RawMessage(`{"v":"hello"}`),
		}
		m.BroadcastToGroup(ctx, "g1", event)

		test.SliceLen(t, 1, s1.sentEvents())
		test.EqOp(t, "test", s1.sentEvents()[0].Type)
		test.SliceLen(t, 1, s2.sentEvents())
		test.EqOp(t, "test", s2.sentEvents()[0].Type)

		// A broadcast to a populated group observes the group, the event type, and
		// the number of streams it fanned out to.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			"event.type":   "test",
			keys.LengthKey: 2,
		})
	})

	T.Run("empty group", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		m, obs := newRecordingManager(t)

		// Should not panic
		m.BroadcastToGroup(ctx, "nonexistent", &Event{Type: "test"})

		// An empty group never reaches the fan-out, so only group_id and the event
		// type are observed (no length).
		op := obs.ObservedOperationWithData(t, map[string]any{
			"group_id":   "nonexistent",
			"event.type": "test",
		})
		test.MapNotContainsKey(t, op.Values, keys.LengthKey)
	})
}

func TestStreamManager_BroadcastToGroupFiltered(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		s1 := newMockStream()
		s2 := newMockStream()
		m, obs := newRecordingManager(t)
		m.Add(ctx, "g1", "m1", s1)
		m.Add(ctx, "g1", "m2", s2)

		event := &Event{
			Type:    "filtered",
			Payload: json.RawMessage(`"only-m2"`),
		}

		// Only include m2
		m.BroadcastToGroupFiltered(ctx, "g1", event, func(memberID string) bool {
			return memberID == "m2"
		})

		test.SliceEmpty(t, s1.sentEvents())
		test.SliceLen(t, 1, s2.sentEvents())
		test.EqOp(t, "filtered", s2.sentEvents()[0].Type)

		// A filtered broadcast observes the group and the event type.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":   "g1",
			"event.type": "filtered",
		})
	})

	T.Run("none match", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		s1 := newMockStream()
		m, obs := newRecordingManager(t)
		m.Add(ctx, "g1", "m1", s1)

		m.BroadcastToGroupFiltered(ctx, "g1", &Event{Type: "x"}, func(string) bool { return false })

		test.SliceEmpty(t, s1.sentEvents())

		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":   "g1",
			"event.type": "x",
		})
	})

	T.Run("continues past a failing included stream", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		s1 := &failingStream{}
		s2 := newMockStream()
		m, obs := newRecordingManager(t)
		m.Add(ctx, "g1", "m1", s1)
		m.Add(ctx, "g1", "m2", s2)

		// Include every member so the failing stream's Send error is exercised.
		m.BroadcastToGroupFiltered(ctx, "g1", &Event{Type: "filtered"}, func(string) bool { return true })

		// The non-failing stream still receives the event despite s1's error.
		test.SliceLen(t, 1, s2.sentEvents())
		test.EqOp(t, "filtered", s2.sentEvents()[0].Type)

		// The values are observed and the per-stream send failure is recorded on
		// the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"group_id":   "g1",
			"event.type": "filtered",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestStreamManager_SendToMember(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		s1 := newMockStream()
		s2 := newMockStream()
		m, obs := newRecordingManager(t)
		m.Add(ctx, "g1", "m1", s1)
		m.Add(ctx, "g1", "m2", s2)

		event := &Event{Type: "direct", Payload: json.RawMessage(`"hi"`)}
		err := m.SendToMember(ctx, "g1", "m1", event)
		must.NoError(t, err)

		test.SliceLen(t, 1, s1.sentEvents())
		test.EqOp(t, "direct", s1.sentEvents()[0].Type)
		test.SliceEmpty(t, s2.sentEvents())

		// A direct send observes the targeted group, member, and event type.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":   "g1",
			"member_id":  "m1",
			"event.type": "direct",
		})
	})

	T.Run("nonexistent member returns nil", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		m, obs := newRecordingManager(t)

		err := m.SendToMember(ctx, "g1", "m1", &Event{Type: "x"})
		test.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":   "g1",
			"member_id":  "m1",
			"event.type": "x",
		})
	})

	T.Run("nonexistent group returns nil", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		m, obs := newRecordingManager(t)

		err := m.SendToMember(ctx, "g999", "m1", &Event{Type: "x"})
		test.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":   "g999",
			"member_id":  "m1",
			"event.type": "x",
		})
	})
}

func TestStreamManager_GroupHasStreams(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)
		test.False(t, m.GroupHasStreams(ctx, "g1"))

		m.Add(ctx, "g1", "m1", newMockStream())
		test.True(t, m.GroupHasStreams(ctx, "g1"))

		// GroupHasStreams observes the group it was asked about.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id": "g1",
		})
	})
}

func TestStreamManager_GetStreamCount(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)
		test.EqOp(t, 0, m.GetStreamCount(ctx, "g1"))

		m.Add(ctx, "g1", "m1", newMockStream())
		test.EqOp(t, 1, m.GetStreamCount(ctx, "g1"))

		m.Add(ctx, "g1", "m2", newMockStream())
		test.EqOp(t, 2, m.GetStreamCount(ctx, "g1"))

		// Each count observes the group alongside the measured length.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			keys.LengthKey: 0,
		})
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			keys.LengthKey: 2,
		})
	})
}

func TestStreamManager_Remove_nonexistent(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)
		// Should not panic
		m.Remove(ctx, "g1", "m1")

		// Remove observes its identifiers even when the group does not exist.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":  "g1",
			"member_id": "m1",
		})
	})
}

func TestStreamManager_GetGroupStreams(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		s1 := newMockStream()
		s2 := newMockStream()
		m, obs := newRecordingManager(t)
		m.Add(ctx, "g1", "m1", s1)
		m.Add(ctx, "g1", "m2", s2)

		streams := m.GetGroupStreams(ctx, "g1")
		test.SliceLen(t, 2, streams)

		// GetGroupStreams observes the group and the number of streams returned.
		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			keys.LengthKey: 2,
		})
	})

	T.Run("nonexistent group", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)
		streams := m.GetGroupStreams(ctx, "g1")
		test.SliceEmpty(t, streams)

		obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			keys.LengthKey: 0,
		})
	})
}

func TestStreamManager_BroadcastToGroup_with_failing_stream(T *testing.T) {
	T.Parallel()

	T.Run("does not stop on error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		s1 := &failingStream{}
		s2 := newMockStream()
		m, obs := newRecordingManager(t)
		m.Add(ctx, "g1", "m1", s1)
		m.Add(ctx, "g1", "m2", s2)

		event := &Event{Type: "test"}
		m.BroadcastToGroup(ctx, "g1", event)

		// s2 should still receive the event even though s1 failed
		// (we can't guarantee order due to map iteration, but we can check that
		// at least the non-failing stream received it)
		time.Sleep(10 * time.Millisecond)
		test.SliceLen(t, 1, s2.sentEvents())

		// The broadcast still observes its values and fan-out length, and records
		// the single failing stream's error.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			"event.type":   "test",
			keys.LengthKey: 2,
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("records the send error on the operation", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		m, obs := newRecordingManager(t)

		m.Add(ctx, "g1", "m1", &failingStream{})

		m.BroadcastToGroup(ctx, "g1", &Event{Type: "test"})

		op := obs.ObservedOperationWithData(t, map[string]any{
			"group_id":     "g1",
			"event.type":   "test",
			keys.LengthKey: 1,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

// failingStream is a stream that always returns an error on Send.
type failingStream struct{}

func (f *failingStream) Send(context.Context, *Event) error { return errStub }
func (f *failingStream) Done() <-chan struct{}              { return make(chan struct{}) }
func (f *failingStream) Close() error                       { return nil }
