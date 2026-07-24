package noop

import (
	"context"
	"net/http"
	"sync"

	"github.com/primandproper/platform-go/v6/eventstream"
)

var (
	_ eventstream.EventStream                      = (*EventStream)(nil)
	_ eventstream.BidirectionalEventStream         = (*BidirectionalEventStream)(nil)
	_ eventstream.EventStreamUpgrader              = (*EventStreamUpgrader)(nil)
	_ eventstream.BidirectionalEventStreamUpgrader = (*BidirectionalEventStreamUpgrader)(nil)
)

// EventStream is a no-op EventStream.
type EventStream struct {
	done chan struct{}
	once sync.Once
}

// NewEventStream returns a no-op EventStream.
func NewEventStream() eventstream.EventStream {
	return &EventStream{
		done: make(chan struct{}),
	}
}

// Send is a no-op.
func (*EventStream) Send(context.Context, *eventstream.Event) error {
	return nil
}

// Done returns a channel that closes when Close is called.
func (s *EventStream) Done() <-chan struct{} {
	return s.done
}

// Close closes the done channel.
func (s *EventStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// BidirectionalEventStream is a no-op BidirectionalEventStream.
type BidirectionalEventStream struct {
	receive chan *eventstream.Event
	EventStream
}

// NewBidirectionalEventStream returns a no-op BidirectionalEventStream.
func NewBidirectionalEventStream() eventstream.BidirectionalEventStream {
	return &BidirectionalEventStream{
		EventStream: EventStream{
			done: make(chan struct{}),
		},
		receive: make(chan *eventstream.Event),
	}
}

// Receive returns a channel that never delivers events.
func (s *BidirectionalEventStream) Receive() <-chan *eventstream.Event {
	return s.receive
}

// EventStreamUpgrader is a no-op EventStreamUpgrader.
type EventStreamUpgrader struct{}

// NewEventStreamUpgrader returns a no-op EventStreamUpgrader.
func NewEventStreamUpgrader() eventstream.EventStreamUpgrader {
	return &EventStreamUpgrader{}
}

// UpgradeToEventStream returns a no-op EventStream.
func (*EventStreamUpgrader) UpgradeToEventStream(http.ResponseWriter, *http.Request) (eventstream.EventStream, error) {
	return NewEventStream(), nil
}

// BidirectionalEventStreamUpgrader is a no-op BidirectionalEventStreamUpgrader.
type BidirectionalEventStreamUpgrader struct{}

// NewBidirectionalEventStreamUpgrader returns a no-op BidirectionalEventStreamUpgrader.
func NewBidirectionalEventStreamUpgrader() eventstream.BidirectionalEventStreamUpgrader {
	return &BidirectionalEventStreamUpgrader{}
}

// UpgradeToBidirectionalStream returns a no-op BidirectionalEventStream.
func (*BidirectionalEventStreamUpgrader) UpgradeToBidirectionalStream(http.ResponseWriter, *http.Request) (eventstream.BidirectionalEventStream, error) {
	return NewBidirectionalEventStream(), nil
}
