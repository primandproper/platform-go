package slots

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v5/errors"

	"github.com/redis/go-redis/v9"
	"github.com/shoenig/test/must"
)

func TestFromClusterClient(T *testing.T) {
	T.Parallel()

	T.Run("resolves the topology by calling CLUSTER SHARDS", func(t *testing.T) {
		t.Parallel()

		shards := []redis.ClusterShard{
			{Slots: []redis.SlotRange{{Start: 0, End: 5460}}},
			{Slots: []redis.SlotRange{{Start: 5461, End: 10922}}},
			{Slots: []redis.SlotRange{
				{Start: 10923, End: 12000},
				{Start: 14000, End: 16383},
			}},
		}

		cmd := redis.NewClusterShardsCmd(t.Context(), "cluster", "shards")
		cmd.SetVal(shards)

		mock := &ClusterShardsClientMock{
			ClusterShardsFunc: func(_ context.Context) *redis.ClusterShardsCmd {
				return cmd
			},
		}

		topology, err := FromClusterClient(t.Context(), mock, 2)()
		must.NoError(t, err)
		must.EqOp(t, 2, topology.SlotsPerNode)
		must.Len(t, 3, topology.Nodes)

		must.Eq(t, NodeSlots{{Start: 0, End: 5460}}, topology.Nodes[0])
		must.Eq(t, NodeSlots{{Start: 5461, End: 10922}}, topology.Nodes[1])
		must.Eq(t, NodeSlots{
			{Start: 10923, End: 12000},
			{Start: 14000, End: 16383},
		}, topology.Nodes[2])
	})

	T.Run("propagates client errors through TopologySource", func(t *testing.T) {
		t.Parallel()

		cmd := redis.NewClusterShardsCmd(t.Context(), "cluster", "shards")
		cmd.SetErr(errors.New("boom"))

		mock := &ClusterShardsClientMock{
			ClusterShardsFunc: func(_ context.Context) *redis.ClusterShardsCmd {
				return cmd
			},
		}

		_, err := FromClusterClient(t.Context(), mock, 1)()
		must.Error(t, err)
	})

	T.Run("rejects out-of-range slot indices", func(t *testing.T) {
		t.Parallel()

		cmd := redis.NewClusterShardsCmd(t.Context(), "cluster", "shards")
		cmd.SetVal([]redis.ClusterShard{
			{Slots: []redis.SlotRange{{Start: 0, End: 99999}}},
		})

		mock := &ClusterShardsClientMock{
			ClusterShardsFunc: func(_ context.Context) *redis.ClusterShardsCmd {
				return cmd
			},
		}

		_, err := FromClusterClient(t.Context(), mock, 1)()
		must.Error(t, err)
	})

	T.Run("NewKeyGenerator threads source errors through", func(t *testing.T) {
		t.Parallel()

		cmd := redis.NewClusterShardsCmd(t.Context(), "cluster", "shards")
		cmd.SetErr(errors.New("boom"))

		mock := &ClusterShardsClientMock{
			ClusterShardsFunc: func(_ context.Context) *redis.ClusterShardsCmd {
				return cmd
			},
		}

		_, err := NewKeyGenerator("p:", "", FromClusterClient(t.Context(), mock, 1))
		must.Error(t, err)
	})
}
