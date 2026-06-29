package noop

import (
	"context"

	"github.com/primandproper/platform-go/v2/uploads"
)

var _ uploads.UploadManager = (*UploadManager)(nil)

// UploadManager is a no-op UploadManager.
type UploadManager struct{}

// NewUploadManager returns a no-op UploadManager.
func NewUploadManager() uploads.UploadManager {
	return &UploadManager{}
}

// SaveFile is a no-op.
func (*UploadManager) SaveFile(context.Context, string, []byte) error {
	return nil
}

// ReadFile is a no-op that always returns empty bytes.
func (*UploadManager) ReadFile(context.Context, string) ([]byte, error) {
	return []byte{}, nil
}
