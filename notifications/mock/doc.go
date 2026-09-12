// Package notificationsmock provides moq-generated mock implementations of
// interfaces in the notifications package. The primary consumers are external
// tests that need to stand in for notifications.Store, or for either half of
// it, without a database.
package notificationsmock

// Regenerate via `go generate ./notifications/mock/`.

//go:generate go tool github.com/matryer/moq -out notifications_mock.go -pkg notificationsmock -rm -fmt goimports .. Store:StoreMock Inbox:InboxMock Registry:RegistryMock
