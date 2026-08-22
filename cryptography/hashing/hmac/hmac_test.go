package hmac

import (
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewHMACSHA256Hasher(T *testing.T) {
	T.Parallel()

	// RFC 4231 test case 2, which pins this to the published vector rather than
	// to whatever this implementation happened to produce first.
	T.Run("matches RFC 4231 case 2", func(t *testing.T) {
		t.Parallel()

		hasher := NewHMACSHA256Hasher([]byte("Jefe"))

		test.EqOp(t,
			"5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843",
			hashing.HexString(hasher, "what do ya want for nothing?"),
		)
	})

	T.Run("digest is thirty-two bytes wide", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 32, NewHMACSHA256Hasher([]byte("key")).Hash([]byte("anything")))
	})

	T.Run("different keys disagree on the same content", func(t *testing.T) {
		t.Parallel()

		content := []byte("the same payload")

		test.False(t, Equal(
			NewHMACSHA256Hasher([]byte("key-a")).Hash(content),
			NewHMACSHA256Hasher([]byte("key-b")).Hash(content),
		))
	})

	T.Run("empty key is accepted", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 32, NewHMACSHA256Hasher(nil).Hash([]byte("anything")))
	})

	// The key is copied at construction, so a caller that reuses or zeroes its
	// buffer afterwards does not silently change every later signature.
	T.Run("mutating the caller's key buffer does not change the hasher", func(t *testing.T) {
		t.Parallel()

		key := []byte("original")
		hasher := NewHMACSHA256Hasher(key)
		before := hasher.Hash([]byte("content"))

		for i := range key {
			key[i] = 0
		}

		test.True(t, Equal(before, hasher.Hash([]byte("content"))))
	})

	// One endpoint's hasher signs deliveries concurrently, so a shared internal
	// hash.Hash would interleave two payloads into one signature.
	T.Run("is safe for concurrent use", func(t *testing.T) {
		t.Parallel()

		hasher := NewHMACSHA256Hasher([]byte("key"))
		expected := hasher.Hash([]byte("content"))

		var wg sync.WaitGroup
		for range 64 {
			wg.Go(func() {
				test.True(t, Equal(expected, hasher.Hash([]byte("content"))))
			})
		}
		wg.Wait()
	})
}

func TestNewHMACSHA512Hasher(T *testing.T) {
	T.Parallel()

	T.Run("matches RFC 4231 case 2", func(t *testing.T) {
		t.Parallel()

		hasher := NewHMACSHA512Hasher([]byte("Jefe"))

		test.EqOp(t,
			"164b7a7bfcf819e2e395fbe73b56e0a387bd64222e831fd610270cd7ea250554"+
				"9758bf75c05a994a6d034f65f8f0e6fdcaeab1a34d4a6b4b636e070a38bce737",
			hashing.HexString(hasher, "what do ya want for nothing?"),
		)
	})

	T.Run("digest is sixty-four bytes wide", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 64, NewHMACSHA512Hasher([]byte("key")).Hash([]byte("anything")))
	})
}

func TestEqual(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		hasher := NewHMACSHA256Hasher([]byte("key"))

		must.True(t, Equal(hasher.Hash([]byte("content")), hasher.Hash([]byte("content"))))
		test.False(t, Equal(hasher.Hash([]byte("content")), hasher.Hash([]byte("other"))))
	})

	T.Run("length mismatch", func(t *testing.T) {
		t.Parallel()

		test.False(t, Equal([]byte("short"), []byte("considerably longer")))
	})
}

func TestMatchesAny(T *testing.T) {
	T.Parallel()

	content := []byte("content")

	hashers := []hashing.Hasher{
		NewHMACSHA256Hasher([]byte("a")),
		NewHMACSHA256Hasher([]byte("b")),
	}

	macUnder := func(key string) []byte {
		return NewHMACSHA256Hasher([]byte(key)).Hash(content)
	}

	// The rotation case in both directions: the outgoing key still verifies, and
	// so does the incoming one.
	T.Run("matches under any held key", func(t *testing.T) {
		t.Parallel()

		test.True(t, MatchesAny(hashers, content, macUnder("a")))
		test.True(t, MatchesAny(hashers, content, macUnder("b")))
	})

	// The provider's own rotation: several candidates in one header, any one of
	// which may be the one this receiver's secret produces.
	T.Run("matches when any candidate is the one", func(t *testing.T) {
		t.Parallel()

		test.True(t, MatchesAny(hashers, content, macUnder("z"), macUnder("b")))
	})

	T.Run("does not match a key it does not hold", func(t *testing.T) {
		t.Parallel()

		test.False(t, MatchesAny(hashers, content, macUnder("c")))
	})

	T.Run("does not match a MAC over other content", func(t *testing.T) {
		t.Parallel()

		test.False(t, MatchesAny(hashers, content, NewHMACSHA256Hasher([]byte("a")).Hash([]byte("other"))))
	})

	// A verifier holding no keys accepts nothing, and a header carrying no
	// candidates proves nothing. Either reading as a match would turn a
	// misconfiguration into an open endpoint.
	T.Run("an empty side is never a match", func(t *testing.T) {
		t.Parallel()

		test.False(t, MatchesAny(nil, content, macUnder("a")))
		test.False(t, MatchesAny(hashers, content))
		test.False(t, MatchesAny(nil, content))
	})
}
