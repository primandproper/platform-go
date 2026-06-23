// Package mock provides moq-generated mock implementations of interfaces in
// the cache package. The primary consumer is external tests that need to mock
// cache.Cache[T] or cache.BatchCache[T] — cache's own tests do not depend on
// this package.
package mock

// Regenerate via `go generate ./cache/mock/`.

//go:generate go tool github.com/matryer/moq -out cache_mock.go -pkg mock -rm -fmt goimports .. Cache:CacheMock
//go:generate go tool github.com/matryer/moq -out batch_cache_mock.go -pkg mock -rm -fmt goimports .. BatchCache:BatchCacheMock
