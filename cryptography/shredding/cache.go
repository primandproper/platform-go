package shredding

import (
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
)

// keyCache holds unwrapped data keys in this process, and nowhere else.
//
// Not cache.Cache, and the difference is the point. A shared cache holding
// plaintext data keys writes them to another host's disk through its own
// persistence, where this package can neither bound their lifetime nor destroy
// them — a second copy of the key with none of the properties that make the
// first one shreddable.
//
// What is stored is the constructed Cipher rather than the key bytes, because
// building the AEAD is the expensive half and there is nothing to be gained by
// keeping the raw material around as well.
type keyCache struct {
	clock   clock.Clock
	entries map[Subject]cacheEntry
	ttl     time.Duration
	max     int

	mu sync.Mutex
}

type cacheEntry struct {
	cipher  encryption.Cipher
	expires time.Time
}

func newKeyCache(c clock.Clock, ttl time.Duration, maxEntries int) *keyCache {
	return &keyCache{
		clock:   c,
		entries: make(map[Subject]cacheEntry),
		ttl:     ttl,
		max:     maxEntries,
	}
}

// enabled reports whether this cache holds anything. A zero TTL turns it off
// entirely, which costs an unwrap per operation and buys an erasure that
// completes on the call.
func (c *keyCache) enabled() bool {
	return c.ttl > 0 && c.max > 0
}

// get returns a live key for the subject.
func (c *keyCache) get(subject Subject) (encryption.Cipher, bool) {
	if !c.enabled() {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[subject]
	if !ok {
		return nil, false
	}

	if !c.clock.Now().Before(entry.expires) {
		delete(c.entries, subject)

		return nil, false
	}

	return entry.cipher, true
}

// put stores a key until the TTL runs out, evicting to stay under the cap.
func (c *keyCache) put(subject Subject, cipher encryption.Cipher) {
	if !c.enabled() {
		return
	}

	now := c.clock.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.entries[subject]; !ok && len(c.entries) >= c.max {
		c.evictLocked(now)
	}

	c.entries[subject] = cacheEntry{cipher: cipher, expires: now.Add(c.ttl)}
}

// drop forgets a subject's key, and reports whether there was one to forget.
//
// This is what makes a shred take effect in this process immediately rather than
// at the TTL. It drops the reference and nothing more: the expanded key schedule
// inside crypto/aes is not reachable to overwrite, and a garbage collector may
// have copied the material anyway, so the honest bound on a cached key is the
// TTL rather than memory hygiene.
//
// The return value is what separates the two things an invalidation can mean:
// this replica was holding the key and is not anymore, or it had already
// expired. An expired entry counts as nothing to forget, because from the
// caller's side of the TTL it is already gone.
func (c *keyCache) drop(subject Subject) bool {
	if !c.enabled() {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[subject]
	if !ok {
		return false
	}

	delete(c.entries, subject)

	return c.clock.Now().Before(entry.expires)
}

// len reports how many keys are held, for the gauge.
func (c *keyCache) len() int {
	if !c.enabled() {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.entries)
}

// evictLocked makes room for one entry: expired keys first, then an arbitrary
// live one.
//
// Not an LRU, deliberately. A miss costs one unwrap, and an LRU costs a second
// data structure that has to stay consistent with this map under a lock that is
// already the hot path — a worse trade for a cache whose entries expire in
// minutes anyway. Go's map iteration order makes "arbitrary" mean roughly
// "random", which is the property that matters: no subject is systematically the
// one that gets evicted.
func (c *keyCache) evictLocked(now time.Time) {
	for subject := range c.entries {
		if !now.Before(c.entries[subject].expires) {
			delete(c.entries, subject)
		}
	}

	if len(c.entries) < c.max {
		return
	}

	for subject := range c.entries {
		delete(c.entries, subject)

		return
	}
}
