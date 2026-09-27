// Package seriesmock provides moq-generated mock implementations of interfaces
// in the series package. The primary consumer is external tests that need to
// stand in for series.Store without a database.
package seriesmock

// Regenerate via `go generate ./series/mock/`.

//go:generate go tool github.com/matryer/moq -out series_mock.go -pkg seriesmock -rm -fmt goimports .. Store:StoreMock
