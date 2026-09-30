package timers

import (
	stderrors "errors"
	"strings"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSortAndDedupeTimers(T *testing.T) {
	T.Parallel()

	first := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	// The sort is the lock ordering the whole design rests on: ON CONFLICT DO
	// UPDATE locks each conflicting row as the source reaches it, so two
	// overlapping batches arriving in different orders deadlock.
	T.Run("orders by key", func(t *testing.T) {
		t.Parallel()

		out := sortAndDedupeTimers([]encodedTimer{
			{key: "c", runAt: first},
			{key: "a", runAt: first},
			{key: "b", runAt: first},
		})

		must.SliceLen(t, 3, out)
		test.EqOp(t, "a", out[0].key)
		test.EqOp(t, "b", out[1].key)
		test.EqOp(t, "c", out[2].key)
	})

	// Not merely an optimization: ON CONFLICT DO UPDATE refuses to touch the
	// same row twice in one statement, so a caller who names a key twice would
	// otherwise lose the whole batch alongside it.
	T.Run("collapses a repeated key onto its last occurrence", func(t *testing.T) {
		t.Parallel()

		out := sortAndDedupeTimers([]encodedTimer{
			{key: "a", runAt: first},
			{key: "a", runAt: second, payload: []byte("later")},
		})

		must.SliceLen(t, 1, out)
		test.EqOp(t, second, out[0].runAt)
		test.Eq(t, []byte("later"), out[0].payload)
	})
}

func TestSortAndDedupeFirings(T *testing.T) {
	T.Parallel()

	first := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	T.Run("orders by key then instant", func(t *testing.T) {
		t.Parallel()

		out := sortAndDedupeFirings([]firingRef{
			{key: "b", runAt: first},
			{key: "a", runAt: second},
			{key: "a", runAt: first},
		})

		must.SliceLen(t, 3, out)
		test.EqOp(t, "a", out[0].key)
		test.EqOp(t, first, out[0].runAt)
		test.EqOp(t, "a", out[1].key)
		test.EqOp(t, second, out[1].runAt)
		test.EqOp(t, "b", out[2].key)
	})

	// At most one of them can match the row, and which one is the question the
	// fence exists to answer — so neither may be dropped for the other.
	T.Run("keeps two instants of one key", func(t *testing.T) {
		t.Parallel()

		out := sortAndDedupeFirings([]firingRef{
			{key: "a", runAt: first},
			{key: "a", runAt: second},
		})

		test.SliceLen(t, 2, out)
	})

	T.Run("drops an exact repeat", func(t *testing.T) {
		t.Parallel()

		out := sortAndDedupeFirings([]firingRef{
			{key: "a", runAt: first},
			{key: "a", runAt: first},
		})

		test.SliceLen(t, 1, out)
	})
}

func TestSortAndDedupe(T *testing.T) {
	T.Parallel()

	T.Run("orders and collapses", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []string{"a", "b", "c"}, sortAndDedupe([]string{"c", "a", "b", "a"}))
	})
}

func TestTimers_ScheduleValidation(T *testing.T) {
	T.Parallel()

	// The zero time is what a forgotten assignment looks like, so admitting it
	// as "now" would turn one into a timer that fires immediately — the one
	// outcome a scheduler must never produce by accident.
	T.Run("rejects a timer with no instant", func(t *testing.T) {
		t.Parallel()

		set, err := New[string](t.Context(), validConfig(), postgresClient())
		must.NoError(t, err)

		test.True(t, stderrors.Is(set.Schedule(t.Context(), noTx(), Timer[string]{Key: "a"}), ErrZeroRunAt))
	})

	// The transaction is the caller's proof that the timer lands or rolls back
	// with its subject, so a write handed none is refused rather than run on a
	// handle of the set's own choosing.
	T.Run("rejects a write with no transaction", func(t *testing.T) {
		t.Parallel()

		set, err := New[string](t.Context(), validConfig(), postgresClient())
		must.NoError(t, err)

		at := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)

		test.ErrorIs(t, set.Schedule(t.Context(), nil, Timer[string]{Key: "a", RunAt: at}), ErrNilTransaction)
		test.ErrorIs(t, set.ScheduleAt(t.Context(), nil, "a", at, nil), ErrNilTransaction)
		test.ErrorIs(t, set.ScheduleIn(t.Context(), nil, "a", time.Hour, nil), ErrNilTransaction)

		_, err = set.Cancel(t.Context(), nil, "a")
		test.ErrorIs(t, err, ErrNilTransaction)
	})

	T.Run("rejects an oversized payload", func(t *testing.T) {
		t.Parallel()

		set, err := New[string](t.Context(), validConfig(), postgresClient())
		must.NoError(t, err)

		err = set.Schedule(t.Context(), noTx(), Timer[string]{
			Key:     "a",
			RunAt:   time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC),
			Payload: make([]byte, MaxPayloadSize+1),
		})

		test.True(t, stderrors.Is(err, ErrPayloadTooLarge))
	})

	// A malformed key must fail before the batch is built, so the caller learns
	// which key it was rather than getting a bind error from the driver.
	T.Run("rejects a malformed key", func(t *testing.T) {
		t.Parallel()

		set, err := New[string](t.Context(), validConfig(), postgresClient())
		must.NoError(t, err)

		err = set.Schedule(t.Context(), noTx(), Timer[string]{
			Key:   strings.Repeat("k", MaxKeyLength+1),
			RunAt: time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC),
		})

		test.True(t, stderrors.Is(err, ErrKeyTooLong))
	})
}
