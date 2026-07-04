package bitmask

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type testPerm uint8

const (
	permRead   testPerm = 1 << iota // 1
	permWrite                       // 2
	permDelete                      // 4
	permAdmin                       // 8
)

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("with no flags", func(t *testing.T) {
		t.Parallel()

		mask := New[testPerm]()

		test.EqOp(t, testPerm(0), mask.Value())
		test.True(t, mask.IsEmpty())
	})

	T.Run("with single flag", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.EqOp(t, permRead, mask.Value())
	})

	T.Run("with multiple flags", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permWrite)

		test.EqOp(t, permRead|permWrite, mask.Value())
	})

	T.Run("with duplicate flags", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permRead)

		test.EqOp(t, permRead, mask.Value())
	})
}

func TestFromValue(T *testing.T) {
	T.Parallel()

	T.Run("with zero", func(t *testing.T) {
		t.Parallel()

		mask := FromValue(testPerm(0))

		test.True(t, mask.IsEmpty())
	})

	T.Run("with specific value", func(t *testing.T) {
		t.Parallel()

		mask := FromValue(testPerm(5))

		test.True(t, mask.Has(permRead))
		test.True(t, mask.Has(permDelete))
		test.False(t, mask.Has(permWrite))
	})
}

func TestBitmask_Value(T *testing.T) {
	T.Parallel()

	T.Run("returns underlying value", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permDelete)

		test.EqOp(t, permRead|permDelete, mask.Value())
	})
}

func TestBitmask_Set(T *testing.T) {
	T.Parallel()

	T.Run("set single flag", func(t *testing.T) {
		t.Parallel()

		base := New[testPerm]()
		mask := base.Set(permRead)

		test.True(t, mask.Has(permRead))
	})

	T.Run("set multiple flags", func(t *testing.T) {
		t.Parallel()

		base := New[testPerm]()
		mask := base.Set(permRead, permWrite)

		test.True(t, mask.Has(permRead))
		test.True(t, mask.Has(permWrite))
	})

	T.Run("set already set flag", func(t *testing.T) {
		t.Parallel()

		base := New(permRead)
		mask := base.Set(permRead)

		test.EqOp(t, permRead, mask.Value())
	})

	T.Run("does not mutate original", func(t *testing.T) {
		t.Parallel()

		original := New(permRead)
		_ = original.Set(permWrite)

		test.False(t, original.Has(permWrite))
	})
}

func TestBitmask_Clear(T *testing.T) {
	T.Parallel()

	T.Run("clear set flag", func(t *testing.T) {
		t.Parallel()

		base := New(permRead, permWrite)
		mask := base.Clear(permWrite)

		test.True(t, mask.Has(permRead))
		test.False(t, mask.Has(permWrite))
	})

	T.Run("clear unset flag", func(t *testing.T) {
		t.Parallel()

		base := New(permRead)
		mask := base.Clear(permWrite)

		test.EqOp(t, permRead, mask.Value())
	})

	T.Run("clear multiple flags", func(t *testing.T) {
		t.Parallel()

		base := New(permRead, permWrite, permDelete)
		mask := base.Clear(permRead, permWrite)

		test.False(t, mask.Has(permRead))
		test.False(t, mask.Has(permWrite))
		test.True(t, mask.Has(permDelete))
	})

	T.Run("does not mutate original", func(t *testing.T) {
		t.Parallel()

		original := New(permRead, permWrite)
		_ = original.Clear(permWrite)

		test.True(t, original.Has(permWrite))
	})
}

func TestBitmask_Toggle(T *testing.T) {
	T.Parallel()

	T.Run("toggle unset flag sets it", func(t *testing.T) {
		t.Parallel()

		base := New[testPerm]()
		mask := base.Toggle(permRead)

		test.True(t, mask.Has(permRead))
	})

	T.Run("toggle set flag clears it", func(t *testing.T) {
		t.Parallel()

		base := New(permRead)
		mask := base.Toggle(permRead)

		test.False(t, mask.Has(permRead))
	})

	T.Run("toggle multiple flags", func(t *testing.T) {
		t.Parallel()

		base := New(permRead)
		mask := base.Toggle(permRead, permWrite)

		test.False(t, mask.Has(permRead))
		test.True(t, mask.Has(permWrite))
	})

	T.Run("does not mutate original", func(t *testing.T) {
		t.Parallel()

		original := New(permRead)
		_ = original.Toggle(permRead)

		test.True(t, original.Has(permRead))
	})
}

func TestBitmask_Has(T *testing.T) {
	T.Parallel()

	T.Run("returns true for set flag", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permWrite)

		test.True(t, mask.Has(permRead))
	})

	T.Run("returns false for unset flag", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.False(t, mask.Has(permWrite))
	})

	T.Run("returns false for zero flag", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.False(t, mask.Has(0))
	})

	T.Run("with empty bitmask", func(t *testing.T) {
		t.Parallel()

		mask := New[testPerm]()

		test.False(t, mask.Has(permRead))
	})
}

func TestBitmask_HasAll(T *testing.T) {
	T.Parallel()

	T.Run("returns true when all flags set", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permWrite, permDelete)

		test.True(t, mask.HasAll(permRead, permWrite))
	})

	T.Run("returns false when one flag missing", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.False(t, mask.HasAll(permRead, permWrite))
	})

	T.Run("returns false for empty flags", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.False(t, mask.HasAll())
	})

	T.Run("returns false for zero flag", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.False(t, mask.HasAll(0))
	})
}

func TestBitmask_HasAny(T *testing.T) {
	T.Parallel()

	T.Run("returns true when one flag set", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.True(t, mask.HasAny(permRead, permWrite))
	})

	T.Run("returns false when no flags set", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.False(t, mask.HasAny(permWrite, permDelete))
	})

	T.Run("returns false for empty flags", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.False(t, mask.HasAny())
	})
}

func TestBitmask_IsEmpty(T *testing.T) {
	T.Parallel()

	T.Run("returns true for empty bitmask", func(t *testing.T) {
		t.Parallel()

		mask := New[testPerm]()

		test.True(t, mask.IsEmpty())
	})

	T.Run("returns false for non-empty bitmask", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.False(t, mask.IsEmpty())
	})

	T.Run("returns true for zero value", func(t *testing.T) {
		t.Parallel()

		var mask Bitmask[testPerm]

		test.True(t, mask.IsEmpty())
	})
}

func TestBitmask_Count(T *testing.T) {
	T.Parallel()

	T.Run("counts zero bits", func(t *testing.T) {
		t.Parallel()

		mask := New[testPerm]()

		test.EqOp(t, 0, mask.Count())
	})

	T.Run("counts one bit", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.EqOp(t, 1, mask.Count())
	})

	T.Run("counts multiple bits", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permWrite, permDelete)

		test.EqOp(t, 3, mask.Count())
	})

	T.Run("counts all bits", func(t *testing.T) {
		t.Parallel()

		mask := FromValue(^testPerm(0))

		test.EqOp(t, 8, mask.Count())
	})
}

func TestBitmask_Union(T *testing.T) {
	T.Parallel()

	T.Run("combines two masks", func(t *testing.T) {
		t.Parallel()

		a := New(permRead)
		b := New(permWrite)
		result := a.Union(b)

		test.True(t, result.Has(permRead))
		test.True(t, result.Has(permWrite))
	})

	T.Run("union with empty", func(t *testing.T) {
		t.Parallel()

		a := New(permRead)
		b := New[testPerm]()
		result := a.Union(b)

		test.EqOp(t, a.Value(), result.Value())
	})

	T.Run("union with self", func(t *testing.T) {
		t.Parallel()

		a := New(permRead, permWrite)
		result := a.Union(a)

		test.EqOp(t, a.Value(), result.Value())
	})
}

func TestBitmask_Intersect(T *testing.T) {
	T.Parallel()

	T.Run("intersects two masks", func(t *testing.T) {
		t.Parallel()

		a := New(permRead, permWrite)
		b := New(permWrite, permDelete)
		result := a.Intersect(b)

		test.False(t, result.Has(permRead))
		test.True(t, result.Has(permWrite))
		test.False(t, result.Has(permDelete))
	})

	T.Run("intersect with no overlap", func(t *testing.T) {
		t.Parallel()

		a := New(permRead)
		b := New(permWrite)
		result := a.Intersect(b)

		test.True(t, result.IsEmpty())
	})

	T.Run("intersect with self", func(t *testing.T) {
		t.Parallel()

		a := New(permRead, permWrite)
		result := a.Intersect(a)

		test.EqOp(t, a.Value(), result.Value())
	})
}

func TestBitmask_Difference(T *testing.T) {
	T.Parallel()

	T.Run("removes other's flags", func(t *testing.T) {
		t.Parallel()

		a := New(permRead, permWrite, permDelete)
		b := New(permWrite)
		result := a.Difference(b)

		test.True(t, result.Has(permRead))
		test.False(t, result.Has(permWrite))
		test.True(t, result.Has(permDelete))
	})

	T.Run("difference with no overlap", func(t *testing.T) {
		t.Parallel()

		a := New(permRead)
		b := New(permWrite)
		result := a.Difference(b)

		test.EqOp(t, a.Value(), result.Value())
	})

	T.Run("difference with self", func(t *testing.T) {
		t.Parallel()

		a := New(permRead, permWrite)
		result := a.Difference(a)

		test.True(t, result.IsEmpty())
	})
}

func TestBitmask_String(T *testing.T) {
	T.Parallel()

	T.Run("empty bitmask", func(t *testing.T) {
		t.Parallel()

		mask := New[testPerm]()

		test.EqOp(t, "00000000", mask.String())
	})

	T.Run("single flag", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead)

		test.EqOp(t, "00000001", mask.String())
	})

	T.Run("multiple flags", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permWrite)

		test.EqOp(t, "00000011", mask.String())
	})

	T.Run("all flags", func(t *testing.T) {
		t.Parallel()

		mask := FromValue(^testPerm(0))

		test.EqOp(t, "11111111", mask.String())
	})
}

func TestBitmask_MarshalJSON(T *testing.T) {
	T.Parallel()

	T.Run("marshals as number", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permWrite)
		// Marshal the value, not &mask: the API hands back values, so this is the path
		// callers actually hit. C-09 (pointer-receiver MarshalJSON) emitted "{}" here.
		data, err := json.Marshal(mask)

		must.NoError(t, err)
		test.EqOp(t, "3", string(data))
	})

	T.Run("marshals zero", func(t *testing.T) {
		t.Parallel()

		mask := New[testPerm]()
		data, err := json.Marshal(mask)

		must.NoError(t, err)
		test.EqOp(t, "0", string(data))
	})

	T.Run("marshals in struct", func(t *testing.T) {
		t.Parallel()

		type wrapper struct {
			Perms Bitmask[testPerm] `json:"perms"`
		}

		// Marshal the struct by value — an unaddressable field is exactly where a
		// pointer-receiver MarshalJSON would silently emit "{}".
		w := wrapper{Perms: New(permRead, permDelete)}
		data, err := json.Marshal(w)

		must.NoError(t, err)
		test.EqOp(t, `{"perms":5}`, string(data))
	})

	T.Run("stringer works on a value", func(t *testing.T) {
		t.Parallel()

		mask := New(permRead, permWrite)
		//nolint:gocritic // intentionally exercising fmt's %v to prove the value-receiver Stringer fires
		test.EqOp(t, "00000011", fmt.Sprintf("%v", mask))
	})
}

func TestBitmask_UnmarshalJSON(T *testing.T) {
	T.Parallel()

	T.Run("unmarshals number", func(t *testing.T) {
		t.Parallel()

		var mask Bitmask[testPerm]
		err := json.Unmarshal([]byte("3"), &mask)

		must.NoError(t, err)
		test.True(t, mask.Has(permRead))
		test.True(t, mask.Has(permWrite))
	})

	T.Run("unmarshals zero", func(t *testing.T) {
		t.Parallel()

		var mask Bitmask[testPerm]
		err := json.Unmarshal([]byte("0"), &mask)

		must.NoError(t, err)
		test.True(t, mask.IsEmpty())
	})

	T.Run("returns error for invalid input", func(t *testing.T) {
		t.Parallel()

		var mask Bitmask[testPerm]
		err := json.Unmarshal([]byte(`"not a number"`), &mask)

		test.Error(t, err)
	})

	T.Run("returns error for negative number", func(t *testing.T) {
		t.Parallel()

		var mask Bitmask[testPerm]
		err := json.Unmarshal([]byte("-1"), &mask)

		test.Error(t, err)
	})

	T.Run("returns error for value exceeding the type width", func(t *testing.T) {
		t.Parallel()

		// testPerm is uint8; 511 does not fit and must error rather than truncating to 255.
		var mask Bitmask[testPerm]
		err := json.Unmarshal([]byte("511"), &mask)

		test.Error(t, err)
	})

	T.Run("unmarshals the maximum value for the type width", func(t *testing.T) {
		t.Parallel()

		var mask Bitmask[testPerm]
		err := json.Unmarshal([]byte("255"), &mask)

		must.NoError(t, err)
		test.EqOp(t, testPerm(255), mask.Value())
	})

	T.Run("unmarshals in struct", func(t *testing.T) {
		t.Parallel()

		type wrapper struct {
			Perms Bitmask[testPerm] `json:"perms"`
		}

		var w wrapper
		err := json.Unmarshal([]byte(`{"perms":5}`), &w)

		must.NoError(t, err)
		test.True(t, w.Perms.Has(permRead))
		test.True(t, w.Perms.Has(permDelete))
	})

	T.Run("round trip", func(t *testing.T) {
		t.Parallel()

		original := New(permRead, permWrite, permAdmin)
		data, err := json.Marshal(original)
		must.NoError(t, err)

		var restored Bitmask[testPerm]
		err = json.Unmarshal(data, &restored)
		must.NoError(t, err)

		test.EqOp(t, original.Value(), restored.Value())
	})
}

func TestBitmask_Immutability(T *testing.T) {
	T.Parallel()

	T.Run("chained operations", func(t *testing.T) {
		t.Parallel()

		a := New(permRead)
		b := a.Set(permWrite)
		c := b.Clear(permRead)

		test.EqOp(t, permRead, a.Value())
		test.EqOp(t, permRead|permWrite, b.Value())
		test.EqOp(t, permWrite, c.Value())
	})
}

func TestBitmask_uint16(T *testing.T) {
	T.Parallel()

	type flag16 uint16

	const (
		f1 flag16 = 1 << iota
		f2
		f3
	)

	T.Run("string has 16 digits", func(t *testing.T) {
		t.Parallel()

		mask := New(f1, f3)

		test.EqOp(t, "0000000000000101", mask.String())
	})

	T.Run("operations work", func(t *testing.T) {
		t.Parallel()

		base := New(f1, f2)
		mask := base.Clear(f1)

		test.False(t, mask.Has(f1))
		test.True(t, mask.Has(f2))
	})
}

func TestBitmask_uint32(T *testing.T) {
	T.Parallel()

	type flag32 uint32

	T.Run("count with wider type", func(t *testing.T) {
		t.Parallel()

		mask := FromValue(flag32(0b11110000_00001111))

		test.EqOp(t, 8, mask.Count())
	})

	T.Run("string has 32 digits", func(t *testing.T) {
		t.Parallel()

		mask := New(flag32(1))

		test.EqOp(t, 32, len(mask.String()))
	})
}
