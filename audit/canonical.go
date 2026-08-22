package audit

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strconv"

	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	"github.com/primandproper/platform-go/v13/cryptography/hashing/canonical"
	"github.com/primandproper/platform-go/v13/cryptography/hashing/sha256"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// imageVersion tags the framing below. It is the first field of every image, so
// a future v2 framing produces different digests for the same entry rather than
// colliding with v1 — which is what lets a verifier tell "the format changed"
// apart from "the row changed".
const imageVersion = "audit.v1"

// ErrMalformedHash indicates a stored hash that is not hex. It is what Verify
// reports when a chain field has been overwritten with something that was never
// a digest at all.
var ErrMalformedHash = platformerrors.New("malformed audit hash")

// canonicalImage renders the exact bytes an entry's digest is taken over.
//
// The framing is length-prefixed rather than delimited: every field is written
// as an 8-byte big-endian length followed by its bytes. Concatenating fields
// with a separator would let two different entries produce the same image —
// a resource type of "a|b" with an empty ID against a type of "a" with an ID of
// "b" — and an audit log whose digest can be made to collide by choosing field
// values is not evidence of anything. With explicit lengths no such pair
// exists, whatever the fields contain.
//
// changes and metadata arrive as the encoded bytes that are actually stored,
// not as the maps they came from. That is the load-bearing choice in this file.
// Verification re-hashes what it read out of the database, so if it re-encoded
// those maps instead, the digest would depend on a JSON round trip surviving
// intact — and it does not: an int64 above 2^53 written as a number and read
// back into an `any` returns as a float64, re-encodes differently, and every
// entry after it reads as tampered. Hashing the stored bytes has no such
// failure mode, because there is no decode step between writing and verifying.
//
// RecordedAt contributes as microseconds since the epoch for the same reason in
// a different disguise: an integer has no timezone, no layout, and no precision
// left to lose on the way through a driver.
func canonicalImage(e *Entry, changes, metadata []byte) []byte {
	fields := [][]byte{
		[]byte(imageVersion),
		[]byte(strconv.FormatInt(e.Seq, 10)),
		[]byte(e.ID),
		[]byte(strconv.FormatInt(e.RecordedAt.UTC().UnixMicro(), 10)),
		[]byte(e.Scope),
		[]byte(e.ResourceType),
		[]byte(e.ResourceID),
		[]byte(e.EventType),
		[]byte(e.Actor.ID),
		[]byte(e.Actor.Type),
		[]byte(e.Actor.IP),
		changes,
		metadata,
	}

	size := 0
	for _, f := range fields {
		size += 8 + len(f)
	}

	buf := bytes.NewBuffer(make([]byte, 0, size))
	for _, f := range fields {
		_, _ = buf.Write(binary.BigEndian.AppendUint64(nil, uint64(len(f))))
		_, _ = buf.Write(f)
	}

	return buf.Bytes()
}

// chainHash digests an entry's image against its predecessor's hash, returning
// the result hex-encoded.
//
// The predecessor is mixed in as its decoded bytes rather than its hex text, so
// the digest covers 32 bytes of digest and not 64 bytes of a rendering of it.
// An empty prevHash — the first entry in a scope — contributes nothing, which
// makes the genesis entry's hash a plain digest of its own image.
func chainHash(prevHash string, image []byte) (string, error) {
	prev, err := hex.DecodeString(prevHash)
	if err != nil {
		return "", platformerrors.Wrapf(ErrMalformedHash, "decoding previous hash %q", prevHash)
	}

	return hashing.Hex(sha256.NewSHA256Hasher(), append(prev, image...)), nil
}

// encodeFields renders a map for storage, or nil for an empty one.
//
// Nil and empty collapse to the same stored value deliberately. Canonical JSON
// distinguishes them — null against {} — so keeping both would make two entries
// that say exactly the same thing hash differently depending on whether a call
// site wrote `nil` or `map[string]Change{}`.
func encodeFields[T any](m map[string]T) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}

	encoded, err := canonical.Marshal(m)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding audit fields")
	}

	return encoded, nil
}

// hashValue renders the digest recorded in place of a value that Redaction
// marked as hash-only. The prefix names the algorithm, so a reader can tell a
// redacted digest from a value that merely looks like one.
func hashValue(v any) (string, error) {
	sum, err := canonical.Sum(v)
	if err != nil {
		return "", platformerrors.Wrap(err, "hashing redacted value")
	}

	return "sha256:" + sum, nil
}
