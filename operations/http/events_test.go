package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v14/operations"
	operationsmock "github.com/primandproper/platform-go/v14/operations/mock"

	"github.com/primandproper/primitives-go/encoding"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/eventstream"
	"github.com/primandproper/primitives-go/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingStream is an eventstream.EventStream a test drives directly: it
// records what was sent, can be told to fail a send, and can be finished from
// the outside the way a departing client finishes a real one.
type recordingStream struct {
	done     chan struct{}
	sendErr  error
	sent     []eventstream.Event
	mu       sync.Mutex
	doneOnce sync.Once
}

func newRecordingStream() *recordingStream {
	return &recordingStream{done: make(chan struct{})}
}

func (s *recordingStream) Send(_ context.Context, event *eventstream.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sendErr != nil {
		return s.sendErr
	}

	s.sent = append(s.sent, *event)

	return nil
}

func (s *recordingStream) Done() <-chan struct{} { return s.done }

func (s *recordingStream) Close() error {
	s.finish()

	return nil
}

func (s *recordingStream) finish() { s.doneOnce.Do(func() { close(s.done) }) }

func (s *recordingStream) events() []eventstream.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]eventstream.Event(nil), s.sent...)
}

// failingCodec refuses to encode anything, which is how the paths that report an
// encoding failure to a client are reached.
type failingCodec struct {
	encoding.Codec
	err error
}

func (c failingCodec) Marshal(context.Context, any) ([]byte, error) { return nil, c.err }

func (failingCodec) ContentType() string { return "application/json" }

// testHandlers builds handlers without mounting them, for the unexported
// methods that take their collaborators as arguments.
func testHandlers(t *testing.T, opts ...Option) *Handlers {
	t.Helper()

	h, err := New(&operationsmock.ServiceMock{}, append([]Option{WithOwnerResolver(Unscoped)}, opts...)...)
	must.NoError(t, err)

	return h
}

func TestHandlers_pump(T *testing.T) {
	T.Parallel()

	begin := func(t *testing.T, h *Handlers) (context.Context, observability.Operation) {
		t.Helper()

		ctx, span := h.o11y.Begin(t.Context())
		t.Cleanup(span.End)

		return ctx, span
	}

	T.Run("forwards every snapshot and ends when the channel closes", func(t *testing.T) {
		t.Parallel()

		// The channel closes on the terminal snapshot, which is why there is no
		// "am I finished" check anywhere in the loop.
		h := testHandlers(t)
		ctx, span := begin(t, h)

		snapshots := make(chan *operations.Operation, 2)
		snapshots <- &operations.Operation{ID: "op1", State: operations.StateRunning}
		snapshots <- &operations.Operation{ID: "op1", State: operations.StateSucceeded}
		close(snapshots)

		stream := newRecordingStream()

		h.pump(ctx, span, stream, snapshots)

		sent := stream.events()
		must.SliceLen(t, 2, sent)
		test.EqOp(t, EventOperation, sent[0].Type)
		test.EqOp(t, EventOperation, sent[1].Type)
	})

	T.Run("stops when the request context is done", func(t *testing.T) {
		t.Parallel()

		// A client that disconnects cancels the request context, which retires
		// the subscription. Returning here rather than blocking is what keeps a
		// departed client from holding a goroutine.
		h := testHandlers(t)
		_, span := begin(t, h)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		h.pump(ctx, span, newRecordingStream(), make(chan *operations.Operation))
	})

	T.Run("stops when the stream itself is done", func(t *testing.T) {
		t.Parallel()

		h := testHandlers(t)
		ctx, span := begin(t, h)

		stream := newRecordingStream()
		stream.finish()

		h.pump(ctx, span, stream, make(chan *operations.Operation))

		test.SliceEmpty(t, stream.events())
	})

	T.Run("tells the client when a snapshot cannot be encoded", func(t *testing.T) {
		t.Parallel()

		// Reported rather than only logged: a stream that simply stopped would
		// look to a client exactly like an operation that is taking a while.
		h := testHandlers(t)
		h.codec = failingCodec{err: platformerrors.New("cannot encode")}

		ctx, span := begin(t, h)

		snapshots := make(chan *operations.Operation, 1)
		snapshots <- &operations.Operation{ID: "op1"}

		stream := newRecordingStream()

		h.pump(ctx, span, stream, snapshots)

		// The error frame itself could not be encoded either, so nothing was
		// sent — but the loop returned rather than spinning on the failure.
		test.SliceEmpty(t, stream.events())
	})

	T.Run("emits an error frame when only the snapshot fails to encode", func(t *testing.T) {
		t.Parallel()

		h := testHandlers(t)
		h.codec = codecFailingOn[*operations.Operation]{Codec: h.codec}

		ctx, span := begin(t, h)

		snapshots := make(chan *operations.Operation, 1)
		snapshots <- &operations.Operation{ID: "op1"}

		stream := newRecordingStream()

		h.pump(ctx, span, stream, snapshots)

		sent := stream.events()
		must.SliceLen(t, 1, sent)
		test.EqOp(t, EventError, sent[0].Type)
		test.StrContains(t, string(sent[0].Payload), "could not encode the operation")
	})

	T.Run("stops when the client has gone", func(t *testing.T) {
		t.Parallel()

		// Neither a broken connection nor a departed client is this process's
		// fault, and the operation carries on regardless — which is the whole
		// point of it being durable.
		h := testHandlers(t)
		ctx, span := begin(t, h)

		snapshots := make(chan *operations.Operation, 2)
		snapshots <- &operations.Operation{ID: "op1"}
		snapshots <- &operations.Operation{ID: "op1"}

		stream := newRecordingStream()
		stream.sendErr = platformerrors.New("connection reset")

		h.pump(ctx, span, stream, snapshots)

		test.SliceEmpty(t, stream.events())
	})
}

// codecFailingOn encodes everything except values of type T, so a test can fail
// the snapshot while leaving the error frame encodable.
type codecFailingOn[T any] struct {
	encoding.Codec
}

func (c codecFailingOn[T]) Marshal(ctx context.Context, v any) ([]byte, error) {
	if _, ok := v.(T); ok {
		return nil, platformerrors.New("cannot encode this one")
	}

	return c.Codec.Marshal(ctx, v)
}

func TestHandlers_writeRefusal(T *testing.T) {
	T.Parallel()

	T.Run("writes the body under the content type the codec names", func(t *testing.T) {
		t.Parallel()

		// Read off the codec rather than written out, so the header cannot claim
		// one encoding while the body is in another.
		h := testHandlers(t)

		_, span := h.o11y.Begin(t.Context())
		defer span.End()

		res := httptest.NewRecorder()
		h.writeRefusal(t.Context(), res, span, nethttp.StatusTooManyRequests, map[string]string{"message": "slow down"})

		test.EqOp(t, nethttp.StatusTooManyRequests, res.Code)
		test.EqOp(t, h.codec.ContentType(), res.Header().Get("Content-Type"))
		test.StrContains(t, res.Body.String(), "slow down")
	})

	T.Run("falls back to a plain 500 when the envelope cannot be encoded", func(t *testing.T) {
		t.Parallel()

		h := testHandlers(t)
		h.codec = failingCodec{err: platformerrors.New("cannot encode")}

		_, span := h.o11y.Begin(t.Context())
		defer span.End()

		res := httptest.NewRecorder()
		h.writeRefusal(t.Context(), res, span, nethttp.StatusNotFound, map[string]string{"message": "gone"})

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
	})
}

func TestHandlers_sendError(T *testing.T) {
	T.Parallel()

	T.Run("emits a stream-level failure frame", func(t *testing.T) {
		t.Parallel()

		h := testHandlers(t)

		_, span := h.o11y.Begin(t.Context())
		defer span.End()

		stream := newRecordingStream()

		h.sendError(t.Context(), span, stream, "something went wrong")

		sent := stream.events()
		must.SliceLen(t, 1, sent)
		test.EqOp(t, EventError, sent[0].Type)
		test.StrContains(t, string(sent[0].Payload), "something went wrong")
	})

	T.Run("is best-effort when the connection cannot carry it either", func(t *testing.T) {
		t.Parallel()

		// The connection that could not carry a snapshot may not carry this
		// one, and there is nobody left to tell about that.
		h := testHandlers(t)

		_, span := h.o11y.Begin(t.Context())
		defer span.End()

		stream := newRecordingStream()
		stream.sendErr = platformerrors.New("connection reset")

		h.sendError(t.Context(), span, stream, "something went wrong")

		test.SliceEmpty(t, stream.events())
	})

	T.Run("says nothing when the message itself cannot be encoded", func(t *testing.T) {
		t.Parallel()

		h := testHandlers(t)
		h.codec = failingCodec{err: platformerrors.New("cannot encode")}

		_, span := h.o11y.Begin(t.Context())
		defer span.End()

		stream := newRecordingStream()

		h.sendError(t.Context(), span, stream, "something went wrong")

		test.SliceEmpty(t, stream.events())
	})
}
