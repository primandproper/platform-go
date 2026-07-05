package objectstorage

import (
	"github.com/primandproper/platform-go/v4/uploads"
)

const (
	// FilesystemProvider indicates we'd like to use the filesystem adapter for blob.
	FilesystemProvider = "filesystem"
	// MemoryProvider indicates we'd like to use the memory adapter for blob.
	MemoryProvider = "memory"
	// S3Provider indicates we'd like to use the s3 adapter for blob.
	S3Provider = "s3"
	// GCPCloudStorageProvider indicates we'd like to use the GCP adapter for blob objectstorage.
	GCPCloudStorageProvider = "gcp"
	// R2Provider indicates we'd like to use the Cloudflare R2 adapter for blob.
	R2Provider = "r2"
	// BackblazeB2Provider indicates we'd like to use the Backblaze B2 adapter for blob.
	BackblazeB2Provider = "backblaze_b2"
)

// ProvideUploadManager transforms an *objectstorage.Uploader into an UploadManager.
func ProvideUploadManager(u *Uploader) uploads.UploadManager {
	return u
}
