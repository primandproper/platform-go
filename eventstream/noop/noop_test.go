package noop

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v6/eventstream"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewEventStream(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil stream", func(t *testing.T) {
		t.Parallel()

		s := NewEventStream()
		must.NotNil(t, s)
	})
}

func TestEventStream_Send(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		s := NewEventStream()
		err := s.Send(t.Context(), &eventstream.Event{
			Type:    "test",
			Payload: json.RawMessage(`{"key":"value"}`),
		})

		test.NoError(t, err)
	})
}

func TestEventStream_Done(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil channel", func(t *testing.T) {
		t.Parallel()

		s := NewEventStream()
		test.NotNil(t, s.Done())
	})

	T.Run("channel closes after Close", func(t *testing.T) {
		t.Parallel()

		s := NewEventStream()
		must.NoError(t, s.Close())

		select {
		case <-s.Done():
		default:
			t.Fatal("expected Done channel to be closed")
		}
	})
}

func TestEventStream_Close(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		s := NewEventStream()
		test.NoError(t, s.Close())
	})

	T.Run("idempotent", func(t *testing.T) {
		t.Parallel()

		s := NewEventStream()
		test.NoError(t, s.Close())
		test.NoError(t, s.Close())
	})
}

func TestNewBidirectionalEventStream(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil stream", func(t *testing.T) {
		t.Parallel()

		s := NewBidirectionalEventStream()
		must.NotNil(t, s)
	})
}

func TestBidirectionalEventStream_Send(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		s := NewBidirectionalEventStream()
		err := s.Send(t.Context(), &eventstream.Event{
			Type:    "test",
			Payload: json.RawMessage(`{"key":"value"}`),
		})

		test.NoError(t, err)
	})
}

func TestBidirectionalEventStream_Receive(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil channel", func(t *testing.T) {
		t.Parallel()

		s := NewBidirectionalEventStream()
		test.NotNil(t, s.Receive())
	})
}

func TestBidirectionalEventStream_Close(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		s := NewBidirectionalEventStream()
		test.NoError(t, s.Close())
	})
}

func TestNewEventStreamUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil upgrader", func(t *testing.T) {
		t.Parallel()

		u := NewEventStreamUpgrader()
		must.NotNil(t, u)
	})
}

func TestEventStreamUpgrader_UpgradeToEventStream(T *testing.T) {
	T.Parallel()

	T.Run("returns a usable stream", func(t *testing.T) {
		t.Parallel()

		u := NewEventStreamUpgrader()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)

		s, err := u.UpgradeToEventStream(w, r)

		must.NoError(t, err)
		must.NotNil(t, s)
		test.NoError(t, s.Send(t.Context(), &eventstream.Event{Type: "test"}))
		test.NoError(t, s.Close())
	})
}

func TestNewBidirectionalEventStreamUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil upgrader", func(t *testing.T) {
		t.Parallel()

		u := NewBidirectionalEventStreamUpgrader()
		must.NotNil(t, u)
	})
}

func TestBidirectionalEventStreamUpgrader_UpgradeToBidirectionalStream(T *testing.T) {
	T.Parallel()

	T.Run("returns a usable stream", func(t *testing.T) {
		t.Parallel()

		u := NewBidirectionalEventStreamUpgrader()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)

		s, err := u.UpgradeToBidirectionalStream(w, r)

		must.NoError(t, err)
		must.NotNil(t, s)
		test.NoError(t, s.Send(t.Context(), &eventstream.Event{Type: "test"}))
		test.NotNil(t, s.Receive())
		test.NoError(t, s.Close())
	})
}
