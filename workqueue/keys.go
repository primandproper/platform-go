package workqueue

import (
	"reflect"
	"strings"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// MaxKeyLength bounds an encoded key.
//
// The encoded key is half of the table's primary key, and a primary key has to
// be indexable. The limit is enforced in Go rather than by the column, because
// the failure it prevents is not a rejected write: two keys that differ only
// past the limit encode to the same row, and the second unit of work vanishes
// into the first with nothing to detect it.
const MaxKeyLength = 512

// KeyCodec translates a payload key to and from the text stored in the queue's
// primary key column.
//
// The default handles the two shapes that cover nearly everything — see
// DefaultKeyCodec — so this exists for keys that need a specific rendering: one
// that has to sort a particular way, one with a canonical string form already,
// or one whose Go type is about to change in a way JSON would notice.
//
// Whatever a codec produces has to be stable forever. It is the identity of a
// row: a rendering that changes between releases does not migrate the queue, it
// silently forks it, and the old rows stay claimable under keys nothing will
// ever complete.
type KeyCodec[K comparable] interface {
	// EncodeKey renders key as the text stored in the primary key column.
	EncodeKey(key K) (string, error)
	// DecodeKey is EncodeKey's inverse, applied to keys read back from a claim.
	DecodeKey(encoded string) (K, error)
}

// DefaultKeyCodec is the codec a Queue uses when none is supplied.
//
// A string, or any type whose underlying type is string, is stored as itself.
// That is not only shorter than the JSON rendering, it keeps the table legible:
// an operator reading item_key sees the key, not a quoted one.
//
// Everything else is JSON. K is comparable, which is most of what makes that
// safe — maps and slices cannot be keys in the first place, so the only
// remaining source of instability is struct field order, and Go's encoder emits
// fields in declaration order. Reordering the fields of a key struct therefore
// forks the queue, exactly as changing a custom codec would; treat the key type
// as part of the schema.
func DefaultKeyCodec[K comparable]() KeyCodec[K] {
	if reflect.TypeFor[K]().Kind() == reflect.String {
		return stringKeyCodec[K]{}
	}

	return jsonKeyCodec[K]{}
}

// stringKeyCodec stores a string-like key as itself. It goes through reflection
// rather than a ~string type constraint because it has to satisfy
// KeyCodec[K comparable], which cannot name that constraint — the caller's K is
// already fixed by the time a codec is chosen.
type stringKeyCodec[K comparable] struct{}

func (stringKeyCodec[K]) EncodeKey(key K) (string, error) {
	return reflect.ValueOf(key).String(), nil
}

func (stringKeyCodec[K]) DecodeKey(encoded string) (K, error) {
	// Written through a pointer to the zero value rather than assembled from
	// reflect.New and asserted back: the assertion could not fail, but it would
	// still have to be spelled and defended, and this needs neither.
	var key K

	reflect.ValueOf(&key).Elem().SetString(encoded)

	return key, nil
}

// jsonKeyCodec stores a key as its JSON rendering.
type jsonKeyCodec[K comparable] struct{}

func (jsonKeyCodec[K]) EncodeKey(key K) (string, error) {
	encoded, err := encoding.EncodeJSON(key)
	if err != nil {
		return "", platformerrors.Wrap(err, "encoding work queue key")
	}

	return string(encoded), nil
}

func (jsonKeyCodec[K]) DecodeKey(encoded string) (K, error) {
	var key K
	if err := encoding.Decode([]byte(encoded), encoding.ContentTypeJSON, &key); err != nil {
		return key, platformerrors.Wrap(err, "decoding work queue key")
	}

	return key, nil
}

// encodeKey renders one key and vets the result. The vetting lives here rather
// than in the codecs so that a custom codec inherits it: a caller supplying
// their own rendering should not also have to remember the column's limits.
func encodeKey[K comparable](codec KeyCodec[K], key K) (string, error) {
	encoded, err := codec.EncodeKey(key)
	if err != nil {
		return "", err
	}

	if encoded == "" {
		return "", ErrEmptyKey
	}

	if len(encoded) > MaxKeyLength {
		return "", platformerrors.Wrapf(ErrKeyTooLong,
			"encoded key is %d bytes, over the %d-byte limit", len(encoded), MaxKeyLength)
	}

	// A newline or a NUL in a primary key is legal in Postgres and always a
	// mistake here: it is what a key built by concatenating unvalidated input
	// looks like, and it makes every log line and every psql session that
	// touches the row unreadable.
	if strings.ContainsAny(encoded, "\x00\n\r") {
		return "", platformerrors.Wrapf(ErrKeyContainsControlCharacter, "encoded key %q", encoded)
	}

	return encoded, nil
}

// encodeKeys renders a batch, preserving the caller's order. Ordering for lock
// acquisition is applied later, by the statement builders that need it — a
// caller reading a returned error should see the key they passed, at the
// position they passed it.
func encodeKeys[K comparable](codec KeyCodec[K], keys []K) ([]string, error) {
	encoded := make([]string, 0, len(keys))

	for i := range keys {
		one, err := encodeKey(codec, keys[i])
		if err != nil {
			return nil, err
		}

		encoded = append(encoded, one)
	}

	return encoded, nil
}
