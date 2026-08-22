package pgnotify

import (
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// unreachableDSN points at a port nothing listens on, so a connect attempt
// fails immediately instead of waiting out a timeout. Every test here that
// starts a listener wants the failure path, not a database.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"

func validConfig() *Config {
	return &Config{ConnectionString: unreachableDSN, Channel: "outbox"}
}

// newTestListener builds a listener that has been constructed but never run, so
// its channel and counters are live and nothing has dialed.
func newTestListener(t *testing.T, mutate func(*Config)) *Listener {
	t.Helper()

	cfg := validConfig()
	if mutate != nil {
		mutate(cfg)
	}

	l, err := NewListener(t.Context(), cfg)
	must.NoError(t, err)

	return l
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills unset knobs", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultMinReconnectBackoff, cfg.MinReconnectBackoff)
		test.EqOp(t, DefaultMaxReconnectBackoff, cfg.MaxReconnectBackoff)
	})

	T.Run("leaves set knobs alone", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.MinReconnectBackoff = time.Second
		cfg.MaxReconnectBackoff = time.Minute
		cfg.EnsureDefaults()

		test.EqOp(t, time.Second, cfg.MinReconnectBackoff)
		test.EqOp(t, time.Minute, cfg.MaxReconnectBackoff)
	})

	// A ceiling below the floor is a config that would make the backoff shrink
	// as it escalated, so the ceiling gives way rather than the floor.
	T.Run("raises a ceiling that sits below the floor", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.MinReconnectBackoff = time.Minute
		cfg.MaxReconnectBackoff = time.Second
		cfg.EnsureDefaults()

		test.EqOp(t, time.Minute, cfg.MaxReconnectBackoff)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a defaulted config", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a missing connection string", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Channel: "outbox"}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a missing channel", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{ConnectionString: unreachableDSN}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an over-long channel", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{ConnectionString: unreachableDSN, Channel: strings.Repeat("a", MaxChannelLength+1)}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewListener(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewListener(t.Context(), nil)
		test.ErrorIs(t, err, ErrNilConfig)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	// The channel is rendered into a LISTEN, which cannot bind parameters, so
	// anything that is not an identifier has to be refused here.
	T.Run("rejects a channel that is not an identifier", func(t *testing.T) {
		t.Parallel()

		for _, channel := range []string{"outbox; DROP TABLE users", "out box", "1outbox", "naïve"} {
			_, err := NewListener(t.Context(), &Config{ConnectionString: unreachableDSN, Channel: channel})
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier, test.Sprintf("channel %q", channel))
		}
	})

	// A name the server would truncate is one that may no longer be the name
	// the producer is notifying, so it fails loudly rather than half-working.
	T.Run("rejects an over-long channel", func(t *testing.T) {
		t.Parallel()

		_, err := NewListener(t.Context(), &Config{
			ConnectionString: unreachableDSN,
			Channel:          strings.Repeat("a", MaxChannelLength+1),
		})
		test.Error(t, err)
	})

	// Quoting is a correctness requirement, not decoration: pg_notify compares
	// its channel as text, and an unquoted identifier would be down-cased.
	T.Run("renders a quoted LISTEN", func(t *testing.T) {
		t.Parallel()

		l := newTestListener(t, func(cfg *Config) { cfg.Channel = "Outbox" })

		test.EqOp(t, `LISTEN "Outbox"`, l.listen)
	})

	T.Run("accepts observability options and ignores nil ones", func(t *testing.T) {
		t.Parallel()

		l, err := NewListener(t.Context(), validConfig(),
			WithLogger(logging.EnsureLogger(nil)),
			WithTracerProvider(nil),
			WithMetricsProvider(metrics.EnsureMetricsProvider(nil)),
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("defaults the config it was handed", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()

		_, err := NewListener(t.Context(), cfg)
		must.NoError(t, err)

		test.EqOp(t, DefaultMinReconnectBackoff, cfg.MinReconnectBackoff)
	})
}

func TestListener_Signal(T *testing.T) {
	T.Parallel()

	T.Run("coalesces a burst into one pending wake", func(t *testing.T) {
		t.Parallel()

		l := newTestListener(t, nil)

		for range 1000 {
			l.wake(t.Context())
		}

		// One wake is pending, and only one: the consumer re-reads the table
		// when it wakes, so a thousand notifications and one are the same
		// instruction.
		<-l.Signal()

		select {
		case <-l.Signal():
			t.Fatal("a second wake was pending after a coalesced burst")
		default:
		}
	})

	T.Run("a wake is available again after it is read", func(t *testing.T) {
		t.Parallel()

		l := newTestListener(t, nil)

		for range 3 {
			l.wake(t.Context())
			<-l.Signal()
		}
	})
}

func TestListener_Close(T *testing.T) {
	T.Parallel()

	// Close before Run must not park on a done channel nothing will ever close.
	T.Run("returns immediately when the listener was never run", func(t *testing.T) {
		t.Parallel()

		l := newTestListener(t, nil)

		must.NoError(t, l.Close(t.Context()))
		must.NoError(t, l.Close(t.Context()))
	})

	T.Run("stops a running listener", func(t *testing.T) {
		t.Parallel()

		l := newTestListener(t, nil)

		stopped := make(chan struct{})

		go func() {
			l.Run()
			close(stopped)
		}()

		// The connect fails — nothing is listening on that port — so the
		// listener is in its reconnect backoff, which is where Close has to be
		// able to interrupt it.
		must.NoError(t, l.Close(t.Context()))

		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after Close")
		}

		must.NoError(t, l.Close(t.Context()))
	})

	// A connect that never succeeds still fires no wake: the catch-up signal is
	// tied to an established session, not to an attempt.
	T.Run("an unreachable server produces no wake", func(t *testing.T) {
		t.Parallel()

		l := newTestListener(t, nil)

		go l.Run()

		time.Sleep(250 * time.Millisecond)

		select {
		case <-l.Signal():
			t.Fatal("a wake was delivered without an established session")
		default:
		}

		must.NoError(t, l.Close(t.Context()))
	})
}
