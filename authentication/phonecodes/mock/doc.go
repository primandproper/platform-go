// Package phonecodesmock provides moq-generated mock implementations of
// interfaces in the phonecodes package. The primary consumers are external
// tests that need to stand in for phonecodes.Store without a database.
package phonecodesmock

// Regenerate via `go generate ./authentication/phonecodes/mock/`.

//go:generate go tool github.com/matryer/moq -out phonecodes_mock.go -pkg phonecodesmock -rm -fmt goimports .. Store:StoreMock
