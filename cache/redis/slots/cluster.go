package slots

import (
	"context"

	"github.com/primandproper/platform-go/v2/errors"

	"github.com/redis/go-redis/v9"
)

// ClusterShardsClient is the slice of *redis.ClusterClient that
// FromClusterClient needs. It exists so tests can supply a fake without
// depending on a live cluster.
//
//go:generate go tool github.com/matryer/moq -out clustershardsclient_mock_test.go -pkg slots -fmt goimports . ClusterShardsClient
type ClusterShardsClient interface {
	ClusterShards(ctx context.Context) *redis.ClusterShardsCmd
}

// nodeSlotsFromClient runs CLUSTER SHARDS against c and returns one NodeSlots
// per shard, preserving the order returned by Redis. Slot ranges within a
// shard are preserved verbatim, including any non-contiguous gaps that result
// from rebalancing.
func nodeSlotsFromClient(ctx context.Context, c ClusterShardsClient) ([]NodeSlots, error) {
	shards, err := c.ClusterShards(ctx).Result()
	if err != nil {
		return nil, errors.Wrap(err, "fetching cluster shards")
	}

	out := make([]NodeSlots, 0, len(shards))
	for i := range shards {
		shard := &shards[i]
		ranges := make(NodeSlots, 0, len(shard.Slots))
		for _, sr := range shard.Slots {
			if sr.Start < 0 || sr.End < 0 || sr.Start >= SlotCount || sr.End >= SlotCount {
				return nil, errors.Newf("shard slot range out of bounds: [%d, %d]", sr.Start, sr.End)
			}
			ranges = append(ranges, SlotRange{
				Start: uint16(sr.Start),
				End:   uint16(sr.End),
			})
		}
		out = append(out, ranges)
	}
	return out, nil
}
