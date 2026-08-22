package outbox

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	databasemock "github.com/primandproper/platform-go/v13/database/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingExecutor is a SQLQueryExecutor that answers every statement and
// remembers it. The notify path is one extra statement on the caller's
// executor, so what it emits — and whether it emits anything at all — is
// exactly what there is to assert.
type recordingExecutor struct {
	*databasemock.SQLQueryExecutorMock

	execErr error

	statements []executedStatement
}

type executedStatement struct {
	query string
	args  []any
}

func newRecordingExecutor() *recordingExecutor {
	e := &recordingExecutor{}

	e.SQLQueryExecutorMock = &databasemock.SQLQueryExecutorMock{
		ExecContextFunc: func(_ context.Context, query string, args ...any) (sql.Result, error) {
			e.statements = append(e.statements, executedStatement{query: query, args: args})

			if e.execErr != nil {
				return nil, e.execErr
			}

			return driver.RowsAffected(1), nil
		},
	}

	return e
}

// notifies returns the notify statements that were run, which is the whole of
// what this path adds.
func (e *recordingExecutor) notifies() []executedStatement {
	var out []executedStatement

	for i := range e.statements {
		if strings.Contains(e.statements[i].query, "pg_notify") {
			out = append(out, e.statements[i])
		}
	}

	return out
}

func TestNewWriter_notifyChannel(T *testing.T) {
	T.Parallel()

	T.Run("accepts a channel on postgres", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(dialect.Postgres, WithWriterNotifyChannel("outbox"))
		must.NoError(t, err)
		test.EqOp(t, "outbox", w.notifyChannel)
	})

	// Refused, not ignored. A MySQL deployment that set a channel believes it
	// has wakeups, and silently running on the poll interval is the failure
	// this module's constructors exist to prevent.
	T.Run("rejects a channel on a dialect without NOTIFY", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
			_, err := NewWriter(d, WithWriterNotifyChannel("outbox"))

			test.ErrorIs(t, err, ErrNotifyUnsupported, test.Sprintf("dialect %q", d))
			test.ErrorIs(t, err, dialect.ErrUnsupported, test.Sprintf("dialect %q", d))
		}
	})

	// The name is bound as text here, but the listener has to render it into a
	// LISTEN, which takes no parameters — so it is vetted on both sides.
	T.Run("rejects a channel that is not an identifier", func(t *testing.T) {
		t.Parallel()

		for _, channel := range []string{"outbox; DROP TABLE users", "out box", "1outbox"} {
			_, err := NewWriter(dialect.Postgres, WithWriterNotifyChannel(channel))
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier, test.Sprintf("channel %q", channel))
		}
	})

	T.Run("no channel is the default on every dialect", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
			w, err := NewWriter(d)
			must.NoError(t, err, must.Sprintf("dialect %q", d))
			test.EqOp(t, "", w.notifyChannel)
		}
	})
}

func TestRelayConfig_wakeFields(T *testing.T) {
	T.Parallel()

	T.Run("EnsureDefaults fills the wake floor and leaves the channel empty", func(t *testing.T) {
		t.Parallel()

		cfg := &RelayConfig{ClaimMode: ClaimLease}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultMinWakeInterval, cfg.MinWakeInterval)
		test.EqOp(t, "", cfg.NotifyChannel)
	})

	T.Run("EnsureDefaults leaves a set wake floor alone", func(t *testing.T) {
		t.Parallel()

		cfg := &RelayConfig{ClaimMode: ClaimLease, MinWakeInterval: time.Second}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Second, cfg.MinWakeInterval)
	})

	T.Run("rejects a wake floor below a millisecond", func(t *testing.T) {
		t.Parallel()

		cfg := &RelayConfig{ClaimMode: ClaimLease}
		cfg.EnsureDefaults()
		cfg.MinWakeInterval = time.Microsecond

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestWriter_Enqueue_notify(T *testing.T) {
	T.Parallel()

	msg := Message{Topic: "orders", Payload: map[string]any{"id": "a"}}

	// The default has to be exactly the SQL that ran before this feature
	// existed: turning it on changes what runs inside every caller's
	// transaction, which is why it is opt-in.
	T.Run("emits nothing when no channel is configured", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(dialect.Postgres)
		must.NoError(t, err)

		exec := newRecordingExecutor()

		must.NoError(t, w.Enqueue(t.Context(), exec, msg))

		test.SliceLen(t, 1, exec.statements)
		test.SliceEmpty(t, exec.notifies())
	})

	T.Run("emits one payload-free notification on the caller's executor", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(dialect.Postgres, WithWriterNotifyChannel("outbox"))
		must.NoError(t, err)

		exec := newRecordingExecutor()

		must.NoError(t, w.Enqueue(t.Context(), exec, msg, msg, msg))

		// One insert and one notification, however many messages: the
		// notification says "there is work", not what the work is.
		test.SliceLen(t, 2, exec.statements)

		notifies := exec.notifies()
		must.SliceLen(t, 1, notifies)
		test.Eq(t, []any{"outbox"}, notifies[0].args)

		// Bound, never interpolated — and the payload is empty, which is what
		// lets Postgres collapse a transaction's notifications into one.
		test.StrContains(t, notifies[0].query, "$1")
		test.StrNotContains(t, notifies[0].query, "outbox")
	})

	// The notification rides the caller's transaction, so a failure has already
	// aborted it. There is no "carry on without the wakeup" branch to take.
	T.Run("reports a failed notification", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(dialect.Postgres, WithWriterNotifyChannel("outbox"))
		must.NoError(t, err)

		exec := newRecordingExecutor()
		exec.execErr = platformerrors.New("connection reset")

		test.Error(t, w.Enqueue(t.Context(), exec, msg))
	})

	T.Run("emits nothing when there is nothing to enqueue", func(t *testing.T) {
		t.Parallel()

		w, err := NewWriter(dialect.Postgres, WithWriterNotifyChannel("outbox"))
		must.NoError(t, err)

		exec := newRecordingExecutor()

		must.NoError(t, w.Enqueue(t.Context(), exec))

		test.SliceEmpty(t, exec.statements)
	})
}

// newWakeRelay builds a relay whose only observable act is the claim
// transaction each cycle opens, counted here. It never touches a database: the
// wake path is scheduling, and a container would only add flake to a test about
// timing.
func newWakeRelay(t *testing.T, wakeup <-chan struct{}, mutate func(*RelayConfig)) (*Relay, *atomic.Int64) {
	t.Helper()

	var cycles atomic.Int64

	client := &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return dialect.Postgres },
		WithTransactionFunc: func(context.Context, func(database.SQLQueryExecutor) error) error {
			cycles.Add(1)

			return nil
		},
	}

	// Both tickers are set out of reach, so a cycle that happens is a cycle the
	// wakeup caused.
	cfg := &RelayConfig{
		ClaimMode:       ClaimLease,
		PollInterval:    time.Hour,
		ReapInterval:    time.Hour,
		MinWakeInterval: 100 * time.Millisecond,
	}
	if mutate != nil {
		mutate(cfg)
	}

	r, err := NewRelay(t.Context(), cfg, client, &messagequeuemock.PublisherProviderMock{
		CloseFunc: func() {},
	}, WithRelayClock(clock.NewClock()), WithRelayWakeup(wakeup))
	must.NoError(t, err)

	return r, &cycles
}

// eventually polls until cond holds or the deadline passes.
func eventually(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}

func TestRelay_wakeup(T *testing.T) {
	T.Parallel()

	T.Run("WithRelayWakeup sets the channel", func(t *testing.T) {
		t.Parallel()

		wakeup := make(chan struct{})

		r := &Relay{}
		WithRelayWakeup(wakeup)(r)

		test.NotNil(t, r.wakeup)
	})

	T.Run("a wake runs a cycle without waiting for the poll", func(t *testing.T) {
		t.Parallel()

		wakeup := make(chan struct{}, 1)
		r, cycles := newWakeRelay(t, wakeup, nil)

		go r.Run()

		wakeup <- struct{}{}

		eventually(t, 5*time.Second, func() bool { return cycles.Load() >= 1 }, "a wake-driven cycle")

		must.NoError(t, r.Close(t.Context()))
	})

	// Without a floor, a table taking thousands of inserts a second drives
	// thousands of claim transactions a second — more queries under load than
	// polling, which is the opposite of the point.
	T.Run("a burst of wakes does not become a burst of cycles", func(t *testing.T) {
		t.Parallel()

		const (
			floor     = 100 * time.Millisecond
			burstFor  = 600 * time.Millisecond
			maxCycles = 12
		)

		wakeup := make(chan struct{})
		r, cycles := newWakeRelay(t, wakeup, func(cfg *RelayConfig) { cfg.MinWakeInterval = floor })

		go r.Run()

		// Blocking sends, which is the worst case: a channel that does not
		// coalesce, delivering every single wake. The floor has to hold anyway.
		var sent int64

		deadline := time.Now().Add(burstFor)
		for time.Now().Before(deadline) {
			wakeup <- struct{}{}
			sent++
		}

		observed := cycles.Load()

		must.NoError(t, r.Close(t.Context()))

		// The burst has to have been a burst, or the assertion below proves
		// nothing.
		must.GreaterEq(t, int64(100), sent)

		test.LessEq(t, int64(maxCycles), observed,
			test.Sprintf("%d wakes over %s produced %d cycles", sent, burstFor, observed))
		test.GreaterEq(t, int64(1), observed)
	})

	// Deferred, not dropped: the last enqueue of a burst is the one most likely
	// to be the reason somebody is watching, and it must not wait out the poll
	// interval.
	T.Run("a wake arriving inside the floor is served after it", func(t *testing.T) {
		t.Parallel()

		wakeup := make(chan struct{})
		r, cycles := newWakeRelay(t, wakeup, func(cfg *RelayConfig) {
			cfg.MinWakeInterval = 100 * time.Millisecond
		})

		go r.Run()

		// Two wakes back to back: the first cycles immediately, the second
		// lands inside the floor and has to be served when it elapses.
		wakeup <- struct{}{}
		wakeup <- struct{}{}

		eventually(t, 5*time.Second, func() bool { return cycles.Load() >= 2 }, "the deferred cycle")

		must.NoError(t, r.Close(t.Context()))
	})

	// The poll ticker is unchanged and the loop is otherwise as it was, so a
	// relay nobody wired a wakeup to cycles only when it always did.
	T.Run("a relay without a wakeup cycles only on its ticker", func(t *testing.T) {
		t.Parallel()

		r, cycles := newWakeRelay(t, nil, nil)

		test.Nil(t, r.wakeup)

		go r.Run()

		time.Sleep(500 * time.Millisecond)

		test.EqOp(t, int64(0), cycles.Load())

		must.NoError(t, r.Close(t.Context()))

		// Close still drains once, which is the behavior it always had.
		test.EqOp(t, int64(1), cycles.Load())
	})
}
