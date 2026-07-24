/*
Package mockdatabase provides moq-generated mocks for the database package.
*/
package mockdatabase

// Regenerate the moq mocks via `go generate ./database/mock/`.

//go:generate go tool github.com/matryer/moq -out database_mock.go -pkg mockdatabase -rm -fmt goimports .. Client:ClientMock RawAccess:RawAccessMock ResultIterator:ResultIteratorMock SQLQueryExecutor:SQLQueryExecutorMock
