package metering

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newBrokenStore returns a Store whose tables do not exist.
//
// It is how every query in the store gets its error path exercised without a
// double standing in for the database. The alternative — a mock client — would
// assert that the store handles an error it was handed, which is a weaker claim
// than that it handles the error a real driver actually produces; the two differ
// in whether a failure surfaces at Exec, at Query, or at Scan, and this package
// has all three.
func newBrokenStore(t *testing.T) Store {
	t.Helper()

	env := newSQLiteEnv(t)

	// A prefix that is a legal identifier and names nothing. Every statement the
	// store issues then fails at the driver, in the layer that issued it.
	store, err := NewSQLStore(env.client,
		WithTablePrefix("no_such_metering_table"),
		WithStoreLogger(loggingnoop.NewLogger()),
		WithStoreTracerProvider(tracingnoop.NewTracerProvider()),
		WithStoreMetricsProvider(metrics.EnsureMetricsProvider(nil)))
	must.NoError(t, err)

	return store
}

func TestSQLStore_DatabaseFaults(T *testing.T) {
	T.Parallel()

	T.Run("Record reports a failed ledger write", func(t *testing.T) {
		t.Parallel()

		_, err := newBrokenStore(t).Record(t.Context(),
			[]Entry{newEntry("req-1", 1, AggregationSum)}, baseTime)

		test.Error(t, err)
	})

	T.Run("RecordTx reports a failed ledger write", func(t *testing.T) {
		t.Parallel()

		store := newBrokenStore(t)

		test.Error(t, store.WithTransaction(t.Context(), func(q database.SQLQueryExecutor) error {
			_, err := store.RecordTx(t.Context(), q, []Entry{newEntry("req-1", 1, AggregationSum)}, baseTime)

			return err
		}))
	})

	T.Run("Total reports a failed read", func(t *testing.T) {
		t.Parallel()

		// Distinct from the no-rows case, which is not an error: an absent row is
		// a total of zero, and a missing table is a misconfiguration.
		_, err := newBrokenStore(t).Total(t.Context(), testSubject, testMeter, monthBounds)

		test.Error(t, err)
	})

	T.Run("Consume reports a failure opening the total", func(t *testing.T) {
		t.Parallel()

		_, err := newBrokenStore(t).Consume(t.Context(),
			newEntry("req-1", 1, AggregationSum), 100, BehaviorBlock, baseTime)

		test.Error(t, err)
	})

	T.Run("ClaimFlushable reports a failed select", func(t *testing.T) {
		t.Parallel()

		_, err := newBrokenStore(t).ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))

		test.Error(t, err)
	})

	T.Run("MarkFlushed and ReleaseFlush report a failed update", func(t *testing.T) {
		t.Parallel()

		store := newBrokenStore(t)
		total := &Total{Subject: testSubject, Meter: testMeter, PeriodStart: monthBounds.Start}

		test.Error(t, store.MarkFlushed(t.Context(), total, 1, baseTime))
		test.Error(t, store.ReleaseFlush(t.Context(), total, "boom", baseTime))
	})

	T.Run("ReapEvents reports a failed delete", func(t *testing.T) {
		t.Parallel()

		_, err := newBrokenStore(t).ReapEvents(t.Context(), baseTime, 100)

		test.Error(t, err)
	})
}

func TestSQLStore_PartialFaults(T *testing.T) {
	T.Parallel()

	T.Run("Consume reports a failed fold", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		// The zero row and the lock succeed; the fold that follows does not. The
		// three are separate statements inside one transaction, and only breaking
		// the table between them reaches the last one's error path.
		_, err := store.Consume(t.Context(), newEntry("req-1", 1, AggregationSum), 100, BehaviorBlock, baseTime)
		must.NoError(t, err)

		_, err = env.client.Writer().ExecContext(t.Context(), "DROP TABLE "+prefix+"_metering_events")
		must.NoError(t, err)

		_, err = store.Consume(t.Context(), newEntry("req-2", 1, AggregationSum), 100, BehaviorBlock, baseTime)
		test.Error(t, err)
	})

	T.Run("Record reports a failed fold", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		// The ledger write succeeds and the fold into the total does not.
		_, err := env.client.Writer().ExecContext(t.Context(), "DROP TABLE "+prefix+"_metering_totals")
		must.NoError(t, err)

		_, err = store.Record(t.Context(), []Entry{newEntry("req-1", 1, AggregationSum)}, baseTime)
		test.Error(t, err)
	})

	T.Run("ClaimFlushable reports a failed claim", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		// Rename the column the claim writes, so the select finds rows and the
		// update that leases them fails.
		_, err := env.client.Writer().ExecContext(t.Context(),
			"ALTER TABLE "+prefix+"_metering_totals RENAME COLUMN claimed_until TO claimed_until_renamed")
		must.NoError(t, err)

		_, err = store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		test.Error(t, err)
	})

	T.Run("ClaimFlushable reports a failed re-read", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		// Rename a column only the projection reads, so the select and the claim
		// both succeed and the re-read does not. The re-read exists so the
		// attempt counts a flusher sees are the ones the claim just wrote.
		_, err := env.client.Writer().ExecContext(t.Context(),
			"ALTER TABLE "+prefix+"_metering_totals RENAME COLUMN last_error TO last_error_renamed")
		must.NoError(t, err)

		_, err = store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		test.Error(t, err)
	})

	T.Run("Consume reports a failed lock", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		// The zero-row insert succeeds — INSERT OR IGNORE names its columns — and
		// the SELECT that locks the row does not, because the projection asks for
		// a column that is no longer there.
		_, err := env.client.Writer().ExecContext(t.Context(),
			"ALTER TABLE "+prefix+"_metering_totals RENAME COLUMN last_error TO last_error_renamed")
		must.NoError(t, err)

		_, err = store.Consume(t.Context(), newEntry("req-1", 1, AggregationSum), 100, BehaviorBlock, baseTime)
		test.Error(t, err)
	})
}

func TestSQLStore_ScanFaults(T *testing.T) {
	T.Parallel()

	T.Run("reports a total row that does not scan", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		// A timestamp column holding something that is not a timestamp. SQLite is
		// happy to store it; the driver is not happy to scan it into a time.Time,
		// which is the failure a schema drifting under a running binary produces.
		_, err := env.client.Writer().ExecContext(t.Context(),
			"UPDATE "+prefix+"_metering_totals SET period_end = 'not a timestamp'")
		must.NoError(t, err)

		_, err = store.Total(t.Context(), testSubject, testMeter, monthBounds)
		test.Error(t, err)

		_, err = store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		test.Error(t, err)
	})

	T.Run("reports a key projection that does not scan", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		_, err := env.client.Writer().ExecContext(t.Context(),
			"UPDATE "+prefix+"_metering_totals SET period_start = 'not a timestamp'")
		must.NoError(t, err)

		_, err = store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		test.Error(t, err)
	})
}

func TestSQLStore_GuardMisses(T *testing.T) {
	T.Parallel()

	T.Run("reports a settle against a total that is not there", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		// The flusher whose lease lapsed, coming back to a row somebody else has
		// already moved on. Reported rather than treated as success, because two
		// flushers each believing they own the next sequence number is how the
		// same delta reaches the provider under two different keys.
		err := store.MarkFlushed(t.Context(), &Total{
			Subject: testSubject, Meter: testMeter, PeriodStart: monthBounds.Start,
		}, 1, baseTime)

		must.Error(t, err)
		test.StrContains(t, err.Error(), "flush sequence")
	})

	T.Run("reports a release against a total that is not there", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		err := store.ReleaseFlush(t.Context(), &Total{
			Subject: testSubject, Meter: testMeter, PeriodStart: monthBounds.Start,
		}, "boom", baseTime)

		test.Error(t, err)
	})
}

func TestFlusher_SkipsASettledClaim(T *testing.T) {
	T.Parallel()

	store := newSQLiteEnv(T).newStore(T)

	// Claimed on a predicate that said it owed the provider something, settled by
	// somebody else between the select and the read. Settling it again would
	// advance the sequence for a post that never happened, which would make the
	// next genuine post's key distinct from the one a retry would use.
	env := newTestFlusherOver(T, &settledClaimStore{Store: store}, staticMapper("si_123"))

	result, err := env.flusher.Flush(T.Context())
	must.NoError(T, err)

	test.EqOp(T, 1, result.Claimed)
	test.EqOp(T, 1, result.Skipped)
	test.EqOp(T, 0, result.Flushed)
	test.SliceEmpty(T, env.reporter.recorded())
}

// settledClaimStore hands back a claim with nothing left to post.
type settledClaimStore struct {
	Store
}

func (s *settledClaimStore) ClaimFlushable(context.Context, time.Time, int, int, time.Time) ([]*Total, error) {
	return []*Total{{
		Subject: testSubject, Meter: testMeter,
		PeriodStart: monthBounds.Start, PeriodEnd: monthBounds.End,
		Quantity: 42, FlushedQuantity: 42,
	}}, nil
}

func TestTruncateError_CutsMidRune(T *testing.T) {
	T.Parallel()

	// A one-byte prefix in front of two-byte runes puts the cut squarely inside
	// one of them, which is the case the rune-boundary walk exists for. Half a
	// multi-byte rune is invalid UTF-8 that some JSON encoders refuse and others
	// silently replace.
	rendered := truncateError(messageError("x" + strings.Repeat("é", maxStoredErrorLength)))

	test.Less(T, maxStoredErrorLength, len(rendered))
	test.EqOp(T, rendered, strings.ToValidUTF8(rendered, ""))
}

// messageError is an error whose rendering is exactly its own text, so a test
// can control the byte length precisely.
type messageError string

func (e messageError) Error() string { return string(e) }

func TestSQLStore_WriteFaults(T *testing.T) {
	T.Parallel()

	T.Run("Consume reports a failed apply", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		// A trigger that refuses updates. Consume opens the row with an INSERT and
		// locks it with a SELECT, so only the UPDATE that applies the decision
		// fails — which is the statement that turns an allowed consume into a
		// recorded one, and the one whose failure must not be reported as success.
		_, err := env.client.Writer().ExecContext(t.Context(),
			"CREATE TRIGGER "+prefix+"_no_update BEFORE UPDATE ON "+prefix+
				"_metering_totals BEGIN SELECT RAISE(ABORT, 'no updates'); END")
		must.NoError(t, err)

		_, err = store.Consume(t.Context(), newEntry("req-1", 1, AggregationSum), 100, BehaviorBlock, baseTime)
		test.Error(t, err)
	})

	T.Run("ClaimFlushable reports a failed lease", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecord(t, store, newEntry("req-1", 42, AggregationSum)))

		// The select finds the total and the UPDATE that leases it does not land.
		// Reported rather than swallowed: a claim that reports totals it never
		// leased would have two flushers posting the same delta.
		_, err := env.client.Writer().ExecContext(t.Context(),
			"CREATE TRIGGER "+prefix+"_no_update BEFORE UPDATE ON "+prefix+
				"_metering_totals BEGIN SELECT RAISE(ABORT, 'no updates'); END")
		must.NoError(t, err)

		_, err = store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		test.Error(t, err)
	})
}

func TestDurableRecorder_DropsEveryRecord(T *testing.T) {
	T.Parallel()

	recorder, store, _ := newTestRecorder(T)

	// Every record named an unregistered meter, so nothing survives preparation
	// and there is nothing to hand the store. Reported as success, because the
	// record that named an unknown meter is not the caller's to fix — see
	// RecorderConfig.RejectUnknownMeters.
	must.NoError(T, recorder.Record(T.Context(),
		Usage{Subject: testSubject, Meter: "not_registered", Quantity: 1, IdempotencyKey: "req-1"},
		Usage{Subject: testSubject, Meter: "also_not_registered", Quantity: 2, IdempotencyKey: "req-2"},
	))

	test.EqOp(T, int64(0), totalOf(T, store))
}

// unreadableResult is a sql.Result that cannot say how many rows it touched.
//
// It stands in for a driver that reports the failure late, which is rarer than
// the ones above and worse, because the value the caller falls back to is a
// plausible one. insertEvent is the case that matters: an error read as "zero
// rows affected" is indistinguishable from a duplicate idempotency key, so the
// record would be dropped as already-counted and the customer never billed for
// it. Every RowsAffected in this package is checked for exactly that reason.
type unreadableResult struct{}

var _ sql.Result = (*unreadableResult)(nil)

func (unreadableResult) LastInsertId() (int64, error) { return 0, errArbitrary }
func (unreadableResult) RowsAffected() (int64, error) { return 0, errArbitrary }

// unreadableExecutor passes every read through and makes every write's result
// unreadable.
type unreadableExecutor struct {
	database.SQLQueryExecutor
}

func (e *unreadableExecutor) ExecContext(
	ctx context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	if _, err := e.SQLQueryExecutor.ExecContext(ctx, query, args...); err != nil {
		return nil, err
	}

	return unreadableResult{}, nil
}

// unreadableClient is a database.Client whose writes land and whose results
// cannot be read.
type unreadableClient struct {
	database.Client
}

var _ database.Client = (*unreadableClient)(nil)

func (c *unreadableClient) Writer() database.SQLQueryExecutor {
	return &unreadableExecutor{SQLQueryExecutor: c.Client.Writer()}
}

func (c *unreadableClient) WithTransaction(
	ctx context.Context,
	fn func(querier database.SQLQueryExecutor) error,
) error {
	return c.Client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		return fn(&unreadableExecutor{SQLQueryExecutor: q})
	})
}

// newUnreadableStore migrates a table pair and returns a store whose writes
// report results that cannot be read.
func newUnreadableStore(t *testing.T) Store {
	t.Helper()

	env := newSQLiteEnv(t)
	_, prefix := env.newStoreWithPrefix(t)

	store, err := NewSQLStore(&unreadableClient{Client: env.client}, WithTablePrefix(prefix))
	must.NoError(t, err)

	return store
}

func TestSQLStore_UnreadableResults(T *testing.T) {
	T.Parallel()

	T.Run("Record reports rather than dropping the usage", func(t *testing.T) {
		t.Parallel()

		// The failure mode this guards: read as zero rows affected, the insert
		// looks like a duplicate key and the record is silently discarded.
		_, err := newUnreadableStore(t).Record(t.Context(),
			[]Entry{newEntry("req-1", 1, AggregationSum)}, baseTime)

		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("ReapEvents reports rather than reporting a count", func(t *testing.T) {
		t.Parallel()

		_, err := newUnreadableStore(t).ReapEvents(t.Context(), baseTime, 100)

		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("MarkFlushed reports rather than assuming a guard miss", func(t *testing.T) {
		t.Parallel()

		// Read as zero rows, a settle would look like a lapsed lease and the
		// flusher would leave the sequence unadvanced against a post that landed.
		err := newUnreadableStore(t).MarkFlushed(t.Context(), &Total{
			Subject: testSubject, Meter: testMeter, PeriodStart: monthBounds.Start,
		}, 1, baseTime)

		test.ErrorIs(t, err, errArbitrary)
	})
}
