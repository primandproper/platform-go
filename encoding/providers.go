package encoding

// NewContentType provides a ContentType from a Config.
func NewContentType(cfg Config) ContentType {
	return contentTypeFromString(cfg.ContentType)
}
