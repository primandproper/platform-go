package metering

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// suiteTenancy is the dimension the rest of this suite holds constant, asserted
// against every dialect because the scope is a column and a key component on all
// three: it is in both primary keys, in every consumer statement's predicate, and
// in the correlation the retention pass makes between an event and its total.
//
// Every assertion here is about two tenants rather than one. A suite that only
// ever recorded for testScope would pass unchanged against a store that bound the
// column and then ignored it in the predicates, which is precisely the mistake
// that reads as "usage appearing from nowhere" on somebody's invoice.
func suiteTenancy(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("two tenants sharing one idempotency key are two counts", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The same subject, the same meter, the same key — which is the ordinary
		// case rather than a contrived one, because two tenants' request IDs come
		// from two sequences nobody reconciled. Keyed without the scope the second
		// insert is silently deduped against the first, and that customer is
		// under-billed forever.
		entry := newEntry("req-1", 42, AggregationSum)

		mine, err := env.recordAs(t, store, testScope, []Entry{entry}, baseTime)
		must.NoError(t, err)
		test.EqOp(t, 1, mine.Accepted)
		test.EqOp(t, 0, mine.Duplicates)

		theirs, err := env.recordAs(t, store, otherScope, []Entry{entry}, baseTime)
		must.NoError(t, err)
		test.EqOp(t, 1, theirs.Accepted)
		test.EqOp(t, 0, theirs.Duplicates)

		test.EqOp(t, int64(42), env.mustTotalAs(t, store, testScope).Quantity)
		test.EqOp(t, int64(42), env.mustTotalAs(t, store, otherScope).Quantity)
	})

	t.Run("a read cannot reach another tenant's total", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecordAs(t, env, store, testScope, newEntry("req-1", 42, AggregationSum)))

		// Zero and no error, which is what an absent row means here — nothing
		// recorded is a number rather than a missing value. What matters is that
		// it is the answer the neighbor gets for a period somebody else filled.
		nextDoor := env.mustTotalAs(t, store, otherScope)
		test.EqOp(t, int64(0), nextDoor.Quantity)

		// And the zero total carries the scope the read asked about, so a caller
		// folding it back into a write is not holding a Total no statement accepts.
		test.EqOp(t, otherScope, nextDoor.Scope)

		// The row that is there reports its own scope, read back off the column
		// rather than echoed from the argument.
		test.EqOp(t, testScope, env.mustTotalAs(t, store, testScope).Scope)
	})

	t.Run("a consume decides against the tenant's own total", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// Eight of a limit of ten, twice, for two tenants. Sharing one total the
		// second would be refused — which is a customer told they are over a limit
		// somebody else spent.
		mine, err := env.consumeAs(t, store, testScope,
			newEntry("req-1", 8, AggregationSum), 10, BehaviorBlock, baseTime)
		must.NoError(t, err)
		test.True(t, mine.Allowed)

		theirs, err := env.consumeAs(t, store, otherScope,
			newEntry("req-1", 8, AggregationSum), 10, BehaviorBlock, baseTime)
		must.NoError(t, err)
		test.True(t, theirs.Allowed)
		test.EqOp(t, int64(8), theirs.Used)

		// And each tenant's own second consume is refused on their own number.
		over, err := env.consumeAs(t, store, testScope,
			newEntry("req-2", 8, AggregationSum), 10, BehaviorBlock, baseTime)
		must.NoError(t, err)
		test.False(t, over.Allowed)
	})

	t.Run("the flush claim crosses every tenant and each total carries its scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecordAs(t, env, store, testScope, newEntry("req-1", 11, AggregationSum)))
		must.NoError(t, mustRecordAs(t, env, store, otherScope, newEntry("req-1", 22, AggregationSum)))

		// One pass, both tenants. A per-tenant flusher would be a worker nobody
		// could schedule without first enumerating the tenants.
		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 2, claimed)

		byScope := map[tenancy.Scope]int64{}
		for _, total := range claimed {
			byScope[total.Scope] = total.Quantity
		}

		test.EqOp(t, int64(11), byScope[testScope])
		test.EqOp(t, int64(22), byScope[otherScope])

		// The settlement binds the scope off the row the claim handed it, so
		// settling one tenant's total leaves the other's owing.
		for _, total := range claimed {
			if total.Scope == testScope {
				must.NoError(t, store.MarkFlushed(t.Context(), total, total.Quantity, baseTime))
			}
		}

		test.EqOp(t, int64(11), env.mustTotalAs(t, store, testScope).FlushedQuantity)
		test.EqOp(t, int64(0), env.mustTotalAs(t, store, otherScope).FlushedQuantity)
	})

	t.Run("a settle cannot reach another tenant's row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.NoError(t, mustRecordAs(t, env, store, testScope, newEntry("req-1", 11, AggregationSum)))
		must.NoError(t, mustRecordAs(t, env, store, otherScope, newEntry("req-1", 22, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 2, claimed)

		// A Total whose scope has been swapped addresses no row, and that is the
		// guard rather than an error the driver raises: the settlement is keyed on
		// the whole of the key, and a flusher that could settle by subject and
		// meter alone would advance a sequence in a tenant it never posted for.
		strayed := *claimed[0]
		strayed.Scope = tenancy.Of("tenant-nobody")

		must.Error(t, store.MarkFlushed(t.Context(), &strayed, strayed.Quantity, baseTime))
		must.Error(t, store.ReleaseFlush(t.Context(), &strayed, "boom", baseTime))

		// Neither tenant's row moved.
		test.EqOp(t, int64(0), env.mustTotalAs(t, store, testScope).FlushedQuantity)
		test.EqOp(t, int64(0), env.mustTotalAs(t, store, otherScope).FlushedQuantity)
	})

	t.Run("retention spans tenants and respects each total's own debt", func(t *testing.T) {
		t.Parallel()

		store, prefix := env.newStoreWithPrefix(t)

		must.NoError(t, mustRecordAs(t, env, store, testScope, newEntry("req-1", 11, AggregationSum)))
		must.NoError(t, mustRecordAs(t, env, store, otherScope, newEntry("req-1", 22, AggregationSum)))

		claimed, err := store.ClaimFlushable(t.Context(), baseTime, 10, 5, baseTime.Add(time.Minute))
		must.NoError(t, err)
		must.SliceLen(t, 2, claimed)

		for _, total := range claimed {
			if total.Scope == testScope {
				must.NoError(t, store.MarkFlushed(t.Context(), total, total.Quantity, baseTime))
			}
		}

		// One horizon over the whole table, and a correlation that is per tenant:
		// the settled tenant's evidence is retired, and the one still owing keeps
		// theirs. A correlation that omitted the scope would have each tenant's
		// retention decided by the other's flush backlog, in both directions.
		reaped, err := store.ReapEvents(t.Context(), baseTime.Add(time.Hour), 100)
		must.NoError(t, err)

		test.EqOp(t, int64(1), reaped)
		test.EqOp(t, 1, countRows(t, env, prefix+"_metering_events"))
		test.EqOp(t, 2, countRows(t, env, prefix+"_metering_totals"))
	})

	t.Run("an unset scope is refused rather than read as the global one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// tenancy.Scope tells its zero value apart from Global(), which is what
		// makes this refusable at all: the global scope is stored as the empty
		// identifier, so a store that accepted the zero value would file the write
		// that forgot the argument in the tenant that matches nobody.
		_, err := env.recordAs(t, store, tenancy.Scope{}, []Entry{newEntry("req-1", 1, AggregationSum)}, baseTime)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = env.totalAs(t, store, tenancy.Scope{}, testSubject, testMeter, monthBounds)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = env.consumeAs(t, store, tenancy.Scope{},
			newEntry("req-1", 1, AggregationSum), 10, BehaviorBlock, baseTime)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	t.Run("the global scope records and reads like any other", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// What a single-tenant application passes everywhere, and it has to behave
		// exactly as the column's absence did: the empty identifier is a scope and
		// matches only itself.
		must.NoError(t, mustRecordAs(t, env, store, tenancy.Global(), newEntry("req-1", 7, AggregationSum)))

		global := env.mustTotalAs(t, store, tenancy.Global())
		test.EqOp(t, int64(7), global.Quantity)
		test.True(t, global.Scope.IsGlobal())

		// And it is not visible to a tenant, which is the half of "matches only
		// itself" that a default on the column would have destroyed.
		test.EqOp(t, int64(0), env.mustTotalAs(t, store, testScope).Quantity)
	})
}

// TestDurableRecorder_Tenancy pins what the ingest path does with the scope: it
// refuses an unset one at the boundary the caller reached, and files the batch
// under the one it was given.
func TestDurableRecorder_Tenancy(T *testing.T) {
	T.Parallel()

	T.Run("refuses an unset scope before doing any work", func(t *testing.T) {
		t.Parallel()

		recorder, env, _, _ := newTestRecorder(t)

		_, err := inTx(t, env.client, func(tx database.Tx) (struct{}, error) {
			return struct{}{}, recorder.Record(t.Context(), tx, tenancy.Scope{}, Usage{
				Subject: testSubject, Meter: testMeter, Quantity: 1, IdempotencyKey: "req-1",
			})
		})

		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("files the batch under the scope it was given", func(t *testing.T) {
		t.Parallel()

		recorder, env, store, _ := newTestRecorder(t)

		_, err := inTx(t, env.client, func(tx database.Tx) (struct{}, error) {
			return struct{}{}, recorder.Record(t.Context(), tx, otherScope, Usage{
				Subject: testSubject, Meter: testMeter, Quantity: 5, IdempotencyKey: "req-1",
			})
		})
		must.NoError(t, err)

		test.EqOp(t, int64(5), env.mustTotalAs(t, store, otherScope).Quantity)
		test.EqOp(t, int64(0), env.mustTotalAs(t, store, testScope).Quantity)
	})
}

// TestQuotaEnforcer_Tenancy pins the read path's half, and the cache beneath it.
//
// The cache is the part worth a test of its own: a key that could not tell two
// tenants apart would answer one tenant's quota question with the other's usage
// for the whole staleness budget, from a store nobody can see into.
func TestQuotaEnforcer_Tenancy(T *testing.T) {
	T.Parallel()

	T.Run("one tenant's cached total does not answer another's check", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		// Recorded for one tenant, and read by both. The first Check populates the
		// cache; the second must miss it rather than be served the neighbor's
		// number.
		must.NoError(t, mustRecordAs(t, env.db, env.store, testScope, newEntry("req-1", 40, AggregationSum)))

		mine, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)
		test.EqOp(t, int64(41), mine.Used)

		theirs, err := env.enforcer.Check(t.Context(), otherScope, testSubject, testMeter, 1)
		must.NoError(t, err)
		test.EqOp(t, int64(1), theirs.Used)

		// The cached read agrees with the durable one it came from.
		again, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)
		test.EqOp(t, int64(41), again.Used)
		test.True(t, again.Stale)
	})

	T.Run("a consume records against the scope it was given", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		decision, err := inTx(t, env.db.client, func(tx database.Tx) (*Decision, error) {
			return env.enforcer.Consume(t.Context(), tx, otherScope, testSubject, testMeter, 30)
		})
		must.NoError(t, err)
		test.True(t, decision.Allowed)

		test.EqOp(t, int64(30), env.db.mustTotalAs(t, env.store, otherScope).Quantity)
		test.EqOp(t, int64(0), env.db.mustTotalAs(t, env.store, testScope).Quantity)
	})

	T.Run("refuses an unset scope on both methods", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		_, err := env.enforcer.Check(t.Context(), tenancy.Scope{}, testSubject, testMeter, 1)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = inTx(t, env.db.client, func(tx database.Tx) (*Decision, error) {
			return env.enforcer.ConsumeUsage(t.Context(), tx, tenancy.Scope{}, Usage{
				Subject: testSubject, Meter: testMeter, Quantity: 1, IdempotencyKey: "req-1",
			})
		})
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	T.Run("fails closed on an unset scope even with FailOpen set", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{FailOpen: true},
			store, newTestRegistry(t, BehaviorBlock, 100), WithEnforcerClock(newStubClock()))
		must.NoError(t, err)

		// The fail-open budget is for a store that could not be read. A caller who
		// lost the scope is a bug, and allowing on it would answer a question
		// nobody asked with a decision that permits.
		_, err = enforcer.Check(t.Context(), tenancy.Scope{}, testSubject, testMeter, 1)
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})
}

// mustRecordAs is mustRecord for a named scope.
func mustRecordAs(t *testing.T, env *storeEnv, store Store, scope tenancy.Scope, entries ...Entry) error {
	t.Helper()

	_, err := env.recordAs(t, store, scope, entries, baseTime)

	return err
}

// mustTotalAs reads the suite's usual subject, meter, and month for one scope.
func (e *storeEnv) mustTotalAs(tb testing.TB, store Store, scope tenancy.Scope) *Total {
	tb.Helper()

	got, err := e.totalAs(tb, store, scope, testSubject, testMeter, monthBounds)
	must.NoError(tb, err)

	return got
}
