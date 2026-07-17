package slots_test

import (
	"strconv"
	"testing"

	"github.com/primandproper/platform-go/v5/cache/redis/slots"
	"github.com/primandproper/platform-go/v5/testutils/containers"
	"github.com/primandproper/platform-go/v5/testutils/containers/redistest"

	"github.com/redis/go-redis/v9"
	"github.com/shoenig/test/must"
)

// startRedis brings up a cluster-mode-enabled Redis (so CLUSTER KEYSLOT
// works) and returns a connected client. The container is single-node — we
// only need Redis as an oracle for the slot a key would land on, not real
// cluster routing.
func startRedis(t *testing.T) *redis.Client {
	t.Helper()

	container := redistest.Start(t, redistest.WithClusterEnabled())
	opts, err := redis.ParseURL("redis://" + redistest.Address(t, container))
	must.NoError(t, err)

	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// TestKeyGenerator_redisAgreesOnSlots is the central integration check:
// across several (nodes, slotsPerNode) shapes, every precomputed hashtag the
// KeyGenerator produces must hash to the slot Redis itself reports via
// CLUSTER KEYSLOT. If our CRC16 ever drifts from Redis's implementation, or
// the hashtag wrapping is off, this test fails loudly.
func TestKeyGenerator_redisAgreesOnSlots(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	client := startRedis(T)

	cases := []struct {
		nodeCount    int
		slotsPerNode int
	}{
		{nodeCount: 3, slotsPerNode: 1},
		{nodeCount: 3, slotsPerNode: 2},
		{nodeCount: 5, slotsPerNode: 3},
		{nodeCount: 7, slotsPerNode: 1},
	}

	for _, tc := range cases {
		name := strconv.Itoa(tc.nodeCount) + "x" + strconv.Itoa(tc.slotsPerNode)
		T.Run(name, func(t *testing.T) {
			t.Parallel()
			gen, err := slots.NewKeyGenerator("key:version:", "",
				slots.FromClusterConfig(tc.nodeCount, tc.slotsPerNode))
			must.NoError(t, err)

			tags := gen.Hashtags()
			must.Len(t, tc.nodeCount*tc.slotsPerNode, tags)

			seenSlots := make(map[int64]struct{})
			for _, tag := range tags {
				localSlot := slots.SlotForKey(tag)
				serverSlot, kerr := client.ClusterKeySlot(t.Context(), tag).Result()
				must.NoError(t, kerr)
				must.EqOp(t, int64(localSlot), serverSlot,
					must.Sprintf("Redis disagrees on slot for %q", tag))

				_, dup := seenSlots[serverSlot]
				must.False(t, dup, must.Sprintf("slot %d reused across nodes", serverSlot))
				seenSlots[serverSlot] = struct{}{}
			}
		})
	}
}

// TestKeyGenerator_appendedUserDataPreservesSlot proves that the contract
// "SlottedKey(userID) + userID lands on a stable slot" holds end-to-end: ask
// the live Redis what slot it computes for the user-suffixed key and confirm
// it matches the slot of the bare hashtag.
func TestKeyGenerator_appendedUserDataPreservesSlot(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	client := startRedis(T)

	gen, err := slots.NewKeyGenerator("key:version:", "", slots.FromClusterConfig(5, 2))
	must.NoError(T, err)

	for _, user := range []string{"alice", "bob", "carol-the-extra-long-userid"} {
		T.Run(user, func(t *testing.T) {
			t.Parallel()

			base := gen.SlottedKey(user)
			full := base + user

			baseSlot, slotErr := client.ClusterKeySlot(t.Context(), base).Result()
			must.NoError(t, slotErr)

			fullSlot, slotErr := client.ClusterKeySlot(t.Context(), full).Result()
			must.NoError(t, slotErr)

			must.EqOp(t, baseSlot, fullSlot)
		})
	}
}

// TestSlotForKey_redisAgrees double-checks our hashtag extraction matches
// Redis. The cases mix bare keys, ordinary hashtags, empty hashtags, and the
// "first hashtag wins" rule.
func TestSlotForKey_redisAgrees(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	client := startRedis(T)

	keys := []string{
		"foo",
		"user1000",
		"{user1000}.following",
		"{user1000}.followers",
		"{}foo",
		"{foo",
		"{first}{second}",
		"key:version42",
		"{key:version42}",
		"{key:version42}:metadata",
	}

	for _, key := range keys {
		T.Run(key, func(t *testing.T) {
			t.Parallel()

			serverSlot, err := client.ClusterKeySlot(t.Context(), key).Result()
			must.NoError(t, err)
			must.EqOp(t, int64(slots.SlotForKey(key)), serverSlot)
		})
	}
}
