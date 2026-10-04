// Package waitlistsmock provides moq-generated mock implementations of
// interfaces in the waitlists package. The primary consumers are external tests
// that need to stand in for waitlists.Store without a database.
package waitlistsmock

// Regenerate via `go generate ./waitlists/mock/`.

//go:generate go tool github.com/matryer/moq -out waitlists_mock.go -pkg waitlistsmock -rm -fmt goimports .. Store:StoreMock ListStore:ListStoreMock SignupStore:SignupStoreMock Hooks:HooksMock
