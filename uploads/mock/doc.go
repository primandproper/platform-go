// Package mockuploads provides moq-generated mock implementations of the uploads package
// interfaces (UploadManager and the optional capability interfaces) for use in tests.
package mockuploads

// Regenerate the moq mocks via `go generate ./uploads/mock/`.

//go:generate go tool github.com/matryer/moq -out upload_manager_mock.go -pkg mockuploads -rm -fmt goimports .. UploadManager:UploadManagerMock RangeReader:RangeReaderMock URLSigner:URLSignerMock Attributer:AttributerMock Lister:ListerMock
