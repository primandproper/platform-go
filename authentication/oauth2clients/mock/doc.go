// Package oauth2clientsmock provides moq-generated mock implementations of
// interfaces in the oauth2clients package. The primary consumer is external
// tests that need to mock oauth2clients.Store or oauth2clients.Hooks —
// oauth2clients' own tests do not depend on this package.
package oauth2clientsmock

// Regenerate via `go generate ./authentication/oauth2clients/mock/`.

//go:generate go tool github.com/matryer/moq -out oauth2clients_mock.go -pkg oauth2clientsmock -rm -fmt goimports .. Store:StoreMock Hooks:HooksMock
