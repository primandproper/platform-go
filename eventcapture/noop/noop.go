// Package noop provides a no-op eventcapture.Sink, for deployments with
// capture wired but disabled.
package noop

import (
	"github.com/primandproper/platform-go/v13/eventcapture"
)

var _ eventcapture.Sink = (*Sink)(nil)

// Sink is a no-op eventcapture.Sink.
type Sink struct{}

// NewSink returns a no-op Sink.
func NewSink() *Sink {
	return &Sink{}
}

// Write discards the record.
func (*Sink) Write(any) error {
	return nil
}

// Flush is a no-op.
func (*Sink) Flush() error {
	return nil
}

// Close is a no-op.
func (*Sink) Close() error {
	return nil
}
