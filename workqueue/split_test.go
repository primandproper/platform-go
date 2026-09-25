package workqueue

import (
	"testing"

	"github.com/shoenig/test"
)

// byHolder is what turns a batch of Items into the statements the split corpus
// runs, one per claim: the grouping has to keep every key with the claim that
// holds it, keep each claim's keys in the lock order they arrived in, and come
// out in the same order however the caller built the batch.
func TestByHolder(T *testing.T) {
	T.Parallel()

	T.Run("groups each claim's keys in key order, claims in name order", func(t *testing.T) {
		t.Parallel()

		refs := sortAndDedupeItems([]itemRef{
			{key: "d", leasedBy: "claim-b"},
			{key: "a", leasedBy: "claim-b"},
			{key: "c", leasedBy: "claim-a"},
			{key: "b", leasedBy: "claim-a"},
			{key: "a", leasedBy: "claim-a"},
		})

		holders, keys := byHolder(refs)

		test.Eq(t, []string{"claim-a", "claim-b"}, holders)
		test.Eq(t, map[string][]string{
			"claim-a": {"a", "b", "c"},
			"claim-b": {"a", "d"},
		}, keys)
	})

	// An Item nothing handed out carries the empty name, and it is still a
	// claim to address: a statement fenced on it matches nothing, which is the
	// answer the Postgres pairs give too.
	T.Run("keeps the empty name as a claim of its own", func(t *testing.T) {
		t.Parallel()

		holders, keys := byHolder([]itemRef{{key: "k", leasedBy: ""}})

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
