package database

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/links"

	"github.com/primandproper/primitives-go/v2/database"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// conflictRecordingClient is a client that notes what each transaction was
// asked to retry, and runs it on the client underneath.
//
// The retry itself is primitives-go's, and is proven there against a deadlock
// forced on a real MySQL. What this package owes is the asking: a store that
// dropped the option would pass every other test here, and would go on handing
// a redeemer that lost a deadlock a 500 instead of the sentence it earned.
type conflictRecordingClient struct {
	database.Client

	attempts []uint

	mu sync.Mutex
}

var _ database.TxOptionsAccess = (*conflictRecordingClient)(nil)

func (c *conflictRecordingClient) WithTransactionOptions(
	ctx context.Context,
	fn func(database.Tx) error,
	opts ...database.TxOption,
) error {
	c.mu.Lock()
	c.attempts = append(c.attempts, database.NewTxConfig(opts...).ConflictAttempts)
	c.mu.Unlock()

	return database.WithTransaction(ctx, c.Client, fn, opts...)
}

func (c *conflictRecordingClient) recorded() []uint {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]uint(nil), c.attempts...)
}

func TestStore_retriesTheTransactionsThatDeadlockEachOther(T *testing.T) {
	T.Parallel()

	newStore := func(t *testing.T) (*Store, *conflictRecordingClient) {
		t.Helper()

		client := &conflictRecordingClient{Client: newTestClient(t)}

		store, err := New(&Config{}, client, WithClock(newFakeClock()), WithLogger(loggingnoop.NewLogger()))
		must.NoError(t, err)

		return store, client
	}

	T.Run("resolve", func(t *testing.T) {
		t.Parallel()

		store, client := newStore(t)
		put(t, store, testID, activeRecord())

		at := mintedAt.Add(time.Minute)
		_, err := store.Resolve(t.Context(), testID, links.StateRedeemed, at, at.Add(time.Hour))
		must.NoError(t, err)

		test.Eq(t, []uint{conflictAttempts}, client.recorded())
	})

	T.Run("revoke for subject", func(t *testing.T) {
		t.Parallel()

		store, client := newStore(t)
		put(t, store, testID, activeRecord())

		at := mintedAt.Add(time.Minute)
		revoked, err := store.RevokeForSubject(t.Context(), testSubject, at, at.Add(time.Hour))
		must.NoError(t, err)
		test.EqOp(t, int64(1), revoked)

		test.Eq(t, []uint{conflictAttempts}, client.recorded())
	})
}
