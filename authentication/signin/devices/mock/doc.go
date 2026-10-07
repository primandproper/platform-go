// Package devicesmock provides moq-generated mock implementations of interfaces
// in the devices package. The primary consumers are external tests that need to
// stand in for devices.Store without a database — devices' own tests of its
// hooks and annotator use it too, since neither touches SQL.
package devicesmock

// Regenerate via `go generate ./authentication/signin/devices/mock/`.

//go:generate go tool github.com/matryer/moq -out devices_mock.go -pkg devicesmock -rm -fmt goimports .. Store:StoreMock
