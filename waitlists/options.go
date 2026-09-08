package waitlists

import (
	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/cryptography/hashing"
	"github.com/primandproper/primitives-go/cryptography/hashing/sha256"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
)

// SQLStoreOption configures a SQLStore.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. A caller wanting none of the three names none of them.
type SQLStoreOption func(*SQLStore)

// WithTablePrefix namespaces the two waitlist tables. It must match the prefix
// the migrations were rendered with; nothing here can check that, and a mismatch
// surfaces as a missing table on the first query rather than at construction.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) { s.prefix = prefix }
}

// WithClock swaps the clock that decides whether a list is still open and stamps
// every lifecycle transition.
//
// It is one clock rather than two on purpose. A list's closing time is compared
// against whatever this returns — the comparison is a bound instant rather than
// the server's CURRENT_TIMESTAMP, for the reason querygen.AtMostArgument gives —
// so a test clock that only moves when a test moves it decides both halves
// consistently.
func WithClock(c clock.Clock) SQLStoreOption {
	return func(s *SQLStore) {
		if c != nil {
			s.clock = c
		}
	}
}

// WithHasher swaps what the contact_digest column holds.
//
// The default is SHA-256, and a replacement must be a cryptographic hash —
// hashing.Hasher also has adler32, crc64 and fnv implementations, and a checksum
// here is a column whose collisions are somebody else's signup.
//
// What it protects is narrower than passwordreset's digest and worth being
// honest about. A contact is an address somebody chose, not 256 bits from a
// CSPRNG, so the digest of a withdrawn signup is guessable by anyone willing to
// hash a list of addresses — it is not there to make the withdrawal secret. It
// is there so the row that remembers a withdrawal does not have to keep the
// address it is about, which is the difference between a suppression list and a
// mailing list you promised not to use.
//
// Changing it on a deployed store orphans every digest already written: existing
// signups stop being found by their contacts, and a withdrawn contact stops
// being suppressed. A deployment that swaps it rewrites the column first.
func WithHasher(hasher hashing.Hasher) SQLStoreOption {
	return func(s *SQLStore) {
		if hasher != nil {
			s.hasher = hasher
		}
	}
}

// WithStoreLogger attaches a logger. An absent logger logs nowhere.
func WithStoreLogger(logger logging.Logger) SQLStoreOption {
	return func(s *SQLStore) { s.logger = logger }
}

// WithStoreTracerProvider attaches a tracer provider, enabling spans on every
// read and write. An absent provider traces nowhere.
//
// It takes a provider rather than a ready-made tracer so that the spans this
// package emits carry this package's instrumentation scope. A caller-supplied
// tracer would attribute them to whoever built it.
func WithStoreTracerProvider(tracerProvider tracing.Provider) SQLStoreOption {
	return func(s *SQLStore) { s.tracerProvider = tracerProvider }
}

// WithStoreMetricsProvider attaches a metrics provider. An absent provider
// records nothing.
func WithStoreMetricsProvider(metricsProvider metrics.Provider) SQLStoreOption {
	return func(s *SQLStore) { s.metricsProvider = metricsProvider }
}

// WithStorePillars attaches a logger, tracer provider, and metrics provider in
// one go. A nil Pillars attaches nothing.
//
// Options apply in order, so a caller can hand over its pillars and then
// override one of them.
func WithStorePillars(p *observability.Pillars) SQLStoreOption {
	return func(s *SQLStore) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

// defaultHasher is what the contact_digest column holds when nothing else is
// configured.
func defaultHasher() hashing.Hasher { return sha256.NewSHA256Hasher() }

// defaultClock is what decides "now" when nothing else is configured.
func defaultClock() clock.Clock { return clock.NewClock() }
