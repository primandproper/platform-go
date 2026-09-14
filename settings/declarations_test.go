package settings

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runDeclarationSuite is the boot half: what a composition root declaring its
// catalog gets on the first boot, on the second, and on the one where the
// catalog changed under values subjects have already chosen.
func runDeclarationSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a catalog declared on an empty scope is created", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		declared := mustDeclare(t, env, store, testScope, []Declaration{
			digestDeclaration("digest"),
			{Name: "compact", Kind: KindBool, Default: pointer.To("false")},
		})

		for _, one := range declared {
			test.EqOp(t, OutcomeCreated, one.Outcome)
			must.NotNil(t, one.Definition)
			test.NotEqOp(t, "", one.Definition.ID)
			test.EqOp(t, testScope, one.Definition.Scope)
			test.Nil(t, one.Definition.LastUpdatedAt)
		}

		// Declared in the order the catalog spells, so a caller logging what its
		// boot did reads it beside the declaration it wrote.
		test.EqOp(t, "digest", declared[0].Definition.Name)
		test.EqOp(t, "compact", declared[1].Definition.Name)

		read, err := store.GetDefinitionByName(t.Context(), env.reader(), testScope, "digest")
		must.NoError(t, err)
		test.EqOp(t, declared[0].Definition.ID, read.ID)
		test.EqOp(t, "how often a digest is sent", read.Description)
		test.EqOp(t, "weekly", pointer.Dereference(read.Default))
		test.Eq(t, []string{"daily", "never", "weekly"}, read.Enumeration)
	})

	t.Run("declaring the same catalog again writes nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		catalog := []Declaration{digestDeclaration("digest")}

		first := mustDeclare(t, env, store, testScope, catalog)
		second := mustDeclare(t, env, store, testScope, catalog)

		test.EqOp(t, OutcomeCreated, first[0].Outcome)
		test.EqOp(t, OutcomeUnchanged, second[0].Outcome)

		// The same row, and one nothing has edited. A reconcile that rewrote a
		// definition it agreed with would stamp last_updated_at on every restart
		// and walk every stored value to do it.
		test.EqOp(t, first[0].Definition.ID, second[0].Definition.ID)
		test.Nil(t, second[0].Definition.LastUpdatedAt)

		read, err := store.GetDefinitionByName(t.Context(), env.reader(), testScope, "digest")
		must.NoError(t, err)
		test.Nil(t, read.LastUpdatedAt)
	})

	t.Run("a declaration the enumeration's order differs in is still unchanged", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mustDeclare(t, env, store, testScope, []Declaration{digestDeclaration("digest")})

		shuffled := digestDeclaration("digest")
		shuffled.Enumeration = []string{"never", "weekly", "daily"}

		declared := mustDeclare(t, env, store, testScope, []Declaration{shuffled})
		test.EqOp(t, OutcomeUnchanged, declared[0].Outcome)

		// The caller's slice is theirs: the comparison sorts a copy.
		test.Eq(t, []string{"never", "weekly", "daily"}, shuffled.Enumeration)
	})

	t.Run("a changed declaration is reconciled onto the row it already has", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		created := mustDeclare(t, env, store, testScope, []Declaration{digestDeclaration("digest")})
		mustSet(t, env, store, testScope, testSubject, "digest", "daily")

		widened := digestDeclaration("digest")
		widened.Description = "how often the digest goes out"
		widened.Default = pointer.To("never")
		widened.Enumeration = []string{"weekly", "daily", "never", "hourly"}
		widened.AdminOnly = true

		declared := mustDeclare(t, env, store, testScope, []Declaration{widened})
		test.EqOp(t, OutcomeUpdated, declared[0].Outcome)

		// The id is the one the values point at. A reconcile that wrote a new row
		// would leave every answer attached to a definition nothing reads.
		test.EqOp(t, created[0].Definition.ID, declared[0].Definition.ID)
		test.NotNil(t, declared[0].Definition.LastUpdatedAt)
		test.EqOp(t, "how often the digest goes out", declared[0].Definition.Description)
		test.EqOp(t, "never", pointer.Dereference(declared[0].Definition.Default))
		test.True(t, declared[0].Definition.AdminOnly)
		test.Eq(t, []string{"daily", "hourly", "never", "weekly"}, declared[0].Definition.Enumeration)

		// The subject's answer survived the edit, which is the whole point of
		// reconciling rather than replacing.
		resolved, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, SourceSubject, resolved.Source)
		test.EqOp(t, "daily", resolved.Raw)
	})

	t.Run("a declaration that would strand a stored value takes the whole catalog with it", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mustDeclare(t, env, store, testScope, []Declaration{digestDeclaration("digest")})
		mustSet(t, env, store, testScope, testSubject, "digest", "never")

		narrowed := digestDeclaration("digest")
		narrowed.Enumeration = []string{"weekly", "daily"}

		// The new setting is declared first, so the refusal reaches it after it
		// has been written: what the transaction leaves behind is the test.
		_, err := env.declare(t, store, testScope, []Declaration{
			{Name: "compact", Kind: KindBool},
			narrowed,
		})
		test.ErrorIs(t, err, ErrStrandedValues)

		_, err = store.GetDefinitionByName(t.Context(), env.reader(), testScope, "compact")
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		// And the setting that was already there is as it was.
		read, err := store.GetDefinitionByName(t.Context(), env.reader(), testScope, "digest")
		must.NoError(t, err)
		test.Eq(t, []string{"daily", "never", "weekly"}, read.Enumeration)
	})

	t.Run("a catalog that names one setting twice is refused before anything is written", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		second := digestDeclaration("digest")
		second.Default = pointer.To("daily")

		_, err := env.declare(t, store, testScope, []Declaration{
			{Name: "compact", Kind: KindBool},
			digestDeclaration("digest"),
			second,
		})
		test.ErrorIs(t, err, ErrDuplicateDeclaration)

		// Nothing ran: the whole catalog is checked before the first read.
		_, err = store.GetDefinitionByName(t.Context(), env.reader(), testScope, "compact")
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("a malformed declaration is refused before anything is written", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		outside := digestDeclaration("digest")
		outside.Default = pointer.To("hourly")

		_, err := env.declare(t, store, testScope, []Declaration{
			{Name: "compact", Kind: KindBool},
			outside,
		})
		test.ErrorIs(t, err, ErrNotEnumerated)

		_, err = store.GetDefinitionByName(t.Context(), env.reader(), testScope, "compact")
		test.ErrorIs(t, err, ErrDefinitionNotFound)
	})

	t.Run("a declaration naming a retired setting is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		declared := mustDeclare(t, env, store, testScope, []Declaration{digestDeclaration("digest")})
		mustArchive(t, env, store, testScope, declared[0].Definition.ID)

		// An archived name stays taken, and reviving the definition silently
		// would undo a retirement nobody asked to undo.
		_, err := env.declare(t, store, testScope, []Declaration{digestDeclaration("digest")})
		test.ErrorIs(t, err, ErrDefinitionNameTaken)
	})

	t.Run("a setting the catalog omits is left alone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mustDeclare(t, env, store, testScope, []Declaration{
			digestDeclaration("digest"),
			{Name: "compact", Kind: KindBool},
		})

		// The next boot's catalog is smaller. It reconciles what it names and
		// retires nothing: archiving is somebody saying so.
		mustDeclare(t, env, store, testScope, []Declaration{digestDeclaration("digest")})

		read, err := store.GetDefinitionByName(t.Context(), env.reader(), testScope, "compact")
		must.NoError(t, err)
		test.Nil(t, read.ArchivedAt)
	})

	t.Run("each scope declares its own catalog", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		catalog := []Declaration{digestDeclaration("digest")}

		here := mustDeclare(t, env, store, testScope, catalog)
		there := mustDeclare(t, env, store, otherScope, catalog)

		test.EqOp(t, OutcomeCreated, here[0].Outcome)
		test.EqOp(t, OutcomeCreated, there[0].Outcome)
		test.NotEqOp(t, here[0].Definition.ID, there[0].Definition.ID)

		// And the second scope's boot does not reconcile the first scope's row.
		again := mustDeclare(t, env, store, otherScope, catalog)
		test.EqOp(t, OutcomeUnchanged, again[0].Outcome)
		test.EqOp(t, there[0].Definition.ID, again[0].Definition.ID)
	})

	t.Run("an empty catalog declares nothing", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		declared, err := env.declare(t, store, testScope, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, declared)
	})

	t.Run("the reconcile reads what its own transaction has written", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		catalog := []Declaration{digestDeclaration("digest")}

		var second []*Declared

		// Both calls in one transaction: the second reads the definition the
		// first wrote and has not committed, which is what taking the caller's
		// executor for the read buys.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if _, err := DeclareDefinitions(t.Context(), store, tx, testScope, catalog); err != nil {
				return err
			}

			var err error
			second, err = DeclareDefinitions(t.Context(), store, tx, testScope, catalog)

			return err
		}))

		must.SliceLen(t, 1, second)
		test.EqOp(t, OutcomeUnchanged, second[0].Outcome)
	})

	t.Run("a setting declared at boot is one a value can be set against", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// The composition root's transaction is the caller's, so a boot that
		// declares its catalog and seeds one answer does both or neither.
		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if _, err := DeclareDefinitions(t.Context(), store, tx, testScope,
				[]Declaration{digestDeclaration("digest")}); err != nil {
				return err
			}

			_, err := store.SetValue(t.Context(), tx, testScope, testSubject, "digest", "daily")

			return err
		}))

		resolved, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, SourceSubject, resolved.Source)
		test.EqOp(t, "daily", resolved.Raw)
	})
}

// TestDeclareDefinitions_Arguments is the half that needs no database: what the
// function refuses before it reads anything.
func TestDeclareDefinitions_Arguments(T *testing.T) {
	T.Parallel()

	catalog := []Declaration{digestDeclaration("digest")}

	T.Run("nil store", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		test.ErrorIs(t, env.inTx(t, func(tx database.Tx) error {
			declared, err := DeclareDefinitions(t.Context(), nil, tx, testScope, catalog)
			test.Nil(t, declared)

			return err
		}), ErrNilStore)
	})

	T.Run("nil transaction", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		declared, err := DeclareDefinitions(t.Context(), store, nil, testScope, catalog)
		test.Nil(t, declared)
		test.ErrorIs(t, err, ErrNilExecutor)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("a scope naming nobody", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)
		store := env.newStore(t)

		// The zero Scope is not tenancy.Global(): it names nobody, and a catalog
		// declared under it would be one no read of either scope reaches.
		test.Error(t, env.inTx(t, func(tx database.Tx) error {
			declared, err := DeclareDefinitions(t.Context(), store, tx, tenancy.Scope{}, catalog)
			test.Nil(t, declared)

			return err
		}))
	})
}

// TestDeclarationMatches pins the comparison that decides whether a boot writes
// anything, field by field, because every false negative here is a rewritten
// enumeration and a stranded-value walk on every restart.
func TestDeclarationMatches(T *testing.T) {
	T.Parallel()

	base := digestDeclaration("digest")

	stored := &Definition{
		Name:        base.Name,
		Description: base.Description,
		Kind:        base.Kind,
		Default:     pointer.To("weekly"),
		Enumeration: []string{"daily", "never", "weekly"},
	}

	T.Run("the row the declaration describes", func(t *testing.T) {
		t.Parallel()

		test.True(t, base.matches(stored))
	})

	T.Run("the fields the store owns are not compared", func(t *testing.T) {
		t.Parallel()

		// An id, a creation stamp and a scope are what the store filled in. A
		// declaration has nowhere to put them and does not disagree with them.
		other := *stored
		other.ID = "definition-1"
		other.Scope = otherScope
		other.LastUpdatedAt = pointer.To(stored.CreatedAt)

		test.True(t, base.matches(&other))
	})

	T.Run("no row at all", func(t *testing.T) {
		t.Parallel()

		test.False(t, base.matches(nil))
	})

	for name, mutate := range map[string]func(*Declaration){
		"description": func(d *Declaration) { d.Description = "something else" },
		"kind":        func(d *Declaration) { d.Kind = KindBool },
		"default":     func(d *Declaration) { d.Default = pointer.To("daily") },
		"no default":  func(d *Declaration) { d.Default = nil },
		"enumeration": func(d *Declaration) { d.Enumeration = []string{"daily", "weekly"} },
		"admin only":  func(d *Declaration) { d.AdminOnly = true },
	} {
		T.Run(name+" differs", func(t *testing.T) {
			t.Parallel()

			changed := digestDeclaration("digest")
			mutate(&changed)

			test.False(t, changed.matches(stored))
		})
	}

	T.Run("a default of none against a default of empty", func(t *testing.T) {
		t.Parallel()

		// Having no default and defaulting to "" are different answers — see
		// Definition.Default — so they are different declarations.
		none := Declaration{Name: "note", Kind: KindString}
		empty := Declaration{Name: "note", Kind: KindString, Default: pointer.To("")}

		test.True(t, none.matches(none.definition()))
		test.True(t, empty.matches(empty.definition()))
		test.False(t, none.matches(empty.definition()))
		test.False(t, empty.matches(none.definition()))
	})
}

func TestOutcome_String(T *testing.T) {
	T.Parallel()

	test.EqOp(T, "created", OutcomeCreated.String())
	test.EqOp(T, "updated", OutcomeUpdated.String())
	test.EqOp(T, "unchanged", OutcomeUnchanged.String())
}
