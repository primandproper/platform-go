package textsearch

import (
	"encoding/base64"
	"encoding/json"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Cursor is an opaque resumption token. Callers pass it back verbatim and must
// not construct, parse, or persist meaning from one: the encoding belongs to
// the backend that issued it and is expected to change.
//
// It is opaque precisely because the backends do not agree on what resumption
// means. Both currently encode an offset, but Elasticsearch's eventual move to
// search_after carries the previous hit's sort values instead, which is not an
// offset and cannot be one. Spelling the interface as an offset would have made
// that a breaking change; spelling it as a token makes it an implementation
// detail.
type Cursor string

// IsZero reports whether the cursor is unset, meaning "start from the
// beginning" on the way in and "no more results" on the way out.
func (c Cursor) IsZero() bool {
	return c == ""
}

// cursorPayload is what a Cursor decodes to. The backend tag is carried so that
// handing an Elasticsearch cursor to Algolia is refused rather than silently
// interpreted as a position that happens to parse — the two do not count in the
// same units.
type cursorPayload struct {
	Backend  string `json:"b"`
	Position int    `json:"p"`
}

// EncodeCursor builds an opaque cursor for the given backend.
//
// position is whatever resuming means to that backend — a document offset for
// Elasticsearch, a page number for Algolia. Nothing outside the issuing backend
// interprets it, which is the point of the token being opaque.
//
// It is exported for backend implementations, not for callers of Search.
func EncodeCursor(backend string, position int) (Cursor, error) {
	raw, err := json.Marshal(cursorPayload{Backend: backend, Position: position})
	if err != nil {
		return "", platformerrors.Wrap(err, "encoding search cursor")
	}

	return Cursor(base64.RawURLEncoding.EncodeToString(raw)), nil
}

// DecodeCursor reads a cursor issued by the named backend, returning the
// position it resumes at. A zero cursor decodes to 0 with no error, so a
// backend need not special-case the first page.
//
// It is exported for backend implementations, not for callers of Search.
func DecodeCursor(backend string, c Cursor) (int, error) {
	if c.IsZero() {
		return 0, nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(string(c))
	if err != nil {
		return 0, platformerrors.Wrap(ErrInvalidCursor, "decoding search cursor")
	}

	var payload cursorPayload
	if err = json.Unmarshal(raw, &payload); err != nil {
		return 0, platformerrors.Wrap(ErrInvalidCursor, "unmarshaling search cursor")
	}

	if payload.Backend != backend {
		return 0, platformerrors.Wrapf(ErrInvalidCursor, "cursor is for backend %q, not %q", payload.Backend, backend)
	}

	if payload.Position < 0 {
		return 0, platformerrors.Wrap(ErrInvalidCursor, "negative position")
	}

	return payload.Position, nil
}

// EffectiveLimit resolves a requested limit against the default and the
// backend's own ceiling. It exists so the three backends cannot disagree about
// what an unset limit means, which is how they ended up capping at 10 and 20.
func EffectiveLimit(requested, ceiling int) int {
	if requested <= 0 {
		requested = DefaultSearchLimit
	}

	if ceiling > 0 && requested > ceiling {
		return ceiling
	}

	return requested
}
