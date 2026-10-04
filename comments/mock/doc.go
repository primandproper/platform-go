// Package commentsmock provides moq-generated mock implementations of
// interfaces in the comments package. The primary consumers are external tests
// that need to stand in for comments.Store without a database.
package commentsmock

// Regenerate via `go generate ./comments/mock/`.

//go:generate go tool github.com/matryer/moq -out comments_mock.go -pkg commentsmock -rm -fmt goimports .. Store:StoreMock Hooks:HooksMock
