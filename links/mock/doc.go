// Package linksmock provides moq-generated mock implementations of interfaces
// in the links package. The primary consumers are external tests that need to
// stand in for links.Store without a database — links/database is the only
// implementation this module ships, and links' own tests do not depend on this
// package, because an in-package test importing it would close an import
// cycle.
package linksmock

// Regenerate via `go generate ./links/mock/`.

//go:generate go tool github.com/matryer/moq -out links_mock.go -pkg linksmock -rm -fmt goimports .. Store:StoreMock
