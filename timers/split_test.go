package timers

import (
	"testing"
	"time"

	"github.com/shoenig/test"
)

// byHolder is what turns a batch of Due values into the statements the split
// corpus runs, one per claim: the grouping has to keep every key with the claim
// that holds it, keep each claim's keys in the lock order they arrived in, and
// come out in the same order however the caller built the batch.
func TestByHolder(T *testing.T) {
	T.Parallel()

	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

	T.Run("groups each claim's keys in key order, claims in name order", func(t *testing.T) {
		t.Parallel()

		refs := sortAndDedupeFirings([]firingRef{
			{key: "d", runAt: at, leasedBy: "claim-b"},
			{key: "a", runAt: at, leasedBy: "claim-b"},
			{key: "c", runAt: at, leasedBy: "claim-a"},
			{key: "b", runAt: at, leasedBy: "claim-a"},
			{key: "a", runAt: at, leasedBy: "claim-a"},
		})

		holders, keys := byHolder(refs)

		test.Eq(t, []string{"claim-a", "claim-b"}, holders)
		test.Eq(t, map[string][]string{
			"claim-a": {"a", "b", "c"},
			"claim-b": {"a", "d"},
		}, keys)
	})

	// Two firings of one key under one claim differ only in the instant, and
	// the name fences the instant on these engines, so they are one row and
	// the key is named once.
	T.Run("names a key once per claim whatever instants it was given", func(t *testing.T) {
		t.Parallel()

		refs := sortAndDedupeFirings([]firingRef{
			{key: "k", runAt: at.Add(time.Minute), leasedBy: "claim"},
			{key: "k", runAt: at, leasedBy: "claim"},
		})

		holders, keys := byHolder(refs)

		test.Eq(t, []string{"claim"}, holders)
		test.Eq(t, map[string][]string{"claim": {"k"}}, keys)
	})

	// A Due nothing handed out carries the empty name, and it is still a claim
	// to address: a statement fenced on it matches nothing, which is the answer
	// the Postgres triples give too.
	T.Run("keeps the empty name as a claim of its own", func(t *testing.T) {
		t.Parallel()

		holders, keys := byHolder([]firingRef{{key: "k", runAt: at, leasedBy: ""}})

		test.Eq(t, []string{""}, holders)
		test.Eq(t, map[string][]string{"": {"k"}}, keys)
	})

	T.Run("returns nothing for nothing", func(t *testing.T) {
		t.Parallel()

		holders, keys := byHolder(nil)

		test.SliceEmpty(t, holders)
		test.MapEmpty(t, keys)
	})
}

// A stored instant is never earlier than the one the caller named: the
// nanoseconds a time.Time carries past the microsecond are rounded up, never
// to the nearest.
func TestRoundUpToMicrosecond(T *testing.T) {
	T.Parallel()

	base := time.Date(2026, 9, 25, 12, 0, 0, 700_000_000, time.UTC)

	for name, tc := range map[string]struct {
		in, want time.Time
	}{
		"an instant on the microsecond is left alone": {in: base, want: base},
		"one nanosecond past goes to the next":        {in: base.Add(time.Nanosecond), want: base.Add(time.Microsecond)},
		"just short of the next goes to it":           {in: base.Add(999 * time.Nanosecond), want: base.Add(time.Microsecond)},
		"before the epoch rounds later, not nearer zero": {
			in:   time.Unix(-1, 500),
			want: time.Unix(-1, 1000),
		},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			got := roundUpToMicrosecond(tc.in)

			test.True(t, got.Equal(tc.want), test.Sprintf("rounded %s to %s, want %s", tc.in, got, tc.want))
			test.False(t, got.Before(tc.in))
		})
	}
}
