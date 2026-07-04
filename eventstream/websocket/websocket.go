package websocket

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/eventstream"
	"github.com/primandproper/platform-go/v3/observability"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/tracing"

	gorillawebsocket "github.com/gorilla/websocket"
)

var (
	_ eventstream.EventStreamUpgrader              = (*Upgrader)(nil)
	_ eventstream.BidirectionalEventStreamUpgrader = (*Upgrader)(nil)
	_ eventstream.EventStream                      = (*wsStream)(nil)
	_ eventstream.BidirectionalEventStream         = (*bidirectionalWSStream)(nil)
)

const (
	name = "websocket_stream"

	defaultHeartbeatInterval = 30 * time.Second
	defaultBufferSize        = 1024
	incomingChannelBuffer    = 64
	writeWait                = 10 * time.Second
)

// Upgrader upgrades HTTP connections to WebSocket event streams.
type Upgrader struct {
	o11y              observability.Observer
	wsUpgrader        gorillawebsocket.Upgrader
	heartbeatInterval time.Duration
}

// NewUpgrader creates a new WebSocket Upgrader.
func NewUpgrader(logger logging.Logger, tracerProvider tracing.TracerProvider, cfg *Config) *Upgrader {
	heartbeat := defaultHeartbeatInterval
	readBuf := defaultBufferSize
	writeBuf := defaultBufferSize

	var allowedOrigins []string

	if cfg != nil {
		if cfg.HeartbeatInterval > 0 {
			heartbeat = cfg.HeartbeatInterval
		}
		if cfg.ReadBufferSize > 0 {
			readBuf = cfg.ReadBufferSize
		}
		if cfg.WriteBufferSize > 0 {
			writeBuf = cfg.WriteBufferSize
		}
		allowedOrigins = cfg.AllowedOrigins
	}

	return &Upgrader{
		o11y: observability.NewObserver(name, logger, tracerProvider),
		wsUpgrader: gorillawebsocket.Upgrader{
			ReadBufferSize:  readBuf,
			WriteBufferSize: writeBuf,
			CheckOrigin:     originChecker(allowedOrigins),
		},
		heartbeatInterval: heartbeat,
	}
}

// originChecker builds a CheckOrigin function. With no allowed origins configured
// it returns nil, so gorilla applies its default same-origin policy; otherwise it
// permits only the exact Origin header values in the allowlist (plus originless,
// non-browser clients).
func originChecker(allowedOrigins []string) func(*http.Request) bool {
	if len(allowedOrigins) == 0 {
		return nil
	}

	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = struct{}{}
	}

	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		_, ok := allowed[origin]
		return ok
	}
}

// UpgradeToEventStream upgrades an HTTP connection to a unidirectional WebSocket event stream.
func (u *Upgrader) UpgradeToEventStream(w http.ResponseWriter, r *http.Request) (eventstream.EventStream, error) {
	conn, err := u.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		u.o11y.Logger().Error("upgrading to websocket", err)
		return nil, errors.Wrap(err, "upgrading to websocket")
	}

	return newWSStream(conn, u.heartbeatInterval, u.o11y), nil
}

// UpgradeToBidirectionalStream upgrades an HTTP connection to a bidirectional WebSocket event stream.
func (u *Upgrader) UpgradeToBidirectionalStream(w http.ResponseWriter, r *http.Request) (eventstream.BidirectionalEventStream, error) {
	conn, err := u.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		u.o11y.Logger().Error("upgrading to bidirectional websocket", err)
		return nil, errors.Wrap(err, "upgrading to websocket")
	}

	return newBidirectionalWSStream(conn, u.heartbeatInterval, u.o11y), nil
}

// wsStream is a unidirectional (send-only) WebSocket event stream.
type wsStream struct {
	o11y              observability.Observer
	conn              *gorillawebsocket.Conn
	heartbeatInterval *time.Ticker
	done              chan struct{}
	pongWait          time.Duration
	mu                sync.Mutex
	closed            bool
}

func newWSStream(conn *gorillawebsocket.Conn, heartbeat time.Duration, o11y observability.Observer) *wsStream {
	s := newWSStreamBase(conn, heartbeat, o11y)

	// A send-only stream must still read to process control frames (pong/close)
	// and to notice when the client goes away.
	go s.readPump()

	return s
}

// newWSStreamBase builds a wsStream and starts its heartbeat, but leaves reading
// to the caller: the unidirectional stream runs readPump, while the bidirectional
// stream reads in its own readLoop.
func newWSStreamBase(conn *gorillawebsocket.Conn, heartbeat time.Duration, o11y observability.Observer) *wsStream {
	s := &wsStream{
		conn: conn,
		done: make(chan struct{}),
		o11y: o11y,
	}

	if heartbeat > 0 {
		s.heartbeatInterval = time.NewTicker(heartbeat)
		s.pongWait = heartbeat + heartbeat/2
		_ = s.conn.SetReadDeadline(time.Now().Add(s.pongWait)) //nolint:errcheck // best-effort; a dead conn surfaces on the next read
		s.conn.SetPongHandler(func(string) error {
			return s.conn.SetReadDeadline(time.Now().Add(s.pongWait))
		})
		go s.heartbeatLoop()
	}

	return s
}

// readPump drains inbound frames so gorilla can process control frames, and
// closes the stream when the client disconnects or the read deadline lapses.
func (s *wsStream) readPump() {
	for {
		if _, _, err := s.conn.ReadMessage(); err != nil {
			if gorillawebsocket.IsUnexpectedCloseError(err, gorillawebsocket.CloseNormalClosure, gorillawebsocket.CloseGoingAway) {
				s.o11y.Logger().Error("reading from websocket", err)
			}
			_ = s.Close() //nolint:errcheck // cleanup on an already-failing connection
			return
		}
	}
}

func (s *wsStream) heartbeatLoop() {
	for {
		select {
		case <-s.done:
			return
		case <-s.heartbeatInterval.C:
			s.mu.Lock()
			_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait)) //nolint:errcheck // best-effort; the WriteMessage below surfaces a dead conn
			err := s.conn.WriteMessage(gorillawebsocket.PingMessage, nil)
			s.mu.Unlock()
			if err != nil {
				s.o11y.Logger().Error("sending heartbeat ping", err)
				// The connection is dead. Close the stream so `done` fires and the
				// manager deregisters it, instead of leaving a broken stream registered.
				_ = s.Close() //nolint:errcheck // cleanup on an already-failing connection
				return
			}
		}
	}
}

// Send writes an event to the WebSocket connection as JSON.
func (s *wsStream) Send(ctx context.Context, event *eventstream.Event) error {
	_, op := s.o11y.BeginCustom(ctx, "ws_send")
	defer op.End()

	op.Set("event.type", event.Type)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("stream closed")
	}

	_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait)) //nolint:errcheck // best-effort; the WriteJSON below surfaces a dead conn

	if err := s.conn.WriteJSON(event); err != nil {
		return op.Error(err, "writing event to websocket")
	}

	return nil
}

// Done returns a channel that closes when the stream terminates.
func (s *wsStream) Done() <-chan struct{} {
	return s.done
}

// Close terminates the WebSocket stream.
func (s *wsStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	close(s.done)

	if s.heartbeatInterval != nil {
		s.heartbeatInterval.Stop()
	}

	return s.conn.Close()
}

// bidirectionalWSStream extends wsStream with receive capability.
type bidirectionalWSStream struct {
	*wsStream
	incoming chan *eventstream.Event
}

func newBidirectionalWSStream(conn *gorillawebsocket.Conn, heartbeat time.Duration, o11y observability.Observer) *bidirectionalWSStream {
	s := &bidirectionalWSStream{
		wsStream: newWSStreamBase(conn, heartbeat, o11y),
		incoming: make(chan *eventstream.Event, incomingChannelBuffer),
	}

	go s.readLoop()

	return s
}

// readLoop is the bidirectional stream's reader: it delivers inbound events, keeps
// the read deadline fresh, and closes the stream when the client disconnects.
func (s *bidirectionalWSStream) readLoop() {
	defer close(s.incoming)
	defer func() { _ = s.Close() }() //nolint:errcheck // cleanup on stream teardown

	for {
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			return
		}

		if s.pongWait > 0 {
			_ = s.conn.SetReadDeadline(time.Now().Add(s.pongWait)) //nolint:errcheck // best-effort; the next ReadMessage surfaces a dead conn
		}

		var event eventstream.Event
		if err = json.Unmarshal(msg, &event); err != nil {
			continue
		}

		select {
		case s.incoming <- &event:
		case <-s.done:
			return
		}
	}
}

// Receive returns a channel of inbound events from the client.
func (s *bidirectionalWSStream) Receive() <-chan *eventstream.Event {
	return s.incoming
}
