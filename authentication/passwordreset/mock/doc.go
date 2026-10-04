// Package passwordresetmock provides moq-generated mock implementations of
// interfaces in the passwordreset package. The primary consumers are external
// tests that need to stand in for passwordreset.Store without a database —
// passwordreset's own tests do not depend on this package.
package passwordresetmock

// Regenerate via `go generate ./authentication/passwordreset/mock/`.

//go:generate go tool github.com/matryer/moq -out passwordreset_mock.go -pkg passwordresetmock -rm -fmt goimports .. Store:StoreMock Hooks:HooksMock
