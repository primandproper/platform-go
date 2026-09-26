// Package grantsmock provides moq-generated mock implementations of interfaces
// in the grants package. The primary consumers are external tests that need to
// stand in for grants.Store or grants.Exchanger without a database or a
// provider.
package grantsmock

// Regenerate via `go generate ./authentication/grants/mock/`.

//go:generate go tool github.com/matryer/moq -out grants_mock.go -pkg grantsmock -rm -fmt goimports .. Store:StoreMock Exchanger:ExchangerMock
