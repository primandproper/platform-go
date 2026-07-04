package encoding

import (
	"mime"
	"strings"
)

var (
	// ContentTypeJSON is to indicate we want JSON for some reason.
	ContentTypeJSON ContentType = buildContentType(contentTypeJSON)
	// ContentTypeXML is to indicate we want XML for some reason.
	ContentTypeXML ContentType = buildContentType(contentTypeXML)
	// ContentTypeTOML is to indicate we want TOML for some reason.
	ContentTypeTOML ContentType = buildContentType(contentTypeTOML)
	// ContentTypeYAML is to indicate we want YAML for some reason.
	ContentTypeYAML ContentType = buildContentType(contentTypeYAML)
	// ContentTypeEmoji is to indicate we want Emoji for some reason.
	ContentTypeEmoji ContentType = buildContentType(contentTypeEmoji)
)

type (
	// ContentType is the publicly accessible version of contentType.
	ContentType *contentType

	contentType *string
)

var (
	ContentTypes = []ContentType{
		ContentTypeJSON,
		ContentTypeXML,
		ContentTypeTOML,
		ContentTypeYAML,
		ContentTypeEmoji,
	}
)

func (e *clientEncoder) ContentType() string {
	return ContentTypeToString(e.contentType)
}

func buildContentType(s string) *contentType {
	ct := contentType(&s)

	return &ct
}

// ContentTypeToString allows a content type to be converted to a string.
func ContentTypeToString(c ContentType) string {
	switch c {
	case ContentTypeJSON:
		return contentTypeJSON
	case ContentTypeXML:
		return contentTypeXML
	case ContentTypeTOML:
		return contentTypeTOML
	case ContentTypeYAML:
		return contentTypeYAML
	case ContentTypeEmoji:
		return contentTypeEmoji
	default:
		return ""
	}
}

func contentTypeFromString(val string) ContentType {
	// parse off any parameters (e.g. "application/xml; charset=utf-8") so we match on the
	// base media type; fall back to the raw value if it has no parameters to parse.
	base := strings.ToLower(strings.TrimSpace(val))
	if mediaType, _, err := mime.ParseMediaType(val); err == nil {
		base = mediaType
	}

	switch base {
	case contentTypeJSON:
		return ContentTypeJSON
	case contentTypeXML:
		return ContentTypeXML
	case contentTypeTOML:
		return ContentTypeTOML
	case contentTypeYAML:
		return ContentTypeYAML
	case contentTypeEmoji:
		return ContentTypeEmoji
	default:
		return defaultContentType
	}
}
