// Package settingsmock provides moq-generated mock implementations of interfaces
// in the settings package. The primary consumers are external tests that need to
// stand in for settings.Store without a database.
package settingsmock

// Regenerate via `go generate ./settings/mock/`.

//go:generate go tool github.com/matryer/moq -out settings_mock.go -pkg settingsmock -rm -fmt goimports .. Store:StoreMock DefinitionStore:DefinitionStoreMock ValueStore:ValueStoreMock Hooks:HooksMock
