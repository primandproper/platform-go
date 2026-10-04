package settings

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/internal/txcount"

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
	definition       *Definition
	beforeDefinition *Definition
	value            *Value
	beforeValue      *Value
	subject          Subject
	name             string
	scope            tenancy.Scope
	deleted          int64
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

func (h *recordingHooks) AfterCreateDefinition(_ context.Context, _ database.Tx, scope tenancy.Scope, definition *Definition) error {
	return h.record(&hookCall{name: "AfterCreateDefinition", scope: scope, definition: definition})
}

func (h *recordingHooks) AfterUpdateDefinition(_ context.Context, _ database.Tx, scope tenancy.Scope, before, after *Definition) error {
	return h.record(&hookCall{name: "AfterUpdateDefinition", scope: scope, definition: after, beforeDefinition: before})
}

func (h *recordingHooks) AfterArchiveDefinition(_ context.Context, _ database.Tx, scope tenancy.Scope, definition *Definition) error {
	return h.record(&hookCall{name: "AfterArchiveDefinition", scope: scope, definition: definition})
}

func (h *recordingHooks) AfterSetValue(
	_ context.Context,
	_ database.Tx,
	scope tenancy.Scope,
	definition *Definition,
	before, after *Value,
) error {
	return h.record(&hookCall{name: "AfterSetValue", scope: scope, definition: definition, value: after, beforeValue: before})
}

func (h *recordingHooks) AfterClearValue(
	_ context.Context,
	_ database.Tx,
	scope tenancy.Scope,
	definition *Definition,
	value *Value,
) error {
	return h.record(&hookCall{name: "AfterClearValue", scope: scope, definition: definition, value: value})
}

func (h *recordingHooks) AfterDeleteValuesForSubject(
	_ context.Context,
	_ database.Tx,
	scope tenancy.Scope,
	subject Subject,
	deleted int64,
) error {
	return h.record(&hookCall{name: "AfterDeleteValuesForSubject", scope: scope, subject: subject, deleted: deleted})
}

// last is the most recent call, failing the test when there was none.
func (h *recordingHooks) last(tb testing.TB) hookCall {
	tb.Helper()

	must.SliceNotEmpty(tb, h.calls)

	return h.calls[len(h.calls)-1]
}

// runHooksSuite is every assertion about the hooks a write runs: that each write
// calls its own, with the row it answers with, that an update is handed what it
// replaced, and that a hook's refusal is the write's.
func runHooksSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("every write calls its hook with the row it answers with", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStoreWithHooks(t, hooks)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))
		call := hooks.last(t)
		test.EqOp(t, "AfterCreateDefinition", call.name)
		test.EqOp(t, testScope, call.scope)
		test.EqOp(t, created, call.definition)
		test.False(t, call.definition.CreatedAt.IsZero())

		edit := *created
		edit.Description = "rewritten"
		edit.Enumeration = []string{"weekly", "daily", "never", "hourly"}
		updated := mustUpdate(t, env, store, testScope, &edit)
		call = hooks.last(t)
		test.EqOp(t, "AfterUpdateDefinition", call.name)
		test.EqOp(t, updated, call.definition)
		test.EqOp(t, "rewritten", call.definition.Description)
		must.NotNil(t, call.beforeDefinition)
		test.EqOp(t, created.ID, call.beforeDefinition.ID)
		test.EqOp(t, "how often a digest is sent", call.beforeDefinition.Description)
		test.Eq(t, []string{"daily", "never", "weekly"}, call.beforeDefinition.Enumeration)
		test.Nil(t, call.beforeDefinition.LastUpdatedAt)

		first := mustSet(t, env, store, testScope, testSubject, "digest", "daily")
		call = hooks.last(t)
		test.EqOp(t, "AfterSetValue", call.name)
		test.EqOp(t, first, call.value)
		test.Nil(t, call.beforeValue)
		test.EqOp(t, "digest", call.definition.Name)

		second := mustSet(t, env, store, testScope, testSubject, "digest", "hourly")
		call = hooks.last(t)
		test.EqOp(t, "AfterSetValue", call.name)
		test.EqOp(t, second, call.value)
		must.NotNil(t, call.beforeValue)
		test.EqOp(t, first.ID, call.beforeValue.ID)
		test.EqOp(t, "daily", call.beforeValue.Raw)

		cleared := mustClear(t, env, store, testScope, testSubject, "digest")
		call = hooks.last(t)
		test.EqOp(t, "AfterClearValue", call.name)
		test.EqOp(t, cleared, call.value)
		test.EqOp(t, "hourly", call.value.Raw)
		test.NotNil(t, call.value.ArchivedAt)
		test.EqOp(t, created.ID, call.definition.ID)

		// A revival is a subject going from no opinion to one.
		mustSet(t, env, store, testScope, testSubject, "digest", "never")
		call = hooks.last(t)
		test.EqOp(t, "AfterSetValue", call.name)
		test.Nil(t, call.beforeValue)
		test.EqOp(t, first.ID, call.value.ID)

		test.EqOp(t, int64(1), env.erase(t, store, testScope, testSubject))
		call = hooks.last(t)
		test.EqOp(t, "AfterDeleteValuesForSubject", call.name)
		test.EqOp(t, testSubject, call.subject)
		test.EqOp(t, int64(1), call.deleted)

		mustArchive(t, env, store, testScope, created.ID)
		call = hooks.last(t)
		test.EqOp(t, "AfterArchiveDefinition", call.name)
		must.NotNil(t, call.definition)
		test.EqOp(t, created.ID, call.definition.ID)
		test.EqOp(t, "digest", call.definition.Name)
		test.Nil(t, call.definition.ArchivedAt)

		test.SliceLen(t, 8, hooks.calls)
	})

	t.Run("an erasure's hook is called with the count, zero included", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStoreWithHooks(t, hooks)

		test.EqOp(t, int64(0), env.erase(t, store, testScope, testSubject))

		call := hooks.last(t)
		test.EqOp(t, "AfterDeleteValuesForSubject", call.name)
		test.EqOp(t, int64(0), call.deleted)
	})

	t.Run("declaring a catalog calls the hooks of the writes it makes", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStoreWithHooks(t, hooks)

		declarations := []Declaration{digestDeclaration("digest")}

		_, err := env.declare(t, store, testScope, declarations)
		must.NoError(t, err)
		test.EqOp(t, "AfterCreateDefinition", hooks.last(t).name)

		// Already matching: nothing written, nothing called.
		_, err = env.declare(t, store, testScope, declarations)
		must.NoError(t, err)
		test.SliceLen(t, 1, hooks.calls)
	})

	t.Run("a refused write calls no hook", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{}
		store := env.newStoreWithHooks(t, hooks)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))
		mustSet(t, env, store, testScope, testSubject, "digest", "daily")
		before := len(hooks.calls)

		_, err := env.create(t, store, testScope, stringDefinition("digest"))
		must.ErrorIs(t, err, ErrDefinitionNameTaken)

		_, err = env.set(t, store, testScope, testSubject, "digest", "fortnightly")
		must.ErrorIs(t, err, ErrNotEnumerated)

		// Narrowing past a stored value strands it.
		narrowed := *created
		narrowed.Enumeration = []string{"weekly", "never"}
		_, err = env.update(t, store, testScope, &narrowed)
		must.ErrorIs(t, err, ErrStrandedValues)

		_, err = env.clear(t, store, testScope, otherSubject(), "digest")
		must.ErrorIs(t, err, ErrValueNotFound)

		must.ErrorIs(t, env.archive(t, store, otherScope, created.ID), ErrDefinitionNotFound)

		test.SliceLen(t, before, hooks.calls)
	})

	t.Run("a hook's error is the write's, and the write rolls back with it", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterCreateDefinition"}
		store := env.newStoreWithHooks(t, hooks)

		created, err := env.create(t, store, testScope, stringDefinition("digest"))
		must.ErrorIs(t, err, errHook)
		test.Nil(t, created)

		_, err = store.GetDefinitionByName(t.Context(), env.reader(), testScope, "digest")
		must.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("an update's hook error is the update's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterUpdateDefinition"}
		store := env.newStoreWithHooks(t, hooks)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		edit := *created
		edit.Description = "rewritten"
		updated, err := env.update(t, store, testScope, &edit)
		must.ErrorIs(t, err, errHook)
		test.Nil(t, updated)

		read, err := store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, "how often a digest is sent", read.Description)
	})

	t.Run("a value's hook error is the value's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterSetValue"}
		store := env.newStoreWithHooks(t, hooks)

		mustCreate(t, env, store, testScope, stringDefinition("digest"))

		value, err := env.set(t, store, testScope, testSubject, "digest", "daily")
		must.ErrorIs(t, err, errHook)
		test.Nil(t, value)

		_, err = store.GetValue(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.ErrorIs(t, err, ErrValueNotFound)
	})

	t.Run("an archive's hook error is the archive's", func(t *testing.T) {
		t.Parallel()

		hooks := &recordingHooks{failOn: "AfterArchiveDefinition"}
		store := env.newStoreWithHooks(t, hooks)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		must.ErrorIs(t, env.archive(t, store, testScope, created.ID), errHook)

		_, err := store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
	})

	t.Run("nil hooks are refused", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(env.client, nil)
		must.ErrorIs(t, err, ErrNilHooks)
		must.Nil(t, store)
	})

	t.Run("a write reads the row for its hook only when the hooks are not NoopHooks", func(t *testing.T) {
		t.Parallel()

		// Each runs one write on a fresh store and reports how many statements it
		// sent. installedHooks adds nothing to NoopHooks but is not NoopHooks, so the
		// difference is the read for the hook and nothing else.
		archive := func(t *testing.T, hooks Hooks) int64 {
			t.Helper()

			store := env.newStoreWithHooks(t, hooks)
			created := mustCreate(t, env, store, testScope, stringDefinition("digest"))

			return countStatements(t, env, func(tx database.Tx) error {
				return store.ArchiveDefinition(t.Context(), tx, testScope, created.ID)
			})
		}

		set := func(t *testing.T, hooks Hooks) int64 {
			t.Helper()

			store := env.newStoreWithHooks(t, hooks)
			mustCreate(t, env, store, testScope, stringDefinition("digest"))
			mustSet(t, env, store, testScope, testSubject, "digest", "daily")

			return countStatements(t, env, func(tx database.Tx) error {
				_, err := store.SetValue(t.Context(), tx, testScope, testSubject, "digest", "weekly")
				return err
			})
		}

		test.Less(t, archive(t, installedHooks{}), archive(t, NoopHooks{}), test.Sprint("ArchiveDefinition"))
		test.Less(t, set(t, installedHooks{}), set(t, NoopHooks{}), test.Sprint("SetValue"))
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

// otherSubject is somebody who has answered nothing.
func otherSubject() Subject { return Subject{Type: SubjectUser, ID: "user-nobody"} }

// installedHooks embeds NoopHooks and overrides nothing. It is not NoopHooks,
// so a store built with it is a store with hooks installed, and pays for the
// reads only a hook is handed.
type installedHooks struct{ NoopHooks }
