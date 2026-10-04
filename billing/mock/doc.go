// Package billingmock provides moq-generated mock implementations of interfaces
// in the billing package. The primary consumers are external tests that need to
// stand in for billing.Store without a database.
package billingmock

// Regenerate via `go generate ./billing/mock/`.

//go:generate go tool github.com/matryer/moq -out billing_mock.go -pkg billingmock -rm -fmt goimports .. Store:StoreMock ProductStore:ProductStoreMock SubscriptionStore:SubscriptionStoreMock PurchaseStore:PurchaseStoreMock TransactionStore:TransactionStoreMock Hooks:HooksMock
