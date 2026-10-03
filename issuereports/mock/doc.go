// Package issuereportsmock provides moq-generated mock implementations of
// interfaces in the issuereports package. The primary consumers are external
// tests that need to stand in for issuereports.Store without a database.
package issuereportsmock

// Regenerate via `go generate ./issuereports/mock/`.

//go:generate go tool github.com/matryer/moq -out issuereports_mock.go -pkg issuereportsmock -rm -fmt goimports .. Store:StoreMock Hooks:HooksMock
