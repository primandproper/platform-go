package slots

import (
	"strconv"

	"github.com/primandproper/platform-go/v5/errors"
)

// SlotRange is an inclusive [Start, End] range of Redis Cluster slots.
type SlotRange struct {
	Start uint16
	End   uint16
}

// Contains reports whether slot lies within the range.
func (r SlotRange) Contains(slot uint16) bool {
	return slot >= r.Start && slot <= r.End
}

// NodeSlots is the set of ranges owned by a single cluster node. A node may
// own multiple non-contiguous ranges after rebalancing.
type NodeSlots []SlotRange

// Contains reports whether any range owned by the node contains slot.
func (n NodeSlots) Contains(slot uint16) bool {
	for _, r := range n {
		if r.Contains(slot) {
			return true
		}
	}
	return false
}

// equalSplit returns n NodeSlots covering all 16384 cluster slots in
// contiguous, near-equal partitions. The split matches the layout produced by
// `redis-cli --cluster create` on a fresh cluster.
func equalSplit(n int) ([]NodeSlots, error) {
	if n <= 0 {
		return nil, errors.New("node count must be positive")
	}
	if n > SlotCount {
		return nil, errors.Newf("cannot split %d slots into %d nodes", SlotCount, n)
	}

	out := make([]NodeSlots, n)
	for i := range n {
		start := uint16(i * SlotCount / n)
		end := uint16((i+1)*SlotCount/n - 1)
		out[i] = NodeSlots{{Start: start, End: end}}
	}
	return out, nil
}

// nodeForSlot returns the index of the node whose ranges contain slot, or -1
// if no node owns it.
func nodeForSlot(nodes []NodeSlots, slot uint16) int {
	for i := range nodes {
		if nodes[i].Contains(slot) {
			return i
		}
	}
	return -1
}

// distribute searches integer values v = 0, 1, 2, ... and returns N*K values
// such that the hashtag prefix+strconv.Itoa(v)+suffix yields a slot owned by
// each of the N nodes K times, with all K slots per node distinct.
//
// Result is indexed result[nodeIndex][k] = value, ordered by the nodes slice.
// Errors if the search runs out of candidates before every node is filled.
func distribute(prefix, suffix string, nodes []NodeSlots, slotsPerNode int) ([][]int, error) {
	if len(nodes) == 0 {
		return nil, errors.New("no nodes provided")
	}
	if slotsPerNode <= 0 {
		return nil, errors.New("slotsPerNode must be positive")
	}

	maxAttempts := max(SlotCount*slotsPerNode*4, SlotCount)

	result := make([][]int, len(nodes))
	seen := make([]map[uint16]struct{}, len(nodes))
	for i := range nodes {
		result[i] = make([]int, 0, slotsPerNode)
		seen[i] = make(map[uint16]struct{}, slotsPerNode)
	}

	filledNodes := 0
	for v := range maxAttempts {
		slot := Slot(prefix + strconv.Itoa(v) + suffix)
		node := nodeForSlot(nodes, slot)
		if node < 0 {
			continue
		}
		if len(result[node]) == slotsPerNode {
			continue
		}
		if _, dup := seen[node][slot]; dup {
			continue
		}
		seen[node][slot] = struct{}{}
		result[node] = append(result[node], v)
		if len(result[node]) == slotsPerNode {
			filledNodes++
			if filledNodes == len(nodes) {
				return result, nil
			}
		}
	}

	filled := 0
	for _, r := range result {
		filled += len(r)
	}
	return nil, errors.Newf(
		"exhausted %d candidates: filled %d of %d slot assignments",
		maxAttempts, filled, len(nodes)*slotsPerNode,
	)
}
