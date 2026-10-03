package mediaregistry

import (
	"context"
	"testing"

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
	object   *Object
	name     string
	ownerID  string
	scope    tenancy.Scope
	archived int64
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

func (h *recordingHooks) AfterRecordObject(_ context.Context, _ database.Tx, scope tenancy.Scope, object *Object) error {
	return h.record(&hookCall{name: "AfterRecordObject", scope: scope, object: object})
}

func (h *recordingHooks) AfterArchiveObject(_ context.Context, _ database.Tx, scope tenancy.Scope, object *Object) error {
	return h.record(&hookCall{name: "AfterArchiveObject", scope: scope, object: object})
}

func (h *recordingHooks) AfterArchiveObjectsForOwner(_ context.Context, _ database.Tx, scope tenancy.Scope, ownerID string, archived int64) error {
	return h.record(&hookCall{name: "AfterArchiveObjectsForOwner", scope: scope, ownerID: ownerID, archived: archived})
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

		recorded := env.mustRecord(t, store, testScope, newInput("avatars/ada.png", "user_1"))
		call := hooks.last(t)
		test.EqOp(t, "AfterRecordObject", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, recorded, call.object)
		test.False(t, call.object.CreatedAt.IsZero())

		archived := env.mustArchive(t, store, testScope, recorded.ID)
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveObject", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, archived, call.object)
		test.EqOp(t, "avatars/ada.png", call.object.Key)
		test.NotNil(t, call.object.ArchivedAt)

		test.SliceLen(t, 2, hooks.calls)
	})

	t.Run("an erasure's hook is called with the owner and the count, zero included", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		env.mustRecord(t, store, testScope, newInput("a.png", "user_1"))
		env.mustRecord(t, store, testScope, newInput("b.png", "user_1"))

		for _, want := range []int64{2, 0} {
			got, err := env.archiveForOwner(t, store, testScope, "user_1")
			must.NoError(t, err)
			test.EqOp(t, want, got)

			call := hooks.last(t)
			test.EqOp(t, "AfterArchiveObjectsForOwner", call.name)
			test.EqOp(t, testScope, call.scope)
			test.EqOp(t, "user_1", call.ownerID)
			test.EqOp(t, want, call.archived)
		}
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStore(t, WithHooks(hooks))

		env.mustRecord(t, store, testScope, newInput("taken.png", "user_1"))
		before := len(hooks.calls)

		_, err := env.record(t, store, testScope, newInput("taken.png", "user_2"))
		must.ErrorIs(t, err, ErrObjectKeyTaken)

		_, err = env.archive(t, store, testScope, identifiers.New())
		must.ErrorIs(t, err, ErrObjectNotFound)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the write's, and the write rolls back with it", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterRecordObject"}
		store := env.newStore(t, WithHooks(hooks))

		recorded, err := env.record(t, store, testScope, newInput("avatars/ada.png", "user_1"))
		must.ErrorIs(t, err, errHook)
		test.Nil(t, recorded)

		_, err = store.GetObjectByKey(t.Context(), env.reader(), testScope, "avatars/ada.png")
		must.ErrorIs(t, err, ErrObjectNotFound)
	})

	t.Run("an archive's hook error leaves the row live", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterArchiveObject"}
		store := env.newStore(t, WithHooks(hooks))

		recorded := env.mustRecord(t, store, testScope, newInput("avatars/ada.png", "user_1"))

		archived, err := env.archive(t, store, testScope, recorded.ID)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, archived)

		read, err := store.GetObject(t.Context(), env.reader(), testScope, recorded.ID)
		must.NoError(t, err)
		test.Nil(t, read.ArchivedAt)
	})

	t.Run("an erasure's hook error is the erasure's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterArchiveObjectsForOwner"}
		store := env.newStore(t, WithHooks(hooks))

		recorded := env.mustRecord(t, store, testScope, newInput("avatars/ada.png", "user_1"))

		archived, err := env.archiveForOwner(t, store, testScope, "user_1")
		must.ErrorIs(t, err, errHook)
		test.EqOp(t, int64(0), archived)

		_, err = store.GetObject(t.Context(), env.reader(), testScope, recorded.ID)
		must.NoError(t, err)
	})

	t.Run("nil hooks are no hooks", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t, WithHooks(nil))

		env.mustRecord(t, store, testScope, newInput("a.png", "user_1"))
	})
}
