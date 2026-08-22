package grpc

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

var (
	// ErrUnknownMessageType indicates a recorded reply names a message type
	// this binary cannot find in the global proto registry, so the reply
	// cannot be rebuilt.
	ErrUnknownMessageType = platformerrors.New("unknown recorded message type")
	// ErrNotProtoMessage indicates a request or reply that is not a
	// proto.Message. Such a call cannot be fingerprinted or recorded and is
	// passed through untouched.
	ErrNotProtoMessage = platformerrors.New("not a proto message")
)

// Response is the recorded half of a unary RPC.
//
// A reply is stored as its marshaled bytes plus its type name rather than as
// the message itself: the store serializes with gob, which cannot round-trip a
// proto message faithfully, while proto.Marshal can.
type Response struct {
	// MessageName is the reply's fully-qualified proto name, used to rebuild
	// it on replay.
	MessageName string
	// StatusMessage is the error message, empty on success.
	StatusMessage string
	// Payload is the marshaled reply, empty for an error result or when
	// Truncated.
	Payload []byte
	// StatusCode is the gRPC status code the call produced.
	StatusCode uint32
	// Truncated reports that the reply outgrew the configured cap and its
	// bytes were dropped. The call is still recorded, so the effect does not
	// repeat, but the reply can no longer be reproduced.
	Truncated bool
}

// record captures a handler's outcome.
//
// A reply that is not a proto.Message yields ErrNotProtoMessage, which the
// caller turns into a pass-through rather than a failure: grpc-go permits
// non-proto codecs, and a service using one should be left alone rather than
// broken.
func record(reply any, err error, maxBytes int) (*Response, error) {
	if err != nil {
		st, _ := status.FromError(err)

		return &Response{
			StatusCode:    uint32(st.Code()),
			StatusMessage: st.Message(),
		}, nil
	}

	message, ok := reply.(proto.Message)
	if !ok {
		return nil, ErrNotProtoMessage
	}

	// Deterministic marshaling is required, not merely nice: map fields
	// otherwise serialize in a random order, so a replay would produce
	// different bytes than the original for the same message.
	payload, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if marshalErr != nil {
		return nil, marshalErr
	}

	out := &Response{
		MessageName: string(message.ProtoReflect().Descriptor().FullName()),
		StatusCode:  uint32(codes.OK),
	}

	if maxBytes > 0 && len(payload) > maxBytes {
		out.Truncated = true

		return out, nil
	}

	out.Payload = payload

	return out, nil
}

// replay rebuilds a recorded outcome.
//
// A recorded error comes back as the same status. A recorded reply is rebuilt
// from the global proto registry, where every protoc-gen-go type registers
// itself at init.
func replay(recorded *Response) (any, error) {
	if code := codes.Code(recorded.StatusCode); code != codes.OK {
		return nil, status.Error(code, recorded.StatusMessage)
	}

	if recorded.Truncated {
		// The call is known to have succeeded, so re-running it is not an
		// option — that is the duplicate this package exists to prevent. The
		// honest answer is that the reply is gone, which is also the signal to
		// raise WithMaxResponseBytes.
		return nil, status.Error(
			codes.ResourceExhausted,
			"the original response exceeded the idempotency replay size limit and cannot be replayed",
		)
	}

	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(recorded.MessageName))
	if err != nil {
		return nil, platformerrors.Wrapf(ErrUnknownMessageType, "message %q", recorded.MessageName)
	}

	message := messageType.New().Interface()
	if err = proto.Unmarshal(recorded.Payload, message); err != nil {
		return nil, platformerrors.Wrapf(err, "rebuilding recorded %q", recorded.MessageName)
	}

	return message, nil
}
