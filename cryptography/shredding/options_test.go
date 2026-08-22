package shredding

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("ignores a nil clock", func(t *testing.T) {
		t.Parallel()

		k := &KeyManager{clock: newStubClock()}
		WithClock(nil)(k)

		test.NotNil(t, k.clock)
	})

	T.Run("ignores a nil broadcaster", func(t *testing.T) {
		t.Parallel()

		broadcaster := &recordingBroadcaster{}

		k := &KeyManager{}
		WithBroadcaster(broadcaster)(k)
		WithBroadcaster(nil)(k)

		test.NotNil(t, k.broadcaster)
	})

	T.Run("accepts a zero TTL and rejects a negative one", func(t *testing.T) {
		t.Parallel()

		// Zero is a real setting: it turns caching off, so erasure completes on
		// the call at the cost of an unwrap per operation.
		k := &KeyManager{ttl: DefaultKeyTTL}
		WithKeyTTL(0)(k)
		test.EqOp(t, time.Duration(0), k.ttl)

		WithKeyTTL(-time.Minute)(k)
		test.EqOp(t, time.Duration(0), k.ttl)
	})

	T.Run("rejects a negative cache cap", func(t *testing.T) {
		t.Parallel()

		k := &KeyManager{maxCached: DefaultMaxCachedKeys}
		WithMaxCachedKeys(-1)(k)

		test.EqOp(t, DefaultMaxCachedKeys, k.maxCached)
	})

	T.Run("attaches a logger unconditionally", func(t *testing.T) {
		t.Parallel()

		logger := logging.EnsureLogger(nil)

		k := &KeyManager{}
		WithLogger(logger)(k)

		test.NotNil(t, k.logger)
	})

	T.Run("ignores an empty table prefix", func(t *testing.T) {
		t.Parallel()

		s := &SQLStore{tables: newTables("ddb")}
		WithTablePrefix("")(s)

		test.EqOp(t, "ddb", s.tables.prefix())
	})

	T.Run("skips a nil option", func(t *testing.T) {
		t.Parallel()

		keys, err := NewKeys(newSQLiteEnv(t).newStore(t), newTestWrapper(t), nil)
		must.NoError(t, err)
		test.NotNil(t, keys)
	})
}

func TestKeyCache(T *testing.T) {
	T.Parallel()

	T.Run("reports nothing held when disabled", func(t *testing.T) {
		t.Parallel()

		c := newKeyCache(newStubClock(), 0, DefaultMaxCachedKeys)

		test.False(t, c.enabled())
		test.EqOp(t, 0, c.len())

		c.put(testSubject, nil)
		_, ok := c.get(testSubject)
		test.False(t, ok)

		// Dropping from a disabled cache is a no-op rather than a panic: Shred
		// calls it unconditionally.
		c.drop(testSubject)
	})

	T.Run("refreshes an existing entry without evicting", func(t *testing.T) {
		t.Parallel()

		clk := newStubClock()
		c := newKeyCache(clk, time.Minute, 1)

		cipher, err := newTestCipher()
		must.NoError(t, err)

		c.put(testSubject, cipher)
		c.put(testSubject, cipher)

		test.EqOp(t, 1, c.len())

		_, ok := c.get(testSubject)
		test.True(t, ok)
	})
}
