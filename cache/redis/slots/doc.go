// Package slots produces Redis Cluster keys whose slots are pre-planned to
// distribute evenly across the cluster's nodes.
//
// A KeyGenerator is constructed with a hashtag template (a prefix and a
// suffix) and a TopologySource describing the cluster shape. At construction
// time it precomputes N*K hashtag strings of the form
//
//	{prefix<value>suffix}
//
// such that the K slots within each node are distinct and one bucket lands on
// each of the N nodes. SlottedKey(seed) then deterministically maps a seed
// (typically a user ID or another stable identifier) to one of those
// precomputed hashtags via FNV-1a, so callers can build the full key as
//
//	key := gen.SlottedKey(userID) + userID
//
// and trust that reads and writes for the same userID always route to the
// same slot.
//
// The motivation is that CRC16 collides adjacent integers more often than
// callers expect: with a 3-node equal-split cluster, the keys {key:version0}
// and {key:version2} both hash onto the same node, so a naive 0..N-1 sequence
// does not spread load.
package slots
