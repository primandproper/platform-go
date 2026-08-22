package canonical

import (
	"bytes"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	"github.com/primandproper/platform-go/v13/cryptography/hashing/sha256"
	"github.com/primandproper/platform-go/v13/errors"
)

// Option configures canonicalization.
type Option func(*options)

type options struct {
	dropKeys map[string]struct{}
}

// WithoutKeys excludes the named top-level object keys (their encoded JSON
// names, i.e. after struct tags apply) from the canonical form. Use it to
// keep a value's own content-hash field out of its own digest. It has no
// effect when the encoded value is not a JSON object.
func WithoutKeys(keys ...string) Option {
	return func(o *options) {
		if o.dropKeys == nil {
			o.dropKeys = make(map[string]struct{}, len(keys))
		}
		for _, k := range keys {
			o.dropKeys[k] = struct{}{}
		}
	}
}

// Sum returns the hex-encoded SHA-256 digest of v's canonical form. Two calls
// with semantically identical values return identical digests; see the
// package documentation for the canonicalization rules.
func Sum(v any, opts ...Option) (string, error) {
	return SumWith(v, sha256.NewSHA256Hasher(), opts...)
}

// SumWith is Sum with a caller-chosen hashing.Hasher, for digests other than
// SHA-256. The Hasher's cryptographic-strength caveats apply unchanged: a
// non-cryptographic Hasher yields a digest suitable for change detection
// among trusted parties, not for tamper resistance.
func SumWith(v any, hasher hashing.Hasher, opts ...Option) (string, error) {
	if hasher == nil {
		return "", errors.New("nil hasher provided")
	}

	canon, err := Marshal(v, opts...)
	if err != nil {
		return "", err
	}

	return hashing.Hex(hasher, canon), nil
}

// Marshal returns v's canonical JSON encoding: encoding/json's output
// re-emitted with all object keys sorted in lexicographic byte order and no
// insignificant whitespace. It is exposed so callers can inspect, log, or
// cross-check the exact bytes a digest was computed over.
func Marshal(v any, opts ...Option) ([]byte, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	// encoding/json does the semantic encoding (struct tags, omitempty,
	// MarshalJSON); canonicalization below only reorders and compacts what it
	// produced.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, errors.Wrap(err, "encoding value")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	// Preserve each number's encoded text verbatim rather than round-tripping
	// through float64, which could alter the representation.
	dec.UseNumber()

	var parsed any
	if err = dec.Decode(&parsed); err != nil {
		return nil, errors.Wrap(err, "reparsing encoded value")
	}

	if top, ok := parsed.(map[string]any); ok {
		for k := range o.dropKeys {
			delete(top, k)
		}
	}

	// The canonical form differs from raw only by key order and whitespace, so
	// raw's length is a close upper bound and one allocation covers the whole
	// emission in the common case.
	return appendCanonical(make([]byte, 0, len(raw)), parsed)
}

// appendCanonical appends one parsed JSON value's canonical form to dst.
//
// It appends rather than writing to a bytes.Buffer so that strings can be
// encoded in place. Routing them through json.Marshal instead would allocate a
// throwaway slice for every object key and every string value — the dominant
// cost of canonicalizing anything string-heavy, and one paid per string rather
// than per document.
func appendCanonical(dst []byte, v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return append(dst, "null"...), nil
	case bool:
		if t {
			return append(dst, "true"...), nil
		}

		return append(dst, "false"...), nil
	case json.Number:
		return append(dst, t.String()...), nil
	case string:
		return appendJSONString(dst, t), nil
	case []any:
		var err error

		dst = append(dst, '[')

		for i, elem := range t {
			if i > 0 {
				dst = append(dst, ',')
			}

			if dst, err = appendCanonical(dst, elem); err != nil {
				return nil, err
			}
		}

		return append(dst, ']'), nil
	case map[string]any:
		var err error

		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)

		dst = append(dst, '{')

		for i, k := range keys {
			if i > 0 {
				dst = append(dst, ',')
			}

			dst = appendJSONString(dst, k)
			dst = append(dst, ':')

			if dst, err = appendCanonical(dst, t[k]); err != nil {
				return nil, err
			}
		}

		return append(dst, '}'), nil
	default:
		// Unreachable: json.Decoder with UseNumber produces only the types
		// above. Guarded so a future decoder change fails loudly, not
		// silently mis-hashes.
		return nil, errors.Newf("unexpected parsed JSON type %T", v)
	}
}

// hexDigits indexes the lowercase hex nibbles used by \uXXXX escapes.
const hexDigits = "0123456789abcdef"

// appendJSONString appends s to dst as a JSON string literal, producing bytes
// identical to encoding/json.Marshal of the same string — including its HTML
// escaping of <, > and &, its  /  escaping, and its replacement of
// invalid UTF-8 with �.
//
// That equivalence is the whole contract, and it is not a matter of taste: this
// package's digests are stable identifiers, so any divergence from what
// json.Marshal would have emitted silently changes every hash computed over a
// string containing the affected byte. TestAppendJSONStringMatchesEncodingJSON
// holds the two implementations against each other rather than against a table
// of expectations, so a future change to encoding/json's escaping is caught
// here instead of in a consumer's mismatched digest.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')

	// start marks the beginning of the run of bytes that need no escaping, so
	// unescaped spans are copied in bulk rather than a byte at a time.
	start := 0

	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			// <, > and & are escaped because encoding/json escapes them by
			// default, so that output is safe to embed in HTML.
			if b >= ' ' && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&' {
				i++

				continue
			}

			dst = append(dst, s[start:i]...)

			switch b {
			case '\\', '"':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
			}

			i++
			start = i

			continue
		}

		c, size := utf8.DecodeRuneInString(s[i:])

		// Invalid UTF-8 becomes the escaped replacement character, matching
		// encoding/json, so that a canonical form is always valid UTF-8.
		if c == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', 'f', 'f', 'f', 'd')
			i += size
			start = i

			continue
		}

		// U+2028 and U+2029 are valid JSON but not valid JavaScript string
		// content; encoding/json escapes them, so this must too.
		if c == ' ' || c == ' ' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[c&0xF])
			i += size
			start = i

			continue
		}

		i += size
	}

	dst = append(dst, s[start:]...)

	return append(dst, '"')
}
