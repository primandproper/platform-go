// Package mockrouting provides mock implementations of the routing package's
// interfaces (currently the Backend seam), for testing routers without a real
// mux library.
package mockrouting

// Regenerate the moq mocks via `go generate ./routing/mock/`.

//go:generate go tool github.com/matryer/moq -out backend_mock.go -pkg mockrouting -rm -fmt goimports .. Backend:BackendMock
