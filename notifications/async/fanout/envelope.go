package fanout

import (
	"github.com/primandproper/platform-go/v10/notifications/async"
)

// envelope is what one Publish puts on the wire, and the only thing this
// package's consumer expects to find there.
//
// The channel travels with the event because the backplane uses one topic for
// every channel: the consumer needs to know which of its local channels an
// event belongs to, and the topic no longer says.
//
// The event is embedded as an *async.Event rather than flattened into type and
// data fields, so a field added to async.Event crosses the backplane without
// anyone remembering to widen this struct.
//
// Every replica of a service runs the same build, so this format is a private
// detail rather than a compatibility surface — with one exception worth stating:
// during a rolling deploy, replicas on either side of the roll share the topic.
// Adding a field is therefore safe (old replicas ignore it); changing the
// meaning of an existing one is not.
type envelope struct {
	Event   *async.Event `json:"event"`
	Channel string       `json:"channel"`
}
