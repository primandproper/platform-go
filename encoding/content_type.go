package encoding

import (
	"mime"
	"strings"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// ContentType is a media type this package can encode and decode.
//
// It is a string-backed value type: comparable with ==, usable as a map key,
// printable, and with no pointer identity to get wrong. The zero value is the
// empty ContentType, which no constructor in this package returns — an unknown
// media type is reported as ErrUnsupportedContentType rather than silently
// standing in for JSON.
type ContentType string

const (
	// ContentTypeJSON selects JSON encoding.
	ContentTypeJSON ContentType = contentTypeJSON
	// ContentTypeXML selects XML encoding.
	ContentTypeXML ContentType = contentTypeXML
	// ContentTypeTOML selects TOML encoding.
	ContentTypeTOML ContentType = contentTypeTOML
	// ContentTypeYAML selects YAML encoding.
	ContentTypeYAML ContentType = contentTypeYAML
	// ContentTypeCBOR selects CBOR encoding (RFC 8949) — the binary option,
	// smaller than JSON on the wire and readable outside Go. Struct tags carry
	// over: a field with no cbor tag falls back to its json tag.
	ContentTypeCBOR ContentType = contentTypeCBOR
	// ContentTypeEmoji selects Ecoji-over-gob encoding.
	ContentTypeEmoji ContentType = contentTypeEmoji
)

// ErrUnsupportedContentType is returned when a media type does not name one of
// the encodings this package implements.
var ErrUnsupportedContentType = platformerrors.New("unsupported content type")

// ContentTypes are every content type this package supports, in no significant
// order.
var ContentTypes = []ContentType{
	ContentTypeJSON,
	ContentTypeXML,
	ContentTypeTOML,
	ContentTypeYAML,
	ContentTypeCBOR,
	ContentTypeEmoji,
}

// String returns the media type as it appears in a Content-Type header.
func (c ContentType) String() string {
	return string(c)
}

// Valid reports whether c is one of the content types this package implements.
func (c ContentType) Valid() bool {
	switch c {
	case ContentTypeJSON, ContentTypeXML, ContentTypeTOML, ContentTypeYAML, ContentTypeCBOR, ContentTypeEmoji:
		return true
	default:
		return false
	}
}

func (e *Encoder) ContentType() string {
	return e.contentType.String()
}

// ParseContentType resolves a media type — with or without parameters, in any
// case — to the ContentType that names it.
//
// It returns ErrUnsupportedContentType for anything it does not implement,
// including the empty string. Callers that want a default must say so; this
// package will not choose one for them.
func ParseContentType(val string) (ContentType, error) {
	base := strings.ToLower(strings.TrimSpace(val))
	if mediaType, _, err := mime.ParseMediaType(val); err == nil {
		base = mediaType
	}

	if ct := ContentType(base); ct.Valid() {
		return ct, nil
	}

	return "", platformerrors.Wrapf(ErrUnsupportedContentType, "parsing content type %q", val)
}

// contentTypeFromRequestHeader resolves a request's Content-Type header,
// falling back to JSON.
//
// The fallback is deliberate and is scoped to inbound HTTP only: a request may
// legitimately omit the header, and a server that refused every unlabeled body
// would reject clients the rest of the ecosystem accepts. Configuration goes
// through ParseContentType instead, where an unrecognized value is an error.
func contentTypeFromRequestHeader(val string) ContentType {
	ct, err := ParseContentType(val)
	if err != nil {
		return defaultContentType
	}

	return ct
}
