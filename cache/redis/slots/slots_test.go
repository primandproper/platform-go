package slots

import (
	"strconv"
	"testing"

	"github.com/shoenig/test/must"
)

func TestSlotRange_Contains(T *testing.T) {
	T.Parallel()

	r := SlotRange{Start: 10, End: 20}

	T.Run("inside the range", func(t *testing.T) {
		t.Parallel()

		must.True(t, r.Contains(15))
	})

	T.Run("inclusive on both ends", func(t *testing.T) {
		t.Parallel()

		must.True(t, r.Contains(10))
		must.True(t, r.Contains(20))
	})

	T.Run("outside the range", func(t *testing.T) {
		t.Parallel()

		must.False(t, r.Contains(9))
		must.False(t, r.Contains(21))
	})
}

func TestNodeSlots_Contains(T *testing.T) {
	T.Parallel()

	n := NodeSlots{
		{Start: 0, End: 10},
		{Start: 100, End: 200},
	}

	T.Run("covered by first range", func(t *testing.T) {
		t.Parallel()

		must.True(t, n.Contains(5))
	})

	T.Run("covered by second range", func(t *testing.T) {
		t.Parallel()

		must.True(t, n.Contains(150))
	})

	T.Run("in the gap between ranges", func(t *testing.T) {
		t.Parallel()

		must.False(t, n.Contains(50))
	})
}

func TestEqualSplit_internal(T *testing.T) {
	T.Parallel()

	T.Run("rejects non-positive node counts", func(t *testing.T) {
		t.Parallel()

		_, err := equalSplit(0)
		must.Error(t, err)

		_, err = equalSplit(-1)
		must.Error(t, err)
	})

	T.Run("rejects more nodes than slots", func(t *testing.T) {
		t.Parallel()

		_, err := equalSplit(SlotCount + 1)
		must.Error(t, err)
	})

	T.Run("splits 16384 across N=3 into canonical ranges", func(t *testing.T) {
		t.Parallel()

		got, err := equalSplit(3)
		must.NoError(t, err)
		must.Eq(t, []NodeSlots{
			{{Start: 0, End: 5460}},
			{{Start: 5461, End: 10921}},
			{{Start: 10922, End: 16383}},
		}, got)
	})

	T.Run("covers every slot exactly once for a range of N", func(t *testing.T) {
		t.Parallel()

		for _, n := range []int{1, 2, 3, 5, 8, 13, 21, 34, 55} {
			nodes, err := equalSplit(n)
			must.NoError(t, err)
			must.Len(t, n, nodes)

			covered := make([]bool, SlotCount)
			for _, ns := range nodes {
				for _, r := range ns {
					for s := r.Start; s <= r.End; s++ {
						must.False(t, covered[s], must.Sprintf("slot %d double-claimed for N=%d", s, n))
						covered[s] = true
					}
				}
			}
			for s, ok := range covered {
				must.True(t, ok, must.Sprintf("slot %d uncovered for N=%d", s, n))
			}
		}
	})
}

func TestDistribute_internal(T *testing.T) {
	T.Parallel()

	T.Run("3 nodes x 1 slot returns the canonical [[0] [1] [2]]", func(t *testing.T) {
		t.Parallel()

		nodes, err := equalSplit(3)
		must.NoError(t, err)

		got, err := distribute("key:version:", "", nodes, 1)
		must.NoError(t, err)
		must.Eq(t, [][]int{{0}, {1}, {2}}, got)
	})

	T.Run("3 nodes x 5 slots returns the pinned distribution", func(t *testing.T) {
		t.Parallel()

		nodes, err := equalSplit(3)
		must.NoError(t, err)

		got, err := distribute("key:version:", "", nodes, 5)
		must.NoError(t, err)
		must.Eq(t, [][]int{
			{0, 4, 8, 10, 11},
			{1, 5, 9, 13, 17},
			{2, 3, 6, 7, 12},
		}, got)

		// All five slots within each node are distinct — verified directly
		// from the pinned values.
		for i, vs := range got {
			seen := make(map[uint16]struct{}, len(vs))
			for _, v := range vs {
				slot := Slot("key:version:" + strconv.Itoa(v))
				_, dup := seen[slot]
				must.False(t, dup, must.Sprintf("slot %d repeated in node %d", slot, i))
				seen[slot] = struct{}{}
			}
		}
	})

	T.Run("skips slots no node owns", func(t *testing.T) {
		t.Parallel()

		// Two nodes covering only the upper half of slot space; distribute
		// must skip candidates that land in the unowned lower half.
		nodes := []NodeSlots{
			{{Start: SlotCount / 2, End: 3 * SlotCount / 4}},
			{{Start: 3*SlotCount/4 + 1, End: SlotCount - 1}},
		}

		got, err := distribute("key:version:", "", nodes, 2)
		must.NoError(t, err)
		must.Eq(t, [][]int{{2, 6}, {3, 7}}, got)
	})

	T.Run("rejects empty node list", func(t *testing.T) {
		t.Parallel()

		_, err := distribute("p", "s", nil, 1)
		must.Error(t, err)
	})

	T.Run("rejects zero slotsPerNode", func(t *testing.T) {
		t.Parallel()

		nodes, _ := equalSplit(3)
		_, err := distribute("p", "s", nodes, 0)
		must.Error(t, err)
	})
}

func TestNewKeyGenerator(T *testing.T) {
	T.Parallel()

	T.Run("rejects nil source", func(t *testing.T) {
		t.Parallel()

		_, err := NewKeyGenerator("p:", "", nil)
		must.Error(t, err)
	})

	T.Run("propagates source errors", func(t *testing.T) {
		t.Parallel()

		// FromClusterConfig with a non-positive node count fails at resolve time.
		_, err := NewKeyGenerator("p:", "", FromClusterConfig(0, 1))
		must.Error(t, err)
	})

	T.Run("FromClusterConfig(3, 1) yields the pinned hashtags", func(t *testing.T) {
		t.Parallel()

		gen, err := NewKeyGenerator("example:v1:", "", FromClusterConfig(3, 1))
		must.NoError(t, err)
		// Order is node-major: index 0 is node0's single hashtag, etc.
		must.Eq(t, []string{
			"{example:v1:2}",
			"{example:v1:0}",
			"{example:v1:1}",
		}, gen.Hashtags())
	})

	T.Run("FromClusterConfig(3, 4) yields the pinned 12-tag distribution", func(t *testing.T) {
		t.Parallel()

		gen, err := NewKeyGenerator("example:v1:", "", FromClusterConfig(3, 4))
		must.NoError(t, err)
		// Order is node-major: node0's 4 hashtags, then node1's, then node2's.
		must.Eq(t, []string{
			"{example:v1:2}",
			"{example:v1:6}",
			"{example:v1:10}",
			"{example:v1:11}",
			"{example:v1:0}",
			"{example:v1:3}",
			"{example:v1:7}",
			"{example:v1:12}",
			"{example:v1:1}",
			"{example:v1:4}",
			"{example:v1:5}",
			"{example:v1:8}",
		}, gen.Hashtags())
	})
}

func TestKeyGenerator_SlottedKey(T *testing.T) {
	T.Parallel()

	gen, err := NewKeyGenerator("example:v1:", "", FromClusterConfig(3, 1))
	must.NoError(T, err)

	T.Run("seeds map to pinned hashtags", func(t *testing.T) {
		t.Parallel()

		cases := map[string]string{
			"alice": "{example:v1:1}",
			"bob":   "{example:v1:1}",
			"carol": "{example:v1:1}",
			"dave":  "{example:v1:0}",
			"":      "{example:v1:0}",
			"999":   "{example:v1:2}",
		}
		for seed, want := range cases {
			must.EqOp(t, want, gen.SlottedKey(seed),
				must.Sprintf("seed %q", seed))
		}
	})

	T.Run("appended user data does not change the slot", func(t *testing.T) {
		t.Parallel()

		// Pinned: alice routes to {example:v1:1}, whose slot is 14952. The
		// full key "{example:v1:1}alice" must hash to the same slot — that's
		// the whole point of the hashtag mechanism.
		base := gen.SlottedKey("alice")
		must.EqOp(t, "{example:v1:1}", base)
		must.EqOp(t, uint16(14952), SlotForKey(base))
		must.EqOp(t, uint16(14952), SlotForKey(base+"alice"))
	})

	T.Run("FNV distributes 6000 seeds evenly across 3 buckets", func(t *testing.T) {
		t.Parallel()

		// Pinned histogram for the seed sequence "user-0" .. "user-5999".
		counts := make(map[string]int)
		const trials = 6000
		for i := range trials {
			counts[gen.SlottedKey("user-"+strconv.Itoa(i))]++
		}
		must.Eq(t, map[string]int{
			"{example:v1:0}": 1998,
			"{example:v1:1}": 1992,
			"{example:v1:2}": 2010,
		}, counts)
	})
}
