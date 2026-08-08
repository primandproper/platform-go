// Package entitlementsmock provides moq-generated mock implementations of
// interfaces in the entitlements package. The primary consumers are external
// tests that need to stand in for entitlements.Checker without a plan store, a
// flag provider, or a metering database — a handler test asserting that an
// endpoint answers 402 for an account whose plan excludes the feature, most
// often.
//
// PlanSource is deliberately absent. It is a single-method interface with a
// function adapter in the package itself (PlanSourceFunc), and a closure is a
// better test double than a generated struct when the whole implementation is
// one method.
//
// There is nothing here for Catalog or QuotaSource either. Catalog is a concrete
// map built at wiring time — a test that wants one builds one, in three lines,
// and gets the registration validation along with it. QuotaSource is a lookup
// over that same catalog, and a test that mocked it would be asserting against
// its own arithmetic.
package entitlementsmock

// Regenerate the moq mocks via `go generate ./entitlements/mock/`.

//go:generate go tool github.com/matryer/moq -out entitlements_mock.go -pkg entitlementsmock -rm -fmt goimports .. Checker:CheckerMock
