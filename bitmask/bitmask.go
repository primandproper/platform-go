package bitmask

import (
	"fmt"
	"math/bits"
	"strconv"

	"github.com/primandproper/platform-go/v5/errors"
)

// Unsigned is a constraint for fixed-width unsigned integer types that can be used as bitmask values.
type Unsigned interface {
	~uint8 | ~uint16 | ~uint32 | ~uint64
}

// Bitmask is a generic, immutable bitmask over any unsigned integer type.
// Each operation returns a new Bitmask, leaving the original unchanged.
type Bitmask[T Unsigned] struct {
	value T
}

// New creates a Bitmask with the given flags set.
func New[T Unsigned](flags ...T) Bitmask[T] {
	var v T
	for _, f := range flags {
		v |= f
	}

	return Bitmask[T]{value: v}
}

// FromValue creates a Bitmask from a raw integer value.
func FromValue[T Unsigned](value T) Bitmask[T] {
	return Bitmask[T]{value: value}
}

// Value returns the underlying integer value.
func (b *Bitmask[T]) Value() T {
	return b.value
}

// Set returns a new Bitmask with the given flags set.
func (b *Bitmask[T]) Set(flags ...T) Bitmask[T] {
	v := b.value
	for _, f := range flags {
		v |= f
	}

	return Bitmask[T]{value: v}
}

// Clear returns a new Bitmask with the given flags cleared.
func (b *Bitmask[T]) Clear(flags ...T) Bitmask[T] {
	v := b.value
	for _, f := range flags {
		v &^= f
	}

	return Bitmask[T]{value: v}
}

// Toggle returns a new Bitmask with the given flags toggled.
func (b *Bitmask[T]) Toggle(flags ...T) Bitmask[T] {
	v := b.value
	for _, f := range flags {
		v ^= f
	}

	return Bitmask[T]{value: v}
}

// Has reports whether the given flag is set.
func (b *Bitmask[T]) Has(flag T) bool {
	return flag != 0 && b.value&flag == flag
}

// HasAll reports whether all the given flags are set.
func (b *Bitmask[T]) HasAll(flags ...T) bool {
	var combined T
	for _, f := range flags {
		combined |= f
	}

	return combined != 0 && b.value&combined == combined
}

// HasAny reports whether any of the given flags are set.
func (b *Bitmask[T]) HasAny(flags ...T) bool {
	var combined T
	for _, f := range flags {
		combined |= f
	}

	return b.value&combined != 0
}

// IsEmpty reports whether no flags are set.
func (b *Bitmask[T]) IsEmpty() bool {
	return b.value == 0
}

// Count returns the number of set bits.
func (b *Bitmask[T]) Count() int {
	return bits.OnesCount64(uint64(b.value))
}

// Union returns a new Bitmask with the flags set in either bitmask.
func (b *Bitmask[T]) Union(other Bitmask[T]) Bitmask[T] {
	return Bitmask[T]{value: b.value | other.value}
}

// Intersect returns a new Bitmask with only the flags set in both bitmasks.
func (b *Bitmask[T]) Intersect(other Bitmask[T]) Bitmask[T] {
	return Bitmask[T]{value: b.value & other.value}
}

// Difference returns a new Bitmask with flags set in b but not in other.
func (b *Bitmask[T]) Difference(other Bitmask[T]) Bitmask[T] {
	return Bitmask[T]{value: b.value &^ other.value}
}

// String returns a zero-padded binary string representation of the bitmask.
func (b Bitmask[T]) String() string {
	width := bits.OnesCount64(uint64(^T(0)))

	return fmt.Sprintf("%0*b", width, uint64(b.value))
}

// MarshalJSON implements json.Marshaler by encoding the bitmask as a bare number.
func (b Bitmask[T]) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatUint(uint64(b.value), 10)), nil
}

// UnmarshalJSON implements json.Unmarshaler by decoding a bare number into the bitmask.
func (b *Bitmask[T]) UnmarshalJSON(data []byte) error {
	bitSize := bits.OnesCount64(uint64(^T(0)))
	v, err := strconv.ParseUint(string(data), 10, bitSize)
	if err != nil {
		return errors.Wrap(err, "bitmask: invalid JSON value")
	}

	b.value = T(v)

	return nil
}
