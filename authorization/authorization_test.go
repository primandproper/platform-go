package authorization

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"slices"
	"testing"

	"github.com/primandproper/platform-go/v13/cache"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	permRead   Permission = "read.things"
	permWrite  Permission = "write.things"
	permDelete Permission = "delete.things"
)

func TestPermissionSet_NilReceiver(T *testing.T) {
	T.Parallel()

	// A nil set is how "no membership in this scope" is represented, so every
	// method has to tolerate it rather than the callers having to check.
	T.Run("grants nothing without panicking", func(t *testing.T) {
		t.Parallel()

		var s *PermissionSet

		test.False(t, s.Has(permRead))
		test.False(t, s.HasAny(permRead, permWrite))
		test.EqOp(t, 0, s.Len())
		test.True(t, s.IsEmpty())
		test.SliceEmpty(t, s.Slice())
		test.EqOp(t, "PermissionSet(n=0)", s.String())
		test.SliceEmpty(t, slices.Collect(s.All()))
	})

	T.Run("participates in set operations", func(t *testing.T) {
		t.Parallel()

		var s *PermissionSet

		test.EqOp(t, 1, s.Union(NewPermissionSet(permRead)).Len())
		test.True(t, s.IsSubsetOf(NewPermissionSet(permRead)))
		test.True(t, s.Equal(NewPermissionSet()))
	})
}

func TestNewPermissionSet(T *testing.T) {
	T.Parallel()

	T.Run("collapses duplicates and drops empties", func(t *testing.T) {
		t.Parallel()

		s := NewPermissionSet(permRead, permRead, "", permWrite)

		test.EqOp(t, 2, s.Len())
		test.True(t, s.Has(permRead))
		test.True(t, s.Has(permWrite))
		test.False(t, s.Has(""))
	})

	T.Run("copies its input", func(t *testing.T) {
		t.Parallel()

		perms := []Permission{permRead}
		s := NewPermissionSet(perms...)

		perms[0] = permDelete

		test.True(t, s.Has(permRead))
		test.False(t, s.Has(permDelete))
	})
}

func TestPermissionSet_Predicates(T *testing.T) {
	T.Parallel()

	T.Run("HasAll requires every permission", func(t *testing.T) {
		t.Parallel()

		s := NewPermissionSet(permRead, permWrite)

		test.True(t, s.HasAll(permRead, permWrite))
		test.False(t, s.HasAll(permRead, permDelete))
	})

	// Vacuous truth is the honest answer for a universal quantifier over an
	// empty list, and it is also a hazard: an empty requirement would authorize
	// everyone. The guard lives at the declaration site instead, and the three
	// declaration sites do not agree — the matrix on PermissionSet.HasAll is
	// authoritative. Before changing this answer, change all three: the paired
	// assertions are "denies when required with no permissions" in
	// authorization/http and "rejects a method required with no permissions" in
	// authorization/grpc.
	T.Run("HasAll with no permissions is vacuously true", func(t *testing.T) {
		t.Parallel()

		test.True(t, NewPermissionSet().HasAll())

		var s *PermissionSet
		test.True(t, s.HasAll())
	})

	T.Run("HasAny needs a witness", func(t *testing.T) {
		t.Parallel()

		s := NewPermissionSet(permRead)

		test.True(t, s.HasAny(permDelete, permRead))
		test.False(t, s.HasAny(permDelete))
		test.False(t, s.HasAny())
	})

	T.Run("empty permission is never held", func(t *testing.T) {
		t.Parallel()

		test.False(t, NewPermissionSet(permRead).Has(""))
	})
}

func TestPermissionSet_SetOperations(T *testing.T) {
	T.Parallel()

	T.Run("union merges and ignores nils", func(t *testing.T) {
		t.Parallel()

		u := NewPermissionSet(permRead).Union(NewPermissionSet(permWrite), nil)

		test.EqOp(t, 2, u.Len())
		test.True(t, u.HasAll(permRead, permWrite))
	})

	T.Run("union does not mutate its operands", func(t *testing.T) {
		t.Parallel()

		a := NewPermissionSet(permRead)
		b := NewPermissionSet(permWrite)

		_ = a.Union(b)

		test.EqOp(t, 1, a.Len())
		test.EqOp(t, 1, b.Len())
	})

	T.Run("subset relation", func(t *testing.T) {
		t.Parallel()

		member := NewPermissionSet(permRead)
		admin := NewPermissionSet(permRead, permWrite)

		test.True(t, member.IsSubsetOf(admin))
		test.False(t, admin.IsSubsetOf(member))
		test.True(t, NewPermissionSet().IsSubsetOf(member))
		test.True(t, member.IsSubsetOf(member))
	})

	T.Run("equality ignores nil versus empty", func(t *testing.T) {
		t.Parallel()

		var nilSet *PermissionSet

		test.True(t, nilSet.Equal(NewPermissionSet()))
		test.True(t, NewPermissionSet(permRead).Equal(NewPermissionSet(permRead)))
		test.False(t, NewPermissionSet(permRead).Equal(NewPermissionSet(permWrite)))
	})
}

func TestPermissionSet_Iteration(T *testing.T) {
	T.Parallel()

	T.Run("iterates in sorted order", func(t *testing.T) {
		t.Parallel()

		s := NewPermissionSet(permWrite, permRead, permDelete)

		got := slices.Collect(s.All())

		test.Eq(t, []Permission{permDelete, permRead, permWrite}, got)
		test.Eq(t, got, s.Slice())
	})

	T.Run("iteration stops early when asked", func(t *testing.T) {
		t.Parallel()

		s := NewPermissionSet(permWrite, permRead, permDelete)

		var seen int
		for range s.All() {
			seen++

			break
		}

		test.EqOp(t, 1, seen)
	})
}

func TestPermissionSet_Encoding(T *testing.T) {
	T.Parallel()

	// Deterministic output is load-bearing: cache entries and golden files
	// depend on the same set producing the same bytes every run.
	T.Run("JSON is a sorted array of strings", func(t *testing.T) {
		t.Parallel()

		b, err := json.Marshal(NewPermissionSet(permWrite, permRead))
		must.NoError(t, err)

		test.EqOp(t, `["read.things","write.things"]`, string(b))
	})

	T.Run("JSON round-trips", func(t *testing.T) {
		t.Parallel()

		original := NewPermissionSet(permRead, permWrite)

		b, err := json.Marshal(original)
		must.NoError(t, err)

		var decoded PermissionSet
		must.NoError(t, json.Unmarshal(b, &decoded))

		test.True(t, original.Equal(&decoded))
	})

	// The codec under test is deliberately the cache's default rather than a
	// named one. PermissionSet's only field is unexported, so it can only be
	// encoded by a codec that honors its MarshalBinary — and the last time the
	// default moved, the type was left teaching gob and saying nothing to what
	// replaced it. Naming the default here means the next move fails a test.
	T.Run("round-trips through the cache's default codec", func(t *testing.T) {
		t.Parallel()

		codec := cache.NewDefaultCodec[PermissionSet]()
		original := NewPermissionSet(permRead, permWrite, permDelete)

		encoded, err := codec.Encode(original)
		must.NoError(t, err)

		decoded, err := codec.Decode(encoded)
		must.NoError(t, err)

		must.NotNil(t, decoded)
		test.EqOp(t, 3, decoded.Len())
		test.True(t, original.Equal(decoded))
	})

	T.Run("an empty set round-trips through the cache's default codec", func(t *testing.T) {
		t.Parallel()

		codec := cache.NewDefaultCodec[PermissionSet]()

		encoded, err := codec.Encode(NewPermissionSet())
		must.NoError(t, err)

		decoded, err := codec.Decode(encoded)
		must.NoError(t, err)

		must.NotNil(t, decoded)
		test.True(t, decoded.IsEmpty())
	})

	// gob is no longer the default, but it is still the opt-in codec for values
	// CBOR cannot carry, and a PermissionSet nested in such a value takes this
	// path. It reaches MarshalBinary through gob's own fallback chain.
	T.Run("gob round-trips through an unexported field", func(t *testing.T) {
		t.Parallel()

		original := NewPermissionSet(permRead, permWrite, permDelete)

		var buf bytes.Buffer
		must.NoError(t, gob.NewEncoder(&buf).Encode(original))

		var decoded *PermissionSet
		must.NoError(t, gob.NewDecoder(&buf).Decode(&decoded))

		must.NotNil(t, decoded)
		test.EqOp(t, 3, decoded.Len())
		test.True(t, original.Equal(decoded))
	})

	T.Run("binary output is deterministic", func(t *testing.T) {
		t.Parallel()

		first, err := NewPermissionSet(permWrite, permRead).MarshalBinary()
		must.NoError(t, err)

		second, err := NewPermissionSet(permRead, permWrite).MarshalBinary()
		must.NoError(t, err)

		test.Eq(t, first, second)
	})

	T.Run("rejects malformed input", func(t *testing.T) {
		t.Parallel()

		var s PermissionSet

		test.Error(t, s.UnmarshalJSON([]byte(`{"not":"an array"}`)))
		test.Error(t, s.UnmarshalBinary([]byte(`not json`)))
	})
}
