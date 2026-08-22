package eventstream

import (
	"context"
	"encoding/json"
	"net/http"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// ErrNoSuchMember is returned by SendToMember when the named member has no
// stream in the named group.
//
// It exists because the alternative was reporting success. A caller sending to a
// member that has disconnected, or that it named wrongly, got a nil error and no
// delivery — and the two cases a caller most wants to tell apart, "delivered"
// and "there was nobody there", were the same answer.
var ErrNoSuchMember = platformerrors.New("no stream for that member")

// Event represents a typed event with a JSON payload.
type Event struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// EventStream is a unidirectional server-to-client event stream.
type EventStream interface {
	// Send pushes an event to the client.
	Send(ctx context.Context, event *Event) error
	// Done returns a channel that closes when the stream terminates.
	Done() <-chan struct{}
	// Close terminates the stream.
	Close() error
}

// BidirectionalEventStream extends EventStream with client-to-server receiving.
type BidirectionalEventStream interface {
	EventStream
	// Receive returns a channel of inbound events from the client.
	Receive() <-chan *Event
}

// EventStreamUpgrader upgrades an HTTP connection to a unidirectional EventStream.
type EventStreamUpgrader interface {
	UpgradeToEventStream(w http.ResponseWriter, r *http.Request) (EventStream, error)
}

// BidirectionalEventStreamUpgrader upgrades an HTTP connection to a BidirectionalEventStream.
type BidirectionalEventStreamUpgrader interface {
	UpgradeToBidirectionalStream(w http.ResponseWriter, r *http.Request) (BidirectionalEventStream, error)
}
