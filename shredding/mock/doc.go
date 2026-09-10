/*
Package shreddingmock provides moq-generated mock implementations of the
shredding package's interfaces.
*/
package shreddingmock

// Regenerate the moq mocks via `go generate ./shredding/mock/`.

//go:generate go tool github.com/matryer/moq -out keys_mock.go -pkg shreddingmock -rm -fmt goimports .. Keys:KeysMock
//go:generate go tool github.com/matryer/moq -out store_mock.go -pkg shreddingmock -rm -fmt goimports .. Store:StoreMock
//go:generate go tool github.com/matryer/moq -out broadcaster_mock.go -pkg shreddingmock -rm -fmt goimports .. Broadcaster:BroadcasterMock
