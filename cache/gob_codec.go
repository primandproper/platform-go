package cache

import (
	"bytes"
	"encoding/gob"

	"github.com/primandproper/platform-go/v13/errors"
)

// GobCodec is the opt-in Codec for values CBOR cannot carry, using
// encoding/gob. It is exported, and returned by NewGobCodec, so a caller can
// depend on the codec it built rather than on the Codec seam.
type GobCodec[T any] struct{}

var _ Codec[struct{}] = GobCodec[struct{}]{}

// NewGobCodec returns the gob-backed Codec. Types must be gob-friendly:
// exported fields only, and interface-typed fields need their concrete types
// registered with gob.Register.
//
// It was the default until CBOR replaced it (NewCBORCodec, which is smaller on
// the wire and not Go-only), and is retained for the two things gob does that
// CBOR does not: interface-typed fields resolved through gob.Register, and
// decoding into a struct that has drifted from the one that was encoded. Reach
// for it when a cached value has either property, and keep in mind that
// entries written by one codec are unreadable through another.
func NewGobCodec[T any]() GobCodec[T] {
	return GobCodec[T]{}
}

// Encode implements Codec via encoding/gob.
func (GobCodec[T]) Encode(value *T) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(value); err != nil {
		return nil, errors.Wrap(err, "gob-encoding value")
	}

	return buf.Bytes(), nil
}

// Decode implements Codec via encoding/gob.
func (GobCodec[T]) Decode(data []byte) (*T, error) {
	var value *T
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&value); err != nil {
		return nil, errors.Wrap(err, "gob-decoding value")
	}

	return value, nil
}
