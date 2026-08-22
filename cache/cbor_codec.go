package cache

import (
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cbormode"
)

// CBORCodec is the default Codec, using CBOR (RFC 8949). It is exported, and
// returned by NewCBORCodec, so a caller can depend on the codec it built rather
// than on the Codec seam.
type CBORCodec[T any] struct{}

var _ Codec[struct{}] = CBORCodec[struct{}]{}

// NewCBORCodec returns the default CBOR-backed Codec.
//
// It is the default because a cache holds small values one at a time, which is
// the shape gob is worst at: gob earns its compactness by amortizing type
// descriptors across a stream, and a cache entry has to be independently
// decodable by a process that never saw the stream prologue, so every entry
// re-transmits the full descriptor. On a five-field session struct that lands
// gob above JSON on size while staying Go-only. CBOR is smaller than both and
// readable by anything that speaks a documented wire format.
//
// Types need no annotation: a field with no cbor tag falls back to its json
// tag. Interface-typed fields are the one thing this codec will not do — see
// NewGobCodec.
//
// time.Time round-trips to the nanosecond, with its UTC offset. The named
// location does not survive, here or in any other portable format, so compare
// decoded times with time.Time.Equal rather than == .
func NewCBORCodec[T any]() CBORCodec[T] {
	return CBORCodec[T]{}
}

// Encode implements Codec via CBOR.
func (CBORCodec[T]) Encode(value *T) ([]byte, error) {
	out, err := cbormode.Marshal(value)
	if err != nil {
		return nil, errors.Wrap(err, "cbor-encoding value")
	}

	return out, nil
}

// Decode implements Codec via CBOR.
func (CBORCodec[T]) Decode(data []byte) (*T, error) {
	var value *T
	if err := cbormode.Unmarshal(data, &value); err != nil {
		return nil, errors.Wrap(err, "cbor-decoding value")
	}

	return value, nil
}
