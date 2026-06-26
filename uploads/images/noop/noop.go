package noop

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/uploads/images"
)

var _ images.MediaUploadProcessor = (*MediaUploadProcessor)(nil)

// MediaUploadProcessor is a no-op MediaUploadProcessor.
type MediaUploadProcessor struct{}

// NewMediaUploadProcessor returns a no-op MediaUploadProcessor.
func NewMediaUploadProcessor() images.MediaUploadProcessor {
	return &MediaUploadProcessor{}
}

// ProcessFile is a no-op that returns an empty Upload.
func (*MediaUploadProcessor) ProcessFile(context.Context, *http.Request, string) (*images.Upload, error) {
	return &images.Upload{}, nil
}

// ProcessFiles is a no-op that returns an empty slice.
func (*MediaUploadProcessor) ProcessFiles(context.Context, *http.Request, string) ([]*images.Upload, error) {
	return []*images.Upload{}, nil
}
