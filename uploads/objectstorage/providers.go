package objectstorage

import (
	"github.com/primandproper/platform-go/v2/uploads"
)

// ProvideUploadManager transforms an *objectstorage.Uploader into an UploadManager.
func ProvideUploadManager(u *Uploader) uploads.UploadManager {
	return u
}
