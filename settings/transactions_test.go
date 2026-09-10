package settings

import (
	"testing"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runTransactionSuite is the commit boundary, which is the whole of what this
// store's signatures buy its caller.
//
// Every write takes the caller's transaction and every read takes an executor, so
// what is under test here is not that the statements work — the other three
// suites cover that — but which side of a commit each of them lands on, and what
// a read handed the transaction can see. Those are the questions a store that
// opened its own transaction answered for its caller, and answered wrong.
func runTransactionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a resolution inside one transaction sees the value that transaction set", func(t *testing.T) {
		t.Parallel()

		// The property the reads were widened for, and the one the resolvers
		// wanted most. A resolution is a definition's default read against a
		// subject's override, so a service that saves somebody's preference and
		// returns the new effective value in the same response is resolving a row
		// it has written and not yet committed. Read on a connection of the
		// store's own it would answer "weekly" — what the subject had before the
		// request — with nothing reporting an error.
		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			written, err := store.SetValue(t.Context(), tx, testScope, testSubject, "digest", "daily")
			if err != nil {
				return err
			}

			resolved, err := store.Resolve(t.Context(), tx, testScope, testSubject, "digest")
			if err != nil {
				return err
			}

			test.EqOp(t, SourceSubject, resolved.Source)
			test.EqOp(t, "daily", resolved.Raw)
			must.NotNil(t, resolved.Value)
			test.EqOp(t, written.ID, resolved.Value.ID)

			// ResolveAll walks the catalog and the subject's answers on the same
			// executor, so it agrees.
			all, err := store.ResolveAll(t.Context(), tx, testScope, testSubject)
			if err != nil {
				return err
			}

			must.SliceLen(t, 1, all)
			test.EqOp(t, SourceSubject, all[0].Source)
			test.EqOp(t, "daily", all[0].Raw)

			// And the same resolution, on the client, cannot see it: the
			// transaction has not committed, so this is the other half of the
			// same fact rather than a second one. It is also exactly the stale
			// answer the old shape had no way to avoid.
			outside, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "digest")
			if err != nil {
				return err
			}

			test.EqOp(t, SourceDefault, outside.Source)
			test.EqOp(t, "weekly", outside.Raw)

			return nil
		}))

		// After the commit both executors agree, which is what makes the reading
		// above about visibility rather than about two different rows.
		resolved, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, SourceSubject, resolved.Source)
		test.EqOp(t, "daily", resolved.Raw)
	})

	t.Run("a value is set against a definition the same transaction created", func(t *testing.T) {
		t.Parallel()

		// The definition read behind the value's write runs on the caller's
		// executor, so a catalog seeded and answered in one transaction resolves
		// instead of reporting the setting undefined.
		store := env.newStore(t)

		var (
			created *Definition
			value   *Value
		)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			var txErr error

			if created, txErr = store.CreateDefinition(t.Context(), tx, testScope, stringDefinition("theme")); txErr != nil {
				return txErr
			}

			value, txErr = store.SetValue(t.Context(), tx, testScope, testSubject, "theme", "never")

			return txErr
		}))

		// The create read its creation time back through the caller's executor,
		// and the value's read-back is the row this transaction wrote rather than
		// a zero time waiting on a commit.
		test.NotEqOp(t, "", created.ID)
		test.False(t, created.CreatedAt.IsZero())
		test.EqOp(t, created.ID, value.DefinitionID)
		test.False(t, value.CreatedAt.IsZero())

		read, err := store.GetValue(t.Context(), env.reader(), testScope, testSubject, "theme")
		must.NoError(t, err)
		test.EqOp(t, "never", read.Raw)
	})

	t.Run("the five writes commit with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		renamed := mustCreate(t, env, store, testScope, boolDefinition("compact"))
		retired := mustCreate(t, env, store, testScope, intDefinition("retention.days"))
		answered := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		mustSet(t, env, store, testScope, testSubject, answered.Name, "daily")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if _, txErr := store.CreateDefinition(t.Context(), tx, testScope, stringDefinition("theme")); txErr != nil {
				return txErr
			}

			if _, txErr := store.SetValue(t.Context(), tx, testScope, testSubject, "theme", "never"); txErr != nil {
				return txErr
			}

			edit := *renamed
			edit.Name = "layout.compact"
			if _, txErr := store.UpdateDefinition(t.Context(), tx, testScope, &edit); txErr != nil {
				return txErr
			}

			if _, txErr := store.ArchiveDefinition(t.Context(), tx, testScope, retired.ID); txErr != nil {
				return txErr
			}

			_, txErr := store.ClearValue(t.Context(), tx, testScope, testSubject, answered.Name)

			return txErr
		}))

		read, err := store.GetValue(t.Context(), env.reader(), testScope, testSubject, "theme")
		must.NoError(t, err)
		test.EqOp(t, "never", read.Raw)

		definition, err := store.GetDefinition(t.Context(), env.reader(), testScope, renamed.ID)
		must.NoError(t, err)
		test.EqOp(t, "layout.compact", definition.Name)

		_, err = store.GetDefinition(t.Context(), env.reader(), testScope, retired.ID)
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		_, err = store.GetValue(t.Context(), env.reader(), testScope, testSubject, answered.Name)
		test.ErrorIs(t, err, ErrValueNotFound)
	})

	t.Run("a rolled back transaction takes all five writes with it", func(t *testing.T) {
		t.Parallel()

		// This is the whole point of the signature, seen from the side that
		// matters: the consumer's companion write fails, and every setting write
		// goes back with it rather than surviving in a transaction it was never
		// part of.
		store := env.newStore(t)

		renamed := mustCreate(t, env, store, testScope, boolDefinition("compact"))
		retired := mustCreate(t, env, store, testScope, intDefinition("retention.days"))
		answered := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		mustSet(t, env, store, testScope, testSubject, answered.Name, "daily")

		var created *Definition

		err := env.inTx(t, func(tx database.Tx) error {
			var txErr error

			if created, txErr = store.CreateDefinition(t.Context(), tx, testScope, stringDefinition("theme")); txErr != nil {
				return txErr
			}

			if _, txErr = store.SetValue(t.Context(), tx, testScope, testSubject, "theme", "never"); txErr != nil {
				return txErr
			}

			edit := *renamed
			edit.Name = "layout.compact"
			if _, txErr = store.UpdateDefinition(t.Context(), tx, testScope, &edit); txErr != nil {
				return txErr
			}

			if _, txErr = store.ArchiveDefinition(t.Context(), tx, testScope, retired.ID); txErr != nil {
				return txErr
			}

			if _, txErr = store.ClearValue(t.Context(), tx, testScope, testSubject, answered.Name); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The id was minted onto the returned value on the way through. Nothing
		// undoes that, and nothing should: what rolled back is the row.
		test.NotEqOp(t, "", created.ID)

		_, err = store.GetDefinition(t.Context(), env.reader(), testScope, created.ID)
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		_, err = store.GetDefinitionByName(t.Context(), env.reader(), testScope, "theme")
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		definition, err := store.GetDefinition(t.Context(), env.reader(), testScope, renamed.ID)
		must.NoError(t, err)
		test.EqOp(t, "compact", definition.Name)
		test.Nil(t, definition.LastUpdatedAt)

		definition, err = store.GetDefinition(t.Context(), env.reader(), testScope, retired.ID)
		must.NoError(t, err)
		test.Nil(t, definition.ArchivedAt)

		read, err := store.GetValue(t.Context(), env.reader(), testScope, testSubject, answered.Name)
		must.NoError(t, err)
		test.EqOp(t, "daily", read.Raw)
		test.Nil(t, read.ArchivedAt)
	})

	t.Run("an edit sees the values the transaction has already cleared", func(t *testing.T) {
		t.Parallel()

		// The stranded-value walk runs on the caller's executor, so an
		// administrator can clear the offending values and narrow the enumeration
		// in one transaction — and the two land together or not at all.
		store := env.newStore(t)
		definition := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		mustSet(t, env, store, testScope, testSubject, "digest", "daily")

		narrowed := *definition
		narrowed.Enumeration = []string{"weekly", "never"}

		// Without the clearance the edit is refused, and the refusal names the
		// value.
		_, err := env.update(t, store, testScope, &narrowed)
		must.ErrorIs(t, err, ErrStrandedValues)
		test.StrContains(t, err.Error(), "daily")

		// With it, the walk sees the cleared row as cleared, and the edit goes
		// through.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if _, txErr := store.ClearValue(t.Context(), tx, testScope, testSubject, "digest"); txErr != nil {
				return txErr
			}

			_, txErr := store.UpdateDefinition(t.Context(), tx, testScope, &narrowed)

			return txErr
		}))

		read, err := store.GetDefinition(t.Context(), env.reader(), testScope, definition.ID)
		must.NoError(t, err)
		test.Eq(t, []string{"never", "weekly"}, read.Enumeration)

		_, err = store.GetValue(t.Context(), env.reader(), testScope, testSubject, "digest")
		test.ErrorIs(t, err, ErrValueNotFound)
	})

	t.Run("the three moving writes answer from inside the uncommitted transaction", func(t *testing.T) {
		t.Parallel()

		// This is what returning the row buys, seen from the caller the
		// signature is for. A consumer's audit entry is written beside the write,
		// inside the same transaction, and the row it describes has not
		// committed — so the only reading of it available is the one the write
		// hands over. Before the transaction ends, nothing outside it can see any
		// of these rows at all.
		store := env.newStore(t)

		edited := mustCreate(t, env, store, testScope, boolDefinition("compact"))
		retired := mustCreate(t, env, store, testScope, intDefinition("retention.days"))
		answered := mustCreate(t, env, store, testScope, stringDefinition("digest"))

		mustSet(t, env, store, testScope, testSubject, answered.Name, "daily")

		var (
			updated  *Definition
			archived *Definition
			cleared  *Value
		)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			edit := *edited
			edit.Name = "layout.compact"

			var txErr error
			if updated, txErr = store.UpdateDefinition(t.Context(), tx, testScope, &edit); txErr != nil {
				return txErr
			}

			if archived, txErr = store.ArchiveDefinition(t.Context(), tx, testScope, retired.ID); txErr != nil {
				return txErr
			}

			cleared, txErr = store.ClearValue(t.Context(), tx, testScope, testSubject, answered.Name)
			if txErr != nil {
				return txErr
			}

			// Each row is the one the statement just wrote, stamped by the
			// server, while the transaction that wrote it is still open.
			test.EqOp(t, "layout.compact", updated.Name)
			must.NotNil(t, updated.LastUpdatedAt)
			must.NotNil(t, archived.ArchivedAt)
			test.EqOp(t, "retention.days", archived.Name)
			must.NotNil(t, cleared.ArchivedAt)
			test.EqOp(t, "daily", cleared.Raw)

			// And a reader outside the transaction has none of it yet, which is
			// why a caller inside one has nowhere else to read them from.
			outside, readErr := store.GetDefinition(t.Context(), env.reader(), testScope, edited.ID)
			must.NoError(t, readErr)
			test.EqOp(t, "compact", outside.Name)

			return nil
		}))

		// After the commit the edit is readable and the other two rows are not,
		// which is the asymmetry the two read-backs exist for.
		read, err := store.GetDefinition(t.Context(), env.reader(), testScope, edited.ID)
		must.NoError(t, err)
		test.EqOp(t, updated.Name, read.Name)

		_, err = store.GetDefinition(t.Context(), env.reader(), testScope, retired.ID)
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		_, err = store.GetValue(t.Context(), env.reader(), testScope, testSubject, answered.Name)
		test.ErrorIs(t, err, ErrValueNotFound)
	})

	t.Run("every method refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// Every one of the fourteen, not a representative one. There is no
		// connection of the store's own to fall back to, so a method that did
		// anything but refuse would be reaching for something that is not there.
		store := env.newStore(t)

		_, err := store.CreateDefinition(t.Context(), nil, testScope, stringDefinition("digest"))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.UpdateDefinition(t.Context(), nil, testScope, stringDefinition("digest"))
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ArchiveDefinition(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetDefinition(t.Context(), nil, testScope, "whatever")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetDefinitionByName(t.Context(), nil, testScope, "digest")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListDefinitions(t.Context(), nil, testScope, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.SetValue(t.Context(), nil, testScope, testSubject, "digest", "daily")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ClearValue(t.Context(), nil, testScope, testSubject, "digest")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.DeleteValuesForSubject(t.Context(), nil, testScope, testSubject)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetValue(t.Context(), nil, testScope, testSubject, "digest")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListValuesForSubject(t.Context(), nil, testScope, testSubject, nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListValuesForDefinition(t.Context(), nil, testScope, "digest", nil)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.Resolve(t.Context(), nil, testScope, testSubject, "digest")
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ResolveAll(t.Context(), nil, testScope, testSubject)
		test.ErrorIs(t, err, ErrNilExecutor)

		// And nothing was written on the way to refusing.
		_, err = store.GetDefinitionByName(t.Context(), env.reader(), testScope, "digest")
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("a refused write inside a transaction leaves the transaction usable", func(t *testing.T) {
		t.Parallel()

		// Every check the writes make runs before any statement they would send,
		// so a refusal is the store declining rather than the database aborting.
		// A caller that inspects one and carries on has a transaction to carry on
		// in, which is what lets these be collected here and asserted outside.
		store := env.newStore(t)

		taken := mustCreate(t, env, store, testScope, stringDefinition("digest"))
		other := mustCreate(t, env, store, testScope, boolDefinition("compact"))

		var unset tenancy.Scope

		var (
			nilCreate, unscopedCreate, takenCreate, malformedCreate  error
			nilUpdate, unidentifiedUpdate, absentUpdate, takenUpdate error
			absentArchive, foreignArchive                            error
			unnamedSet, undefinedSet, unenumeratedSet                error
			unnamedClear, undefinedClear, unansweredClear            error
		)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, nilCreate = store.CreateDefinition(t.Context(), tx, testScope, nil)
			_, unscopedCreate = store.CreateDefinition(t.Context(), tx, unset, stringDefinition("theme"))
			_, takenCreate = store.CreateDefinition(t.Context(), tx, testScope, stringDefinition("digest"))

			malformed := stringDefinition("theme")
			malformed.Default = pointer.To("hourly")
			_, malformedCreate = store.CreateDefinition(t.Context(), tx, testScope, malformed)

			_, nilUpdate = store.UpdateDefinition(t.Context(), tx, testScope, nil)
			_, unidentifiedUpdate = store.UpdateDefinition(t.Context(), tx, testScope, stringDefinition("digest"))

			absent := stringDefinition("digest")
			absent.ID = "def_never_written"
			_, absentUpdate = store.UpdateDefinition(t.Context(), tx, testScope, absent)

			collision := *other
			collision.Name = taken.Name
			_, takenUpdate = store.UpdateDefinition(t.Context(), tx, testScope, &collision)

			_, absentArchive = store.ArchiveDefinition(t.Context(), tx, testScope, "def_never_written")
			_, foreignArchive = store.ArchiveDefinition(t.Context(), tx, otherScope, taken.ID)

			_, unnamedSet = store.SetValue(t.Context(), tx, testScope, Subject{Type: SubjectUser}, "digest", "daily")
			_, undefinedSet = store.SetValue(t.Context(), tx, testScope, testSubject, "never.defined", "daily")
			_, unenumeratedSet = store.SetValue(t.Context(), tx, testScope, testSubject, "digest", "hourly")

			_, unnamedClear = store.ClearValue(t.Context(), tx, testScope, Subject{ID: "user-1"}, "digest")
			_, undefinedClear = store.ClearValue(t.Context(), tx, testScope, testSubject, "never.defined")
			_, unansweredClear = store.ClearValue(t.Context(), tx, testScope, testSubject, "digest")

			return nil
		}))

		test.ErrorIs(t, nilCreate, ErrNilDefinition)
		test.ErrorIs(t, unscopedCreate, tenancy.ErrNoScope)
		test.ErrorIs(t, takenCreate, ErrDefinitionNameTaken)
		test.ErrorIs(t, malformedCreate, ErrNotEnumerated)

		test.ErrorIs(t, nilUpdate, ErrNilDefinition)
		test.ErrorIs(t, unidentifiedUpdate, platformerrors.ErrInvalidIDProvided)
		test.ErrorIs(t, absentUpdate, ErrDefinitionNotFound)
		test.ErrorIs(t, takenUpdate, ErrDefinitionNameTaken)

		test.ErrorIs(t, absentArchive, ErrDefinitionNotFound)
		test.ErrorIs(t, foreignArchive, ErrDefinitionNotFound)

		test.ErrorIs(t, unnamedSet, ErrEmptySubjectID)
		test.ErrorIs(t, undefinedSet, ErrDefinitionNotFound)
		test.ErrorIs(t, unenumeratedSet, ErrNotEnumerated)

		test.ErrorIs(t, unnamedClear, ErrEmptySubjectType)
		test.ErrorIs(t, undefinedClear, ErrDefinitionNotFound)
		test.ErrorIs(t, unansweredClear, ErrValueNotFound)

		// The transaction committed with nothing in it: every refusal was refused
		// before a row changed.
		_, err := store.GetValue(t.Context(), env.reader(), testScope, testSubject, "digest")
		test.ErrorIs(t, err, ErrValueNotFound)

		_, err = store.GetDefinitionByName(t.Context(), env.reader(), testScope, "theme")
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})
}

// runErasureSuite is DeleteValuesForSubject: the one delete here, and what an
// erasure is built on.
func runErasureSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("an erasure removes a subject's answers, cleared ones included", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))
		mustCreate(t, env, store, testScope, boolDefinition("compact"))
		mustCreate(t, env, store, otherScope, stringDefinition("digest"))

		second := Subject{Type: SubjectUser, ID: "user-2"}

		// Two answers for the subject, one of them cleared — an archived row
		// that still says what they chose, which is what an erasure has to
		// reach and a clearance does not.
		mustSet(t, env, store, testScope, testSubject, "digest", "daily")
		mustSet(t, env, store, testScope, testSubject, "compact", "true")
		mustClear(t, env, store, testScope, testSubject, "compact")

		// And two rows the erasure must not touch: another subject's in this
		// scope, and this subject's in another.
		mustSet(t, env, store, testScope, second, "digest", "never")
		mustSet(t, env, store, otherScope, testSubject, "digest", "weekly")

		test.EqOp(t, int64(2), env.erase(t, store, testScope, testSubject))

		// Gone rather than archived: a page that asks for archived rows too
		// finds nothing.
		everything := filtering.DefaultQueryFilter()
		everything.IncludeArchived = pointer.To(true)

		mine, err := store.ListValuesForSubject(t.Context(), env.reader(), testScope, testSubject, everything)
		must.NoError(t, err)
		test.SliceEmpty(t, mine.Data)

		_, err = store.GetValue(t.Context(), env.reader(), testScope, testSubject, "digest")
		test.ErrorIs(t, err, ErrValueNotFound)

		theirs, err := store.GetValue(t.Context(), env.reader(), testScope, second, "digest")
		must.NoError(t, err)
		test.EqOp(t, "never", theirs.Raw)

		elsewhere, err := store.GetValue(t.Context(), env.reader(), otherScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, "weekly", elsewhere.Raw)

		// A second erasure has nothing to remove, and says so with a count
		// rather than an error.
		test.EqOp(t, int64(0), env.erase(t, store, testScope, testSubject))

		// The definitions are untouched: an erasure is about a person, not the
		// catalog, and the subject can answer again.
		revived := mustSet(t, env, store, testScope, testSubject, "digest", "weekly")
		test.EqOp(t, "weekly", revived.Raw)
	})

	t.Run("an erasure rolls back with the transaction it ran in", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))

		mustSet(t, env, store, testScope, testSubject, "digest", "daily")

		err := env.inTx(t, func(tx database.Tx) error {
			deleted, txErr := store.DeleteValuesForSubject(t.Context(), tx, testScope, testSubject)
			if txErr != nil {
				return txErr
			}

			test.EqOp(t, int64(1), deleted)

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		read, err := store.GetValue(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, "daily", read.Raw)
	})

	t.Run("an erasure has to name somebody", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		var (
			unset                      tenancy.Scope
			unscoped, untyped, unnamed error
		)

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			_, unscoped = store.DeleteValuesForSubject(t.Context(), tx, unset, testSubject)
			_, untyped = store.DeleteValuesForSubject(t.Context(), tx, testScope, Subject{ID: "user-1"})
			_, unnamed = store.DeleteValuesForSubject(t.Context(), tx, testScope, Subject{Type: SubjectUser})

			return nil
		}))

		test.ErrorIs(t, unscoped, tenancy.ErrNoScope)
		test.ErrorIs(t, untyped, ErrEmptySubjectType)
		test.ErrorIs(t, unnamed, ErrEmptySubjectID)
	})
}
