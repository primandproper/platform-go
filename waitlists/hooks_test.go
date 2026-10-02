package waitlists

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHook is a hook refusing the write it was called from.
var errHook = platformerrors.New("the hook failed")

// hookCall is one hook invocation, as the recording hooks saw it.
type hookCall struct {
	list      *List
	signup    *Signup
	subject   Subject
	name      string
	scope     tenancy.Scope
	withdrawn int64
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

func (h *recordingHooks) AfterCreateList(_ context.Context, _ database.Tx, scope tenancy.Scope, list *List) error {
	return h.record(&hookCall{name: "AfterCreateList", scope: scope, list: list})
}

func (h *recordingHooks) AfterUpdateList(_ context.Context, _ database.Tx, scope tenancy.Scope, list *List) error {
	return h.record(&hookCall{name: "AfterUpdateList", scope: scope, list: list})
}

func (h *recordingHooks) AfterArchiveList(_ context.Context, _ database.Tx, scope tenancy.Scope, list *List) error {
	return h.record(&hookCall{name: "AfterArchiveList", scope: scope, list: list})
}

func (h *recordingHooks) AfterJoin(_ context.Context, _ database.Tx, scope tenancy.Scope, signup *Signup) error {
	return h.record(&hookCall{name: "AfterJoin", scope: scope, signup: signup})
}

func (h *recordingHooks) AfterUpdateSignupNotes(_ context.Context, _ database.Tx, scope tenancy.Scope, signup *Signup) error {
	return h.record(&hookCall{name: "AfterUpdateSignupNotes", scope: scope, signup: signup})
}

func (h *recordingHooks) AfterConfirm(_ context.Context, _ database.Tx, scope tenancy.Scope, signup *Signup) error {
	return h.record(&hookCall{name: "AfterConfirm", scope: scope, signup: signup})
}

func (h *recordingHooks) AfterInvite(_ context.Context, _ database.Tx, scope tenancy.Scope, signup *Signup) error {
	return h.record(&hookCall{name: "AfterInvite", scope: scope, signup: signup})
}

func (h *recordingHooks) AfterConvert(_ context.Context, _ database.Tx, scope tenancy.Scope, signup *Signup) error {
	return h.record(&hookCall{name: "AfterConvert", scope: scope, signup: signup})
}

func (h *recordingHooks) AfterWithdraw(_ context.Context, _ database.Tx, scope tenancy.Scope, signup *Signup) error {
	return h.record(&hookCall{name: "AfterWithdraw", scope: scope, signup: signup})
}

func (h *recordingHooks) AfterWithdrawSignupsForSubject(
	_ context.Context,
	_ database.Tx,
	scope tenancy.Scope,
	subject Subject,
	withdrawn int64,
) error {
	return h.record(&hookCall{name: "AfterWithdrawSignupsForSubject", scope: scope, subject: subject, withdrawn: withdrawn})
}

func (h *recordingHooks) AfterArchiveSignup(_ context.Context, _ database.Tx, scope tenancy.Scope, signup *Signup) error {
	return h.record(&hookCall{name: "AfterArchiveSignup", scope: scope, signup: signup})
}

// last is the most recent call, failing the test when there was none.
func (h *recordingHooks) last(tb testing.TB) hookCall {
	tb.Helper()

	must.SliceNotEmpty(tb, h.calls)

	return h.calls[len(h.calls)-1]
}

// runHooksSuite is every assertion about the hooks a write runs: that each write
// calls its own, with the row it answers with, and that a hook's refusal is the
// write's.
func runHooksSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("every write calls its hook with the row it answers with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		call := hooks.last(t)
		test.EqOp(t, "AfterCreateList", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, list.ID, call.list.ID)
		test.False(t, call.list.CreatedAt.IsZero())

		list.Description = "rewritten"
		updated, err := env.updateList(t, store, testScope, list)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterUpdateList", call.name)
		test.EqOp(t, "rewritten", call.list.Description)
		test.EqOp(t, updated, call.list)

		signup := mustJoin(t, env, store, testScope, list.ID, &Signup{
			Contact: "ada@example.com",
			Subject: testSubject,
			Status:  StatusPending,
		})
		call = hooks.last(t)
		test.EqOp(t, "AfterJoin", call.name)
		test.EqOp(t, signup.ID, call.signup.ID)
		test.EqOp(t, StatusPending, call.signup.Status)

		_, err = env.updateNotes(t, store, testScope, list.ID, signup.ID, "met at the conference")
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterUpdateSignupNotes", call.name)
		test.EqOp(t, "met at the conference", call.signup.Notes)

		_, err = env.confirm(t, store, testScope, list.ID, signup.ID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterConfirm", call.name)
		test.EqOp(t, StatusWaiting, call.signup.Status)
		test.NotNil(t, call.signup.StatusChangedAt)

		mustInvite(t, env, store, testScope, list.ID, signup.ID)
		call = hooks.last(t)
		test.EqOp(t, "AfterInvite", call.name)
		test.EqOp(t, StatusInvited, call.signup.Status)
		test.EqOp(t, "ada@example.com", call.signup.Contact)

		_, err = env.convert(t, store, testScope, list.ID, signup.ID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterConvert", call.name)
		test.EqOp(t, StatusConverted, call.signup.Status)

		archived := mustArchiveSignup(t, env, store, testScope, list.ID, signup.ID)
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveSignup", call.name)
		test.EqOp(t, archived, call.signup)

		mustArchiveList(t, env, store, testScope, list.ID)
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveList", call.name)
		test.EqOp(t, list.ID, call.list.ID)
		test.NotNil(t, call.list.ArchivedAt)

		test.SliceLen(t, 9, hooks.calls)
	})

	t.Run("a withdrawal's hook is handed the row from before the blanking", func(t *testing.T) {
		t.Parallel()

		// The reason the hook exists rather than a wrapper: after the statement the
		// row holds a status and a digest, and the hook still has the person.
		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		signup := mustJoin(t, env, store, testScope, list.ID, &Signup{
			Contact: "ada@example.com",
			Subject: testSubject,
			Notes:   "a note",
		})

		withdrawn := mustWithdraw(t, env, store, testScope, list.ID, signup.ID)

		call := hooks.last(t)
		test.EqOp(t, "AfterWithdraw", call.name)
		test.EqOp(t, withdrawn, call.signup)
		test.EqOp(t, "ada@example.com", call.signup.Contact)
		test.EqOp(t, testSubject, call.signup.Subject)
		test.EqOp(t, StatusWaiting, call.signup.Status)
	})

	t.Run("an erasure's hook is called with the count, zero included", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com", Subject: testSubject})

		for _, want := range []int64{1, 0} {
			must.NoError(t, env.inTx(t, func(tx database.Tx) error {
				_, err := store.WithdrawSignupsForSubject(t.Context(), tx, testScope, testSubject)

				return err
			}))

			call := hooks.last(t)
			test.EqOp(t, "AfterWithdrawSignupsForSubject", call.name)
			test.EqOp(t, testSubject, call.subject)
			test.EqOp(t, want, call.withdrawn)
		}
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})
		before := len(hooks.calls)

		// Waiting, not invited: the guard refuses the move.
		_, err := env.convert(t, store, testScope, list.ID, signup.ID)
		must.ErrorIs(t, err, ErrWrongStatus)

		_, err = env.join(t, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})
		must.ErrorIs(t, err, ErrAlreadySignedUp)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the write's, and the write rolls back with it", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterJoin"}
		store := env.newStore(t, WithHooks(hooks))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))

		joined, err := env.join(t, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})
		must.ErrorIs(t, err, errHook)
		test.Nil(t, joined)

		_, err = store.GetSignupByContact(t.Context(), env.reader(), testScope, list.ID, "ada@example.com")
		must.ErrorIs(t, err, ErrSignupNotFound)
	})

	t.Run("a transition's hook error is the transition's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterInvite"}
		store := env.newStore(t, WithHooks(hooks))

		list := mustCreateList(t, env, store, testScope, openList("Launch"))
		signup := mustJoin(t, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

		invited, err := env.invite(t, store, testScope, list.ID, signup.ID)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, invited)

		read, err := store.GetSignup(t.Context(), env.reader(), testScope, list.ID, signup.ID)
		must.NoError(t, err)
		test.EqOp(t, StatusWaiting, read.Status)
	})

	t.Run("nil hooks are no hooks", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t, WithHooks(nil))

		mustCreateList(t, env, store, testScope, openList("Launch"))
	})
}
