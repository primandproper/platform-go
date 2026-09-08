package settings

import (
	"testing"

	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runValueSuite is the request-path half: what a subject can store, and what the
// store refuses on their behalf.
func runValueSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("set and read back a value", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		definition := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		written, err := env.set(t, store, testScope, testSubject, "digest", "daily")
		must.NoError(t, err)
		test.NotEqOp(t, "", written.ID)
		test.EqOp(t, definition.ID, written.DefinitionID)
		test.EqOp(t, "daily", written.Raw)
		test.EqOp(t, testSubject, written.Subject)
		test.EqOp(t, testScope, written.Scope)
		test.False(t, written.CreatedAt.IsZero())

		read, err := store.GetValue(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, written.ID, read.ID)
		test.EqOp(t, "daily", read.Raw)
	})

	t.Run("a second write converges on the row the first wrote", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))

		first, err := env.set(t, store, testScope, testSubject, "digest", "daily")
		must.NoError(t, err)
		test.Nil(t, first.LastUpdatedAt)

		second, err := env.set(t, store, testScope, testSubject, "digest", "never")
		must.NoError(t, err)

		// The same row: the quadruple is unique, so the id and the creation time
		// are the first write's and the answer is the second's.
		test.EqOp(t, first.ID, second.ID)
		test.EqOp(t, first.CreatedAt, second.CreatedAt)
		test.EqOp(t, "never", second.Raw)
		test.NotNil(t, second.LastUpdatedAt)

		page, err := store.ListValuesForSubject(t.Context(), env.reader(), testScope, testSubject, nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, page.Data)
	})

	t.Run("clearing takes the answer back and setting it again revives the row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))

		first, err := env.set(t, store, testScope, testSubject, "digest", "daily")
		must.NoError(t, err)

		must.NoError(t, env.clear(t, store, testScope, testSubject, "digest"))

		_, err = store.GetValue(t.Context(), env.reader(), testScope, testSubject, "digest")
		test.ErrorIs(t, err, ErrValueNotFound)

		// Clearing twice is not a second clearance: the row is already archived,
		// so the guarded write touches nothing and says so.
		test.ErrorIs(t, env.clear(t, store, testScope, testSubject, "digest"), ErrValueNotFound)

		revived, err := env.set(t, store, testScope, testSubject, "digest", "weekly")
		must.NoError(t, err)
		test.EqOp(t, first.ID, revived.ID)
		test.EqOp(t, first.CreatedAt, revived.CreatedAt)
		test.Nil(t, revived.ArchivedAt)
		test.EqOp(t, "weekly", revived.Raw)
	})

	t.Run("a value is checked against its definition", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))
		mustCreate(t, env, store, testScope, intDefinition("retention.days"))
		mustCreate(t, env, store, testScope, boolDefinition("compact"))

		_, err := env.set(t, store, testScope, testSubject, "digest", "hourly")
		test.ErrorIs(t, err, ErrNotEnumerated)

		_, err = env.set(t, store, testScope, testSubject, "retention.days", "quite a few")
		test.ErrorIs(t, err, ErrMalformedValue)

		_, err = env.set(t, store, testScope, testSubject, "compact", "yes")
		test.ErrorIs(t, err, ErrMalformedValue)

		// A setting that enumerates nothing admits any value of its kind.
		stored, err := env.set(t, store, testScope, testSubject, "retention.days", "30")
		must.NoError(t, err)
		test.EqOp(t, "30", stored.Raw)
	})

	t.Run("a value needs a definition", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := env.set(t, store, testScope, testSubject, "never.defined", "1")
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		_, err = store.GetValue(t.Context(), env.reader(), testScope, testSubject, "never.defined")
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		test.ErrorIs(t, env.clear(t, store, testScope, testSubject, "never.defined"), ErrDefinitionNotFound)

		// An archived definition is not a definition a value can be written
		// against: the read that every write begins with excludes it.
		archived := mustCreate(t, env, store, testScope, boolDefinition("retired"))
		must.NoError(t, env.archive(t, store, testScope, archived.ID))

		_, err = env.set(t, store, testScope, testSubject, "retired", "true")
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("a subject has to name somebody", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, boolDefinition("compact"))

		_, err := env.set(t, store, testScope, Subject{ID: "user-1"}, "compact", "true")
		test.ErrorIs(t, err, ErrEmptySubjectType)

		_, err = env.set(t, store, testScope, Subject{Type: SubjectUser}, "compact", "true")
		test.ErrorIs(t, err, ErrEmptySubjectID)

		var unset tenancy.Scope

		_, err = env.set(t, store, unset, testSubject, "compact", "true")
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	t.Run("one subject's answer is not another's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))

		second := Subject{Type: SubjectUser, ID: "user-2"}
		account := Subject{Type: SubjectAccount, ID: "user-1"}

		_, err := env.set(t, store, testScope, testSubject, "digest", "daily")
		must.NoError(t, err)

		_, err = store.GetValue(t.Context(), env.reader(), testScope, second, "digest")
		test.ErrorIs(t, err, ErrValueNotFound)

		// The subject type is half the key, so an account whose id happens to
		// match a user's is a different subject.
		_, err = store.GetValue(t.Context(), env.reader(), testScope, account, "digest")
		test.ErrorIs(t, err, ErrValueNotFound)

		// And the scope is keyed on too, in a table whose rows the other scope's
		// definition does not even name.
		_, err = store.GetValue(t.Context(), env.reader(), otherScope, testSubject, "digest")
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("values page for a subject and for a definition", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))
		mustCreate(t, env, store, testScope, boolDefinition("compact"))

		second := Subject{Type: SubjectUser, ID: "user-2"}

		_, err := env.set(t, store, testScope, testSubject, "digest", "daily")
		must.NoError(t, err)
		_, err = env.set(t, store, testScope, testSubject, "compact", "true")
		must.NoError(t, err)
		_, err = env.set(t, store, testScope, second, "digest", "never")
		must.NoError(t, err)

		mine, err := store.ListValuesForSubject(t.Context(), env.reader(), testScope, testSubject, nil)
		must.NoError(t, err)
		test.SliceLen(t, 2, mine.Data)

		everyone, err := store.ListValuesForDefinition(t.Context(), env.reader(), testScope, "digest", nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, everyone.Data)

		descending := filtering.DefaultQueryFilter()
		descending.SortBy = filtering.SortDescending

		reversed, err := store.ListValuesForDefinition(t.Context(), env.reader(), testScope, "digest", descending)
		must.NoError(t, err)
		must.SliceLen(t, 2, reversed.Data)
		test.EqOp(t, everyone.Data[0].ID, reversed.Data[1].ID)
		test.EqOp(t, everyone.Data[1].ID, reversed.Data[0].ID)

		// A cleared answer leaves the page, which is what makes the page the set
		// of live overrides rather than the set of rows.
		must.NoError(t, env.clear(t, store, testScope, second, "digest"))

		live, err := store.ListValuesForDefinition(t.Context(), env.reader(), testScope, "digest", nil)
		must.NoError(t, err)
		test.SliceLen(t, 1, live.Data)

		filter := filtering.DefaultQueryFilter()
		filter.IncludeArchived = pointer.To(true)

		all, err := store.ListValuesForDefinition(t.Context(), env.reader(), testScope, "digest", filter)
		must.NoError(t, err)
		test.SliceLen(t, 2, all.Data)
	})

	t.Run("an edit that would strand a stored value is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		definition := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		_, err := env.set(t, store, testScope, testSubject, "digest", "daily")
		must.NoError(t, err)

		// Narrowing the enumeration out from under a stored value.
		narrowed := *definition
		narrowed.Enumeration = []string{"weekly", "never"}

		err = env.update(t, store, testScope, &narrowed)
		test.ErrorIs(t, err, ErrStrandedValues)

		// The refusal names the subject and the value, because clearing or
		// migrating them is what has to happen next.
		test.StrContains(t, err.Error(), "user-1")
		test.StrContains(t, err.Error(), "daily")

		// Nothing was written: the walk and the write share a transaction.
		unchanged, err := store.GetDefinition(t.Context(), env.reader(), testScope, definition.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"daily", "never", "weekly"}, unchanged.Enumeration)
		test.Nil(t, unchanged.LastUpdatedAt)

		// Changing the kind under one is refused the same way.
		retyped := *definition
		retyped.Kind = KindInt
		retyped.Default = nil
		retyped.Enumeration = nil

		test.ErrorIs(t, env.update(t, store, testScope, &retyped), ErrStrandedValues)

		// Widening is not stranding, and neither is an edit that leaves the kind
		// and the enumeration alone.
		widened := *definition
		widened.Enumeration = []string{"weekly", "daily", "never", "hourly"}
		test.NoError(t, env.update(t, store, testScope, &widened))

		// Once the value is cleared, the narrowing goes through: a cleared value
		// resolves to the default rather than to itself.
		must.NoError(t, env.clear(t, store, testScope, testSubject, "digest"))

		narrowedAgain := *definition
		narrowedAgain.Enumeration = []string{"weekly", "never"}
		test.NoError(t, env.update(t, store, testScope, &narrowedAgain))
	})

	t.Run("another scope's values do not block an edit", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := mustCreate(t, env, store, testScope, stringDefinition("digest"))
		theirs := mustCreate(t, env, store, otherScope, stringDefinition("digest"))

		_, err := env.set(t, store, otherScope, testSubject, "digest", "daily")
		must.NoError(t, err)

		// The walk is keyed on the scope like every other read here, so the
		// other tenant's answer is not a reason to refuse this tenant's edit.
		narrowed := *mine
		narrowed.Enumeration = []string{"weekly", "never"}
		test.NoError(t, env.update(t, store, testScope, &narrowed))

		// And the other tenant's own edit is still refused by their own value.
		theirNarrowed := *theirs
		theirNarrowed.Enumeration = []string{"weekly", "never"}
		test.ErrorIs(t, env.update(t, store, otherScope, &theirNarrowed), ErrStrandedValues)
	})
}
