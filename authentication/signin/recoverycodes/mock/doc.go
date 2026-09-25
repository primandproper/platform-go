// Package recoverycodesmock provides moq-generated mock implementations of
// interfaces in the recoverycodes package. The primary consumers are external
// tests that need to stand in for recoverycodes.Store without a database —
// recoverycodes' own tests do not depend on this package.
package recoverycodesmock

// Regenerate via `go generate ./authentication/signin/recoverycodes/mock/`.

//go:generate go tool github.com/matryer/moq -out recoverycodes_mock.go -pkg recoverycodesmock -rm -fmt goimports .. Store:StoreMock
