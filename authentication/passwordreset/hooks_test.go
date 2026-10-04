package passwordreset

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHook is a hook refusing the write it was called from.
var errHook = platformerrors.New("the hook failed")

// hookCall is one hook invocation, as the recording hooks saw it.
type hookCall struct {
	token  *Token
	name   string
	userID string
	scope  tenancy.Scope
	count  int64
}

// recordingHooks remembers every call, and fails the one named by failOn.
//
// It implements Hooks outright rather than embedding NoopHooks, so that a method
// added to Hooks later fails this file's build until it is recorded here too,
// rather than arriving as a no-op the suite never asserts on.
type recordingHooks struct {
	failOn string
	calls  []hookCall
}

var _ Hooks = (*recordingHooks)(nil)

func (h *recordingHooks) record(call *hookCall) error {
	h.calls = append(h.calls, *call)

	if call.name == h.failOn {
		return errHook
	}

	return nil
}

func (h *recordingHooks) AfterIssue(_ context.Context, _ database.Tx, scope tenancy.Scope, token *Token) error {
	return h.record(&hookCall{name: "AfterIssue", scope: scope, token: token})
}

func (h *recordingHooks) AfterConsume(_ context.Context, _ database.Tx, scope tenancy.Scope, token *Token) error {
	return h.record(&hookCall{name: "AfterConsume", scope: scope, token: token})
}

func (h *recordingHooks) AfterRevokeForUser(_ context.Context, _ database.Tx, scope tenancy.Scope, userID string, revoked int64) error {
	return h.record(&hookCall{name: "AfterRevokeForUser", scope: scope, userID: userID, count: revoked})
}

func (h *recordingHooks) AfterDeleteForUser(_ context.Context, _ database.Tx, scope tenancy.Scope, userID string, deleted int64) error {
	return h.record(&hookCall{name: "AfterDeleteForUser", scope: scope, userID: userID, count: deleted})
}

// last is the most recent call, failing the test when there was none.
func (h *recordingHooks) last(tb testing.TB) hookCall {
	tb.Helper()

	must.SliceNotEmpty(tb, h.calls)

	return h.calls[len(h.calls)-1]
}

// hookUser is a principal of the calling subtest's own, because the container
// suite runs every subtest against one table and the counts asserted here are
// per principal.
func hookUser() string { return "hooks_" + identifiers.New() }

// runHooksSuite is every assertion about the hooks a write runs: that each write
// calls its own, with the row it answers with, and that a hook's refusal is the
// write's. newStore builds a store over the suite's database running the hooks
// given; SQLite and both container servers run it.
//
// The subtests are sequential rather than parallel because the container suite
// shares one clock and one table with the subtests around it.
func runHooksSuite(t *testing.T, newStore func(t *testing.T, hooks Hooks) *SQLStore) {
	t.Helper()

	t.Run("every write calls its hook with the row it answers with", func(t *testing.T) {
		hooks := &recordingHooks{}
		store := newStore(t, hooks)
		userID := hookUser()

		issuance, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)
		call := hooks.last(t)
		test.EqOp(t, "AfterIssue", call.name)
		test.EqOp(t, testScope(), call.scope)
		// The same value the Issuance carries, and not the Issuance: the secret
		// has no route into a hook.
		test.EqOp(t, issuance.Token, call.token)
		test.Nil(t, call.token.RedeemedAt)

		consumed, err := consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterConsume", call.name)
		test.EqOp(t, consumed, call.token)
		test.NotNil(t, call.token.RedeemedAt)

		_, err = issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)

		revoked, err := revokeForUser(t, store, testScope(), userID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterRevokeForUser", call.name)
		test.EqOp(t, userID, call.userID)
		test.EqOp(t, int64(1), revoked)
		test.EqOp(t, revoked, call.count)

		// The redeemed row is what the revocation spared and the erasure takes.
		deleted, err := deleteForUser(t, store, testScope(), userID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterDeleteForUser", call.name)
		test.EqOp(t, userID, call.userID)
		test.EqOp(t, int64(1), deleted)
		test.EqOp(t, deleted, call.count)

		test.SliceLen(t, 5, hooks.calls)
	})

	t.Run("the bulk writes' hooks are called with zero, too", func(t *testing.T) {
		hooks := &recordingHooks{}
		store := newStore(t, hooks)
		userID := hookUser()

		_, err := revokeForUser(t, store, testScope(), userID)
		must.NoError(t, err)
		call := hooks.last(t)
		test.EqOp(t, "AfterRevokeForUser", call.name)
		test.EqOp(t, int64(0), call.count)

		_, err = deleteForUser(t, store, testScope(), userID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterDeleteForUser", call.name)
		test.EqOp(t, int64(0), call.count)
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		hooks := &recordingHooks{}
		store := newStore(t, hooks)
		userID := hookUser()

		issuance, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)
		_, err = consume(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)
		before := len(hooks.calls)

		_, err = consume(t, store, testScope(), issuance.Secret)
		must.ErrorIs(t, err, ErrTokenRedeemed)

		_, err = consume(t, store, testScope(), "nobody-was-sent-this")
		must.ErrorIs(t, err, ErrTokenNotFound)

		_, err = issueFor(t, store, testScope(), "", time.Hour)
		must.ErrorIs(t, err, ErrEmptyUserID)

		_, err = revokeForUser(t, store, testScope(), "")
		must.ErrorIs(t, err, ErrEmptyUserID)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the issuance's, and the row rolls back with it", func(t *testing.T) {
		hooks := &recordingHooks{failOn: "AfterIssue"}
		store := newStore(t, hooks)
		userID := hookUser()

		issuance, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, issuance)

		held, err := store.ListForUser(t.Context(), store.db.Writer(), testScope(), userID)
		must.NoError(t, err)
		test.SliceEmpty(t, held)
	})

	t.Run("a hook's error is the redemption's, and the token stays live", func(t *testing.T) {
		hooks := &recordingHooks{failOn: "AfterConsume"}
		store := newStore(t, hooks)

		issuance, err := issueFor(t, store, testScope(), hookUser(), time.Hour)
		must.NoError(t, err)

		consumed, err := consume(t, store, testScope(), issuance.Secret)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, consumed)

		token, err := verify(t, store, testScope(), issuance.Secret)
		must.NoError(t, err)
		test.Nil(t, token.RedeemedAt)
	})

	t.Run("a hook's error is the erasure's, and the rows survive it", func(t *testing.T) {
		hooks := &recordingHooks{failOn: "AfterDeleteForUser"}
		store := newStore(t, hooks)
		userID := hookUser()

		_, err := issueFor(t, store, testScope(), userID, time.Hour)
		must.NoError(t, err)

		deleted, err := deleteForUser(t, store, testScope(), userID)
		must.ErrorIs(t, err, errHook)
		test.EqOp(t, int64(0), deleted)

		held, err := store.ListForUser(t.Context(), store.db.Writer(), testScope(), userID)
		must.NoError(t, err)
		test.SliceLen(t, 1, held)
	})
}

// TestSQLStore_Hooks runs runHooksSuite on SQLite.
//
//nolint:tparallel // the suite is sequential, deliberately: the container suite runs it against one shared clock and table.
func TestSQLStore_Hooks(T *testing.T) {
	T.Parallel()

	runHooksSuite(T, func(t *testing.T, hooks Hooks) *SQLStore {
		t.Helper()

		store, _ := newHookedTestStore(t, hooks)

		return store
	})
}
