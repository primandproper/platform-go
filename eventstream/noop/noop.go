// Package noop is the eventstream implementation for a caller with no transport
// to stream over. Send accepts and discards; Receive returns a channel nothing
// ever sends on, closed by Close along with Done, so a bare range over it ends
// when the stream does — the same moment the websocket and sse implementations
// close theirs, and the reason a consumer written as a range loop terminates
// here instead of parking a goroutine for the life of the process.
//
// The upgraders are the part to read twice. UpgradeToEventStream and
// UpgradeToBidirectionalStream take the http.ResponseWriter and never touch it:
// no SSE preamble and no WebSocket handshake is written, and the connection is
// never hijacked. The response therefore remains entirely the handler's to
// complete, which is the opposite of what every real upgrader leaves behind. A
// handler written against a real upgrader and run against this one will write a
// body the client reads as an ordinary HTTP reply.
//
// Close closes the Done channel exactly once, so shutdown paths that wait on
// Done still finish, and is idempotent on both channels. eventstream/config
// offers only sse and websocket, so this one is reached by naming it in code.
package noop

import (
	"context"
	"net/http"
	"sync"

	"github.com/primandproper/platform-go/v13/eventstream"
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
	receiveOnce sync.Once
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

// Receive returns a channel that never delivers an event and closes when the
// stream does.
func (s *BidirectionalEventStream) Receive() <-chan *eventstream.Event {
	return s.receive
}

// Close terminates the stream, closing the receive channel as well as Done.
//
// The receive channel is closed because a consumer ranging over Receive has no
// other way out: an open channel with no sender parks that goroutine for the
// life of the process, and the closed one ends the range at the same point the
// websocket and sse streams end theirs. Both closes are guarded, so Close stays
// idempotent.
func (s *BidirectionalEventStream) Close() error {
	s.receiveOnce.Do(func() { close(s.receive) })

	return s.EventStream.Close()
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
