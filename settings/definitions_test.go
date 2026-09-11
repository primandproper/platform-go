package settings

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runDefinitionSuite is the catalog half: what an administrator can write, and
// what the store refuses.
func runDefinitionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("create and read back a definition", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		test.NotEqOp(t, "", created.ID)
		test.False(t, created.CreatedAt.IsZero())
		test.EqOp(t, testScope, created.Scope)

		// Sorted, because an enumeration is a set and the read hands it back in
		// the option's own order. A caller holding the value it just wrote and a
		// caller re-reading it see the same slice.
		test.Eq(t, []string{"daily", "never", "weekly"}, created.Enumeration)

		read, err := store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, created.ID, read.ID)
		test.EqOp(t, "digest", read.Name)
		test.EqOp(t, KindString, read.Kind)
		test.EqOp(t, "weekly", pointer.Dereference(read.Default))
		test.Eq(t, []string{"daily", "never", "weekly"}, read.Enumeration)
		test.Nil(t, read.LastUpdatedAt)
		test.Nil(t, read.ArchivedAt)

		byName, err := store.GetDefinitionByName(t.Context(), env.reader(), testScope, "digest")
		must.NoError(t, err)
		test.EqOp(t, created.ID, byName.ID)
		test.Eq(t, read.Enumeration, byName.Enumeration)
	})

	t.Run("a definition with no enumeration comes back with an empty one", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		created := mustCreate(t, env, store, testScope, intDefinition("retention.days"))

		// Empty rather than nil: a nil enumeration is indistinguishable from one
		// nothing attached, and that reading admits every value.
		test.NotNil(t, created.Enumeration)
		test.SliceEmpty(t, created.Enumeration)

		read, err := store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.NotNil(t, read.Enumeration)
		test.SliceEmpty(t, read.Enumeration)
	})

	t.Run("a name is taken once per scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mustCreate(t, env, store, testScope, stringDefinition("digest"))

		_, err := env.create(t, store, testScope, stringDefinition("digest"))
		test.ErrorIs(t, err, ErrDefinitionNameTaken)

		// The other scope's catalog is its own.
		other, err := env.create(t, store, otherScope, stringDefinition("digest"))
		must.NoError(t, err)
		test.EqOp(t, otherScope, other.Scope)
	})

	t.Run("archiving does not free the name", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))
		mustArchive(t, env, store, testScope, created.ID)

		_, err := store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		// The values written under the name are still interpreted against this
		// definition, so a second definition must not be able to claim it.
		_, err = env.create(t, store, testScope, stringDefinition("digest"))
		test.ErrorIs(t, err, ErrDefinitionNameTaken)
	})

	t.Run("a definition is refused before it is written", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		cases := map[string]struct {
			definition *Definition
			sentinel   error
		}{
			"no name":                 {&Definition{Kind: KindBool}, ErrEmptyDefinitionName},
			"unknown kind":            {&Definition{Name: "a", Kind: "date"}, ErrUnknownKind},
			"default of another kind": {&Definition{Name: "b", Kind: KindInt, Default: pointer.To("soon")}, ErrMalformedValue},
			"default outside the enumeration": {
				&Definition{Name: "c", Kind: KindString, Default: pointer.To("hourly"), Enumeration: []string{"daily"}},
				ErrNotEnumerated,
			},
			"empty enumeration value": {
				&Definition{Name: "d", Kind: KindString, Enumeration: []string{"daily", ""}},
				ErrEmptyEnumerationValue,
			},
			"repeated enumeration value": {
				&Definition{Name: "e", Kind: KindString, Enumeration: []string{"daily", "daily"}},
				ErrDuplicateEnumerationValue,
			},
			"enumeration value of another kind": {
				&Definition{Name: "f", Kind: KindInt, Enumeration: []string{"1", "some"}},
				ErrMalformedValue,
			},
		}

		for name, c := range cases {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				created, err := env.create(t, store, testScope, c.definition)
				test.Nil(t, created)
				test.ErrorIs(t, err, c.sentinel)
			})
		}
	})

	t.Run("a nil definition is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := env.create(t, store, testScope, nil)
		test.ErrorIs(t, err, ErrNilDefinition)

		_, err = env.update(t, store, testScope, nil)
		test.ErrorIs(t, err, ErrNilDefinition)
	})

	t.Run("an unset scope reaches no statement", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The zero Scope is the absence of a decision rather than the global
		// scope, so every entry point rejects it before it reaches a statement.
		var unset tenancy.Scope

		_, err := env.create(t, store, unset, stringDefinition("digest"))
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.GetDefinition(t.Context(), env.reader(), unset, "whatever")
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		_, err = store.ListDefinitions(t.Context(), env.reader(), unset, nil)
		test.ErrorIs(t, err, tenancy.ErrNoScope)

		err = env.archive(t, store, unset, "whatever")
		test.ErrorIs(t, err, tenancy.ErrNoScope)
	})

	t.Run("reads are keyed on the scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		_, err := store.GetDefinition(t.Context(), env.reader(), otherScope, created.ID)
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		_, err = store.GetDefinitionByName(t.Context(), env.reader(), otherScope, "digest")
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		err = env.archive(t, store, otherScope, created.ID)
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("the catalog pages in both directions", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := mustCreate(t, env, store, testScope, stringDefinition("a.digest"))
		second := mustCreate(t, env, store, testScope, boolDefinition("b.compact"))
		third := mustCreate(t, env, store, testScope, intDefinition("c.retention"))

		page, err := store.ListDefinitions(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		must.SliceLen(t, 3, page.Data)
		test.EqOp(t, first.ID, page.Data[0].ID)
		test.EqOp(t, third.ID, page.Data[2].ID)

		// The enumeration comes back on a listed definition too, in one batched
		// read for the whole page.
		test.Eq(t, []string{"daily", "never", "weekly"}, page.Data[0].Enumeration)
		test.SliceEmpty(t, page.Data[2].Enumeration)

		counts, total, known := page.Counts()
		test.True(t, known)
		test.EqOp(t, uint64(3), counts)
		test.EqOp(t, uint64(3), total)

		descending := filtering.DefaultQueryFilter()
		descending.SortBy = filtering.SortDescending

		reversed, err := store.ListDefinitions(t.Context(), env.reader(), testScope, descending)
		must.NoError(t, err)
		must.SliceLen(t, 3, reversed.Data)
		test.EqOp(t, third.ID, reversed.Data[0].ID)
		test.EqOp(t, second.ID, reversed.Data[1].ID)
		test.EqOp(t, first.ID, reversed.Data[2].ID)
	})

	t.Run("an archived definition is out of the page unless asked for", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		live := mustCreate(t, env, store, testScope, boolDefinition("live"))
		retired := mustCreate(t, env, store, testScope, boolDefinition("retired"))
		mustArchive(t, env, store, testScope, retired.ID)

		page, err := store.ListDefinitions(t.Context(), env.reader(), testScope, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, live.ID, page.Data[0].ID)

		filter := filtering.DefaultQueryFilter()
		filter.IncludeArchived = pointer.To(true)

		all, err := store.ListDefinitions(t.Context(), env.reader(), testScope, filter)
		must.NoError(t, err)
		test.SliceLen(t, 2, all.Data)
	})

	t.Run("an update rewrites what it is handed", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		created.Name = "digest.frequency"
		created.Description = "reworded"
		created.Default = pointer.To("never")
		created.AdminOnly = true
		created.Enumeration = []string{"weekly", "daily", "never", "hourly"}

		mustUpdate(t, env, store, testScope, created)

		read, err := store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, "digest.frequency", read.Name)
		test.EqOp(t, "reworded", read.Description)
		test.EqOp(t, "never", pointer.Dereference(read.Default))
		test.True(t, read.AdminOnly)
		test.Eq(t, []string{"daily", "hourly", "never", "weekly"}, read.Enumeration)
		test.NotNil(t, read.LastUpdatedAt)

		// The name it was renamed away from is free again, because uniqueness is
		// on the row rather than on the history.
		_, err = env.create(t, store, testScope, stringDefinition("digest"))
		test.NoError(t, err)
	})

	t.Run("an update answers with the definition it moved", func(t *testing.T) {
		t.Parallel()

		// The return is the point: a caller writing an audit entry beside the
		// edit describes the row this statement left rather than the row a read
		// a statement earlier found, and inside an uncommitted transaction there
		// is no other way to read it back.
		store := env.newStore(t)

		created := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		edit := *created
		edit.Name = "digest.frequency"
		edit.Description = "reworded"
		edit.Default = pointer.To("never")
		edit.AdminOnly = true
		edit.Enumeration = []string{"weekly", "daily", "never", "hourly"}

		updated := mustUpdate(t, env, store, testScope, &edit)

		test.EqOp(t, created.ID, updated.ID)
		test.EqOp(t, "digest.frequency", updated.Name)
		test.EqOp(t, "reworded", updated.Description)
		test.EqOp(t, "never", pointer.Dereference(updated.Default))
		test.True(t, updated.AdminOnly)
		test.EqOp(t, testScope, updated.Scope)

		// Sorted, because that is how the row comes back and how the next read
		// will hand it over.
		test.Eq(t, []string{"daily", "hourly", "never", "weekly"}, updated.Enumeration)

		// The stamp is the server's, and it is the field a response assembled
		// from the request would get wrong.
		test.NotNil(t, updated.LastUpdatedAt)
		test.Nil(t, updated.ArchivedAt)

		// What the caller handed over is left alone. Returning the row and
		// mutating the argument are two spellings of one guarantee, and this
		// module writes the first.
		test.Nil(t, edit.LastUpdatedAt)
		test.Eq(t, []string{"weekly", "daily", "never", "hourly"}, edit.Enumeration)

		// And it is the row a later read finds, rather than a value assembled on
		// the way out.
		read, err := store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, read.Name, updated.Name)
		test.Eq(t, read.Enumeration, updated.Enumeration)
	})

	t.Run("an archive answers with an error alone, and the row is readable until it runs", func(t *testing.T) {
		t.Parallel()

		// The write that does not return, and the case that says why it does not
		// have to: a caller recording what it retired reads the definition on
		// the same transaction, before the archive, and gets everything a
		// read-back here would have given it. What it does not get is the
		// retirement stamp, which is the server's — and that is the trade, paid
		// only by the callers who want the row.
		store := env.newStore(t)

		created := mustCreate(t, env, store, testScope,
			&Definition{Name: "digest", Kind: KindString, Enumeration: []string{"daily", "weekly"}})

		var read *Definition

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var readErr error
			if read, readErr = store.GetDefinition(t.Context(), tx, testScope, created.ID); readErr != nil {
				return readErr
			}

			return store.ArchiveDefinition(t.Context(), tx, testScope, created.ID)
		}))

		must.NotNil(t, read)
		test.EqOp(t, created.ID, read.ID)
		test.EqOp(t, "digest", read.Name)
		test.EqOp(t, KindString, read.Kind)
		test.EqOp(t, testScope, read.Scope)
		test.Eq(t, []string{"daily", "weekly"}, read.Enumeration)

		// After the commit the definition is out of the catalog, which is what
		// the archive was for.
		_, err := store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("a refused write answers with no row at all", func(t *testing.T) {
		t.Parallel()

		// The row is returned only alongside a nil error, so nothing here has a
		// state in which a caller holds half an answer.
		store := env.newStore(t)

		created := mustCreate(t, env, store, testScope, boolDefinition("compact"))
		mustArchive(t, env, store, testScope, created.ID)

		// Archiving what is already archived is the guard matching nothing. The
		// archive returns no row to be nil, so what it owes is the sentinel.
		test.ErrorIs(t, env.archive(t, store, testScope, created.ID), ErrDefinitionNotFound)
		test.ErrorIs(t, env.archive(t, store, testScope, "no-such-row"), ErrDefinitionNotFound)

		unedited, err := env.update(t, store, testScope,
			&Definition{ID: "no-such-row", Name: "a", Kind: KindBool})
		test.ErrorIs(t, err, ErrDefinitionNotFound)
		test.Nil(t, unedited)
	})

	t.Run("an update refuses a name another definition holds", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mustCreate(t, env, store, testScope, boolDefinition("taken"))
		other := mustCreate(t, env, store, testScope, boolDefinition("free"))

		other.Name = "taken"
		_, err := env.update(t, store, testScope, other)
		test.ErrorIs(t, err, ErrDefinitionNameTaken)

		// Saving a definition under its own name is not a collision with itself.
		other.Name = "free"
		_, err = env.update(t, store, testScope, other)
		test.NoError(t, err)
	})

	t.Run("an update needs a definition to update", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := env.update(t, store, testScope, &Definition{Name: "a", Kind: KindBool})
		test.ErrorIs(t, err, platformerrors.ErrInvalidIDProvided)

		_, err = env.update(t, store, testScope, &Definition{ID: "no-such-row", Name: "a", Kind: KindBool})
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("an archive needs a definition to archive", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		test.ErrorIs(t, env.archive(t, store, testScope, "no-such-row"), ErrDefinitionNotFound)
	})
}
