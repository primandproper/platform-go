package comments

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/internal/txcount"

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
	before  *Comment
	comment *Comment
	target  Target
	name    string
	author  string
	scope   tenancy.Scope
	deleted int64
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

func (h *recordingHooks) AfterCreateComment(_ context.Context, _ database.Tx, scope tenancy.Scope, comment *Comment) error {
	return h.record(&hookCall{name: "AfterCreateComment", scope: scope, comment: comment})
}

func (h *recordingHooks) AfterUpdateComment(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Comment) error {
	return h.record(&hookCall{name: "AfterUpdateComment", scope: scope, comment: after, before: before})
}

func (h *recordingHooks) AfterArchiveComment(_ context.Context, _ database.Tx, scope tenancy.Scope, comment *Comment) error {
	return h.record(&hookCall{name: "AfterArchiveComment", scope: scope, comment: comment})
}

func (h *recordingHooks) AfterDeleteCommentsForTarget(
	_ context.Context,
	_ database.Tx,
	scope tenancy.Scope,
	target Target,
	deleted int64,
) error {
	return h.record(&hookCall{name: "AfterDeleteCommentsForTarget", scope: scope, target: target, deleted: deleted})
}

func (h *recordingHooks) AfterDeleteCommentsByAuthor(
	_ context.Context,
	_ database.Tx,
	scope tenancy.Scope,
	author string,
	deleted int64,
) error {
	return h.record(&hookCall{name: "AfterDeleteCommentsByAuthor", scope: scope, author: author, deleted: deleted})
}

// last is the most recent call, failing the test when there was none.
func (h *recordingHooks) last(tb testing.TB) hookCall {
	tb.Helper()

	must.SliceNotEmpty(tb, h.calls)

	return h.calls[len(h.calls)-1]
}

// eraseAuthor runs DeleteCommentsByAuthor in a transaction of its own and hands
// back what it reported.
func eraseAuthor(t *testing.T, env *storeEnv, store *SQLStore, scope tenancy.Scope, author string) (int64, error) {
	t.Helper()

	var deleted int64

	err := env.inTx(t, func(tx database.Tx) error {
		var txErr error
		deleted, txErr = store.DeleteCommentsByAuthor(t.Context(), tx, scope, author)

		return txErr
	})

	return deleted, err
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

		root := written(t, env, store, newComment(testAuthor, "first draft"))
		call := hooks.last(t)
		test.EqOp(t, "AfterCreateComment", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, root, call.comment)
		test.False(t, call.comment.CreatedAt.IsZero())

		child := written(t, env, store, reply(root.ID, otherAuthor, "a reply"))
		call = hooks.last(t)
		test.EqOp(t, "AfterCreateComment", call.name)
		test.EqOp(t, child, call.comment)
		test.EqOp(t, testTarget, call.comment.Target)

		edit := *root
		edit.Body = "second draft"
		revised, err := env.update(t, store, testScope, &edit)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterUpdateComment", call.name)
		test.EqOp(t, revised, call.comment)
		test.EqOp(t, "second draft", call.comment.Body)
		test.NotNil(t, call.comment.LastUpdatedAt)
		must.NotNil(t, call.before)
		test.EqOp(t, root.ID, call.before.ID)
		test.EqOp(t, "first draft", call.before.Body)
		test.Nil(t, call.before.LastUpdatedAt)

		hidden, err := env.archive(t, store, testScope, child.ID)
		must.NoError(t, err)
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveComment", call.name)
		test.EqOp(t, hidden, call.comment)
		test.EqOp(t, "a reply", call.comment.Body)
		test.NotNil(t, call.comment.ArchivedAt)

		test.EqOp(t, int64(2), sweep(t, env, store, testScope, testTarget))
		call = hooks.last(t)
		test.EqOp(t, "AfterDeleteCommentsForTarget", call.name)
		test.EqOp(t, testTarget, call.target)
		test.EqOp(t, int64(2), call.deleted)

		test.SliceLen(t, 5, hooks.calls)
	})

	t.Run("an erasure's hook is called with the count, zero included", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		written(t, env, store, newComment(testAuthor, "mine"))
		written(t, env, store, newComment(otherAuthor, "theirs"))

		for _, want := range []int64{1, 0} {
			deleted, err := eraseAuthor(t, env, store, testScope, testAuthor)
			must.NoError(t, err)
			test.EqOp(t, want, deleted)

			call := hooks.last(t)
			test.EqOp(t, "AfterDeleteCommentsByAuthor", call.name)
			test.EqOp(t, testScope, call.scope)
			test.EqOp(t, testAuthor, call.author)
			test.EqOp(t, want, call.deleted)
		}
	})

	t.Run("a sweep's hook is called with every row the statement removed", func(t *testing.T) {
		t.Parallel()

		// The count is the DELETE's own, so it covers archived rows and replies
		// alike — not a page of them read beforehand.
		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		root := written(t, env, store, newComment(testAuthor, "root"))
		written(t, env, store, reply(root.ID, otherAuthor, "child"))
		gone := written(t, env, store, newComment(testAuthor, "already archived"))
		must.NoError(t, env.archiveErr(t, store, testScope, gone.ID))

		test.EqOp(t, int64(3), sweep(t, env, store, testScope, testTarget))

		call := hooks.last(t)
		test.EqOp(t, "AfterDeleteCommentsForTarget", call.name)
		test.EqOp(t, int64(3), call.deleted)
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		root := written(t, env, store, newComment(testAuthor, "root"))
		child := written(t, env, store, reply(root.ID, otherAuthor, "child"))
		before := len(hooks.calls)

		// A reply to a reply: the thread-depth check refuses it.
		must.ErrorIs(t, env.createErr(t, store, testScope, reply(child.ID, testAuthor, "too deep")), ErrNestedReply)

		missing := newComment(testAuthor, "edited")
		missing.ID = "nonexistent"
		must.ErrorIs(t, env.updateErr(t, store, testScope, missing), ErrCommentNotFound)

		must.NoError(t, env.archiveErr(t, store, testScope, child.ID))
		before++

		must.ErrorIs(t, env.archiveErr(t, store, testScope, child.ID), ErrCommentNotFound)

		_, err := eraseAuthor(t, env, store, testScope, "")
		must.ErrorIs(t, err, ErrEmptyAuthor)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the write's, and the write rolls back with it", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterCreateComment"}
		store := env.newStore(t, WithHooks(hooks))

		created, err := env.create(t, store, testScope, newComment(testAuthor, "never lands"))
		must.ErrorIs(t, err, errHook)
		test.Nil(t, created)

		page, err := store.ListCommentsByAuthor(t.Context(), env.reader(), testScope, testAuthor, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)
	})

	t.Run("an edit's hook error is the edit's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterUpdateComment"}
		store := env.newStore(t, WithHooks(hooks))

		c := written(t, env, store, newComment(testAuthor, "original"))

		edit := *c
		edit.Body = "rewritten"
		revised, err := env.update(t, store, testScope, &edit)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, revised)

		read, err := store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
		test.EqOp(t, "original", read.Body)
	})

	t.Run("an archive's hook error is the archive's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterArchiveComment"}
		store := env.newStore(t, WithHooks(hooks))

		c := written(t, env, store, newComment(testAuthor, "stays"))

		hidden, err := env.archive(t, store, testScope, c.ID)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, hidden)

		_, err = store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
	})

	t.Run("an erasure's hook error is the erasure's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterDeleteCommentsByAuthor"}
		store := env.newStore(t, WithHooks(hooks))

		c := written(t, env, store, newComment(testAuthor, "survives"))

		deleted, err := eraseAuthor(t, env, store, testScope, testAuthor)
		must.ErrorIs(t, err, errHook)
		test.EqOp(t, int64(0), deleted)

		_, err = store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
	})

	t.Run("a sweep's hook error is the sweep's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterDeleteCommentsForTarget"}
		store := env.newStore(t, WithHooks(hooks))

		c := written(t, env, store, newComment(testAuthor, "survives"))

		err := env.inTx(t, func(tx database.Tx) error {
			_, txErr := store.DeleteCommentsForTarget(t.Context(), tx, testScope, testTarget)

			return txErr
		})
		must.ErrorIs(t, err, errHook)

		_, err = store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
	})

	t.Run("nil hooks are no hooks", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t, WithHooks(nil))

		c := written(t, env, store, newComment(testAuthor, "fine"))

		edit := *c
		edit.Body = "still fine"
		_, err := env.update(t, store, testScope, &edit)
		must.NoError(t, err)
	})

	t.Run("an update reads its before row only for hooks", func(t *testing.T) {
		t.Parallel()

		// It runs one edit on a fresh store and reports how many statements it
		// sent. NoopHooks is installed hooks as far as the store can tell, so the
		// difference is the before read and nothing else.
		updateComment := func(t *testing.T, opts ...SQLStoreOption) int64 {
			t.Helper()

			store := env.newStore(t, opts...)
			c := written(t, env, store, newComment(testAuthor, "fine"))
			edit := *c
			edit.Body = "still fine"

			return countStatements(t, env, func(tx database.Tx) error {
				_, err := store.UpdateComment(t.Context(), tx, testScope, &edit)
				return err
			})
		}

		test.Less(t, updateComment(t, WithHooks(NoopHooks{})), updateComment(t), test.Sprint("UpdateComment"))
	})
}

// countStatements runs fn in a transaction of its own and reports how many
// statements it sent through it.
func countStatements(tb testing.TB, env *storeEnv, fn func(tx database.Tx) error) int64 {
	tb.Helper()

	var counted *txcount.Tx

	must.NoError(tb, env.inTx(tb, func(tx database.Tx) error {
		counted = txcount.Wrap(tx)

		return fn(counted)
	}))

	return counted.Statements()
}
