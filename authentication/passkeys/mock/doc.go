// Package passkeysmock provides moq-generated mock implementations of interfaces
// in the passkeys package. The primary consumers are external tests that need to
// stand in for passkeys.Store without a database.
package passkeysmock

// Regenerate via `go generate ./authentication/passkeys/mock/`.

//go:generate go tool github.com/matryer/moq -out passkeys_mock.go -pkg passkeysmock -rm -fmt goimports .. Store:StoreMock
