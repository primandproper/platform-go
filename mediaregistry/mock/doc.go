// Package mediaregistrymock provides moq-generated mock implementations of
// interfaces in the mediaregistry package. The primary consumers are external
// tests that need to stand in for mediaregistry.Store without a database.
package mediaregistrymock

// Regenerate via `go generate ./mediaregistry/mock/`.

//go:generate go tool github.com/matryer/moq -out mediaregistry_mock.go -pkg mediaregistrymock -rm -fmt goimports .. Store:StoreMock
