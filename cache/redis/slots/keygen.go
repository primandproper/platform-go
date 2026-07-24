package slots

import (
	"context"
	"hash/fnv"
	"strconv"

	"github.com/primandproper/platform-go/v6/errors"
)

// Topology describes the cluster shape a KeyGenerator should plan against:
// per-node slot ownership and the desired number of distinct slots per node.
type Topology struct {
	Nodes        []NodeSlots
	SlotsPerNode int
}

// TopologySource is a deferred lookup of the cluster's slot layout. It is
// invoked once by NewKeyGenerator. The deferral lets callers express both
// static and dynamic topologies in a single expression without separately
// handling the lookup error at the call site.
type TopologySource func() (Topology, error)

// FromClusterConfig returns a TopologySource that assumes the cluster owns
// SlotCount slots evenly split across nodeCount nodes — the layout produced
// by `redis-cli --cluster create` on a fresh cluster. Useful for tests, local
// development, and any environment where the cluster is known to be in its
// default shape.
func FromClusterConfig(nodeCount, slotsPerNode int) TopologySource {
	return func() (Topology, error) {
		nodes, err := equalSplit(nodeCount)
		if err != nil {
			return Topology{}, err
		}
		return Topology{Nodes: nodes, SlotsPerNode: slotsPerNode}, nil
	}
}

// FromClusterClient returns a TopologySource that queries CLUSTER SHARDS
// against the given client at NewKeyGenerator time. The resulting Topology
// reflects whatever slot layout the cluster actually reports, including
// rebalanced or non-contiguous shards.
func FromClusterClient(ctx context.Context, c ClusterShardsClient, slotsPerNode int) TopologySource {
	return func() (Topology, error) {
		nodes, err := nodeSlotsFromClient(ctx, c)
		if err != nil {
			return Topology{}, err
		}

		return Topology{Nodes: nodes, SlotsPerNode: slotsPerNode}, nil
	}
}

// KeyGenerator returns hashtagged Redis Cluster keys whose slots are
// pre-planned to spread evenly across the cluster's nodes. Construct one with
// NewKeyGenerator and call SlottedKey for each operation.
type KeyGenerator struct {
	hashtags []string
}

// NewKeyGenerator resolves the topology and precomputes N*K hashtag strings
// of the form "{prefix<value>suffix}" such that the resulting slots are
// distributed one per node K times, with all K slots on a node distinct.
//
// Returns an error if the topology source fails, if the topology is empty or
// has a non-positive SlotsPerNode, or if the search cannot find enough
// distinct slots within the search budget.
func NewKeyGenerator(prefix, suffix string, src TopologySource) (*KeyGenerator, error) {
	if src == nil {
		return nil, errors.New("nil TopologySource")
	}

	topology, err := src()
	if err != nil {
		return nil, errors.Wrap(err, "resolving topology")
	}

	values, err := distribute(prefix, suffix, topology.Nodes, topology.SlotsPerNode)
	if err != nil {
		return nil, errors.Wrap(err, "computing hashtag values")
	}

	flat := make([]string, 0, len(topology.Nodes)*topology.SlotsPerNode)
	for _, vs := range values {
		for _, v := range vs {
			flat = append(flat, "{"+prefix+strconv.Itoa(v)+suffix+"}")
		}
	}

	return &KeyGenerator{hashtags: flat}, nil
}

// SlottedKey returns one of the precomputed hashtagged keys. The choice is
// deterministic in seed: identical seeds always map to the same hashtag (and
// therefore the same slot), so callers can build the full key as
//
//	key := gen.SlottedKey(userID) + userID
//
// and trust that subsequent reads for the same userID land on the same node.
func (g *KeyGenerator) SlottedKey(seed string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return g.hashtags[h.Sum32()%uint32(len(g.hashtags))]
}

// Hashtags returns a copy of the precomputed hashtag strings, in node-major
// order. Exposed mainly for debugging and tests; callers wiring up real
// workloads should reach for SlottedKey.
func (g *KeyGenerator) Hashtags() []string {
	out := make([]string, len(g.hashtags))
	copy(out, g.hashtags)
	return out
}
