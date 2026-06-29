package multisource

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v2/analytics"
	analyticsmock "github.com/primandproper/platform-go/v2/analytics/mock"
	"github.com/primandproper/platform-go/v2/analytics/noop"
	"github.com/primandproper/platform-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var errArbitrary = errors.New("arbitrary")

// newRecordingReporter builds a MultiSourceEventReporter with a RecordingObserver
// swapped in, so a test can both drive the tracking methods and assert which
// fields it observed.
func newRecordingReporter(t *testing.T, reporters map[string]analytics.EventReporter) (*MultiSourceEventReporter, *observability.RecordingObserver) {
	t.Helper()

	m := NewMultiSourceEventReporter(reporters, nil, nil)
	must.NotNil(t, m)

	obs := observability.NewRecordingObserver()
	m.o11y = obs

	return m, obs
}

func TestNewMultiSourceEventReporter(T *testing.T) {
	T.Parallel()

	T.Run("with nil reporters map", func(t *testing.T) {
		t.Parallel()

		r := NewMultiSourceEventReporter(nil, nil, nil)
		must.NotNil(t, r)
		test.NotNil(t, r.reporters)
	})

	T.Run("with populated reporters map", func(t *testing.T) {
		t.Parallel()

		reporters := map[string]analytics.EventReporter{
			"ios": noop.NewEventReporter(),
		}
		r := NewMultiSourceEventReporter(reporters, nil, nil)
		must.NotNil(t, r)
		test.MapLen(t, 1, r.reporters)
	})
}

func TestMultiSourceEventReporter_getReporter(T *testing.T) {
	T.Parallel()

	T.Run("returns reporter for known source", func(t *testing.T) {
		t.Parallel()

		expected := noop.NewEventReporter()
		reporters := map[string]analytics.EventReporter{
			"ios": expected,
		}
		m := NewMultiSourceEventReporter(reporters, nil, nil)

		got := m.getReporter("ios")
		test.Eq(t, expected, got)
	})

	T.Run("returns noop for unknown source", func(t *testing.T) {
		t.Parallel()

		m := NewMultiSourceEventReporter(nil, nil, nil)

		got := m.getReporter("unknown")
		test.NotNil(t, got)
	})

	T.Run("returns noop when reporter is nil in map", func(t *testing.T) {
		t.Parallel()

		reporters := map[string]analytics.EventReporter{
			"ios": nil,
		}
		m := NewMultiSourceEventReporter(reporters, nil, nil)

		got := m.getReporter("ios")
		test.NotNil(t, got)
	})
}

func TestMultiSourceEventReporter_TrackEvent(T *testing.T) {
	T.Parallel()

	T.Run("delegates to correct reporter", func(t *testing.T) {
		t.Parallel()

		mockReporter := &analyticsmock.EventReporterMock{
			EventOccurredFunc: func(_ context.Context, event, userID string, properties map[string]any) error {
				test.EqOp(t, "signup", event)
				test.EqOp(t, "user1", userID)
				test.Eq(t, "ios", properties[SourcePropertyKey])
				test.Eq(t, "pro", properties["plan"])
				return nil
			},
		}

		reporters := map[string]analytics.EventReporter{
			"ios": mockReporter,
		}
		m, obs := newRecordingReporter(t, reporters)

		err := m.TrackEvent(t.Context(), "ios", "signup", "user1", map[string]any{"plan": "pro"})
		test.NoError(t, err)

		test.SliceLen(t, 1, mockReporter.EventOccurredCalls())

		obs.ObservedOperationWithData(t, map[string]any{
			"source":  "ios",
			"event":   "signup",
			"user_id": "user1",
		})
	})

	T.Run("uses noop for unknown source", func(t *testing.T) {
		t.Parallel()

		m := NewMultiSourceEventReporter(nil, nil, nil)

		err := m.TrackEvent(context.Background(), "unknown", "signup", "user1", nil)
		test.NoError(t, err)
	})

	T.Run("records values even when reporter errors", func(t *testing.T) {
		t.Parallel()

		mockReporter := &analyticsmock.EventReporterMock{
			EventOccurredFunc: func(_ context.Context, _, _ string, _ map[string]any) error {
				return errArbitrary
			},
		}

		reporters := map[string]analytics.EventReporter{
			"ios": mockReporter,
		}
		m, obs := newRecordingReporter(t, reporters)

		err := m.TrackEvent(t.Context(), "ios", "signup", "user1", nil)
		test.Error(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			"source":  "ios",
			"event":   "signup",
			"user_id": "user1",
		})
	})
}

func TestMultiSourceEventReporter_TrackAnonymousEvent(T *testing.T) {
	T.Parallel()

	T.Run("delegates to correct reporter", func(t *testing.T) {
		t.Parallel()

		mockReporter := &analyticsmock.EventReporterMock{
			EventOccurredAnonymousFunc: func(_ context.Context, event, anonymousID string, properties map[string]any) error {
				test.EqOp(t, "page_view", event)
				test.EqOp(t, "anon1", anonymousID)
				test.Eq(t, "web", properties[SourcePropertyKey])
				return nil
			},
		}

		reporters := map[string]analytics.EventReporter{
			"web": mockReporter,
		}
		m, obs := newRecordingReporter(t, reporters)

		err := m.TrackAnonymousEvent(t.Context(), "web", "page_view", "anon1", map[string]any{})
		test.NoError(t, err)

		test.SliceLen(t, 1, mockReporter.EventOccurredAnonymousCalls())

		obs.ObservedOperationWithData(t, map[string]any{
			"source":       "web",
			"event":        "page_view",
			"anonymous_id": "anon1",
		})
	})
}

func Test_withSourceProperty(T *testing.T) {
	T.Parallel()

	T.Run("adds source to nil properties", func(t *testing.T) {
		t.Parallel()

		result := withSourceProperty("ios", nil)
		test.Eq(t, "ios", result[SourcePropertyKey])
		test.MapLen(t, 1, result)
	})

	T.Run("adds source to existing properties without mutation", func(t *testing.T) {
		t.Parallel()

		original := map[string]any{"key": "value"}
		result := withSourceProperty("web", original)

		test.Eq(t, "web", result[SourcePropertyKey])
		test.Eq(t, "value", result["key"])
		test.MapLen(t, 2, result)

		// original should not be mutated
		_, exists := original[SourcePropertyKey]
		test.False(t, exists)
	})
}
