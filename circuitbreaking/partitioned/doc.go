/*
Package partitioned provides a circuit breaker that is partitioned by key.

Where the circuitbreaking package exposes a single breaker whose state is shared
by all traffic, this package hands out an independent breaker per registered key
(for example, a tenant ID). A key registered at construction gets its own breaker
so it can be circuit broken in isolation; any unregistered key falls back to a
shared global breaker. The set of keyed breakers is fixed and operator-chosen, so
per-breaker metrics stay low-cardinality.
*/
package partitioned
