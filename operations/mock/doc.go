// Package operationsmock provides moq-generated mock implementations of
// interfaces in the operations package. The primary consumers are tests that
// need to stand in for the storage or the service without a database — a handler
// test asserting that an endpoint starts the operation it should, or a worker
// test driving a Runner through a failure the database would be tedious to
// produce.
//
// Reporter is deliberately absent. It is the interface a Runner is handed, so a
// test of a Runner wants to *observe* what the Runner reported rather than to
// script what a Reporter returns — and the honest way to do that is a small
// recording implementation the test owns, whose methods only append to
// slices. A generated mock of it would be more code than the thing it replaces.
//
// There is nothing here for the Worker or the Watcher either. Both are concrete
// background loops rather than seams, and a test that wants to drive one runs it
// against a real Store — the mock one, most often — which is the only way to
// observe what they actually do to a row.
package operationsmock

// Regenerate the moq mocks via `go generate ./operations/mock/`.

//go:generate go tool github.com/matryer/moq -out operations_mock.go -pkg operationsmock -rm -fmt goimports .. Store:StoreMock Service:ServiceMock
