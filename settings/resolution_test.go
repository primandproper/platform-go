package settings

import (
	"fmt"
	"testing"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/pointer"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// runResolutionSuite is the typed read: value, else default, else neither.
func runResolutionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a resolution says where its value came from", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("digest"))

		// Nobody has answered, and the definition has a default.
		fallback, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, SourceDefault, fallback.Source)
		test.EqOp(t, "weekly", fallback.Raw)
		test.Nil(t, fallback.Value)
		test.True(t, fallback.Set())

		_, err = env.set(t, store, testScope, testSubject, "digest", "daily")
		must.NoError(t, err)

		chosen, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, SourceSubject, chosen.Source)
		test.EqOp(t, "daily", chosen.Raw)
		must.NotNil(t, chosen.Value)
		test.EqOp(t, "daily", chosen.Value.Raw)

		// Clearing puts them back on the default.
		mustClear(t, env, store, testScope, testSubject, "digest")

		cleared, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "digest")
		must.NoError(t, err)
		test.EqOp(t, SourceDefault, cleared.Source)
	})

	t.Run("a setting with no value and no default is unset rather than zero", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, intDefinition("retention.days"))

		unset, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "retention.days")
		must.NoError(t, err)
		test.EqOp(t, SourceUnset, unset.Source)
		test.EqOp(t, "", unset.Raw)
		test.False(t, unset.Set())

		// The zero an untyped read would have handed back is exactly what a
		// caller must not be given, so the accessor reports the sentinel.
		days, err := unset.Int()
		test.EqOp(t, int64(0), days)
		test.ErrorIs(t, err, ErrSettingUnset)

		// A definition whose default is the empty string is answered, and that
		// is the distinction a non-nullable column could not carry.
		empty := &Definition{Name: "signature", Kind: KindString, Default: pointer.To("")}
		mustCreate(t, env, store, testScope, empty)

		resolved, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "signature")
		must.NoError(t, err)
		test.EqOp(t, SourceDefault, resolved.Source)
		test.True(t, resolved.Set())

		signature, err := resolved.String()
		must.NoError(t, err)
		test.EqOp(t, "", signature)
	})

	t.Run("a resolution reads back as its kind", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, boolDefinition("compact"))
		mustCreate(t, env, store, testScope, intDefinition("retention.days"))
		mustCreate(t, env, store, testScope, &Definition{Name: "ratio", Kind: KindFloat, Default: pointer.To("0.5")})

		_, err := env.set(t, store, testScope, testSubject, "retention.days", "30")
		must.NoError(t, err)

		compact, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "compact")
		must.NoError(t, err)

		enabled, err := compact.Bool()
		must.NoError(t, err)
		test.True(t, enabled)

		// Read as the wrong kind, it is a mistake in the calling code rather
		// than a coerced answer.
		_, err = compact.Int()
		test.ErrorIs(t, err, ErrKindMismatch)

		retention, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "retention.days")
		must.NoError(t, err)

		days, err := retention.Int()
		must.NoError(t, err)
		test.EqOp(t, int64(30), days)

		ratio, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "ratio")
		must.NoError(t, err)

		fraction, err := ratio.Float()
		must.NoError(t, err)
		test.EqOp(t, 0.5, fraction)
	})

	t.Run("resolving a setting that does not exist is an error", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.Resolve(t.Context(), env.reader(), testScope, testSubject, "never.defined")
		test.ErrorIs(t, err, ErrDefinitionNotFound)

		// An empty name is caught before the read rather than matching a row
		// whose name column happens to be empty — no definition can have one.
		_, err = store.Resolve(t.Context(), env.reader(), testScope, testSubject, "")
		test.ErrorIs(t, err, ErrEmptyDefinitionName)
	})

	t.Run("resolving everything answers for settings nobody has touched", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		mustCreate(t, env, store, testScope, stringDefinition("c.digest"))
		mustCreate(t, env, store, testScope, boolDefinition("a.compact"))
		mustCreate(t, env, store, testScope, intDefinition("b.retention"))

		_, err := env.set(t, store, testScope, testSubject, "c.digest", "daily")
		must.NoError(t, err)

		resolutions, err := store.ResolveAll(t.Context(), env.reader(), testScope, testSubject)
		must.NoError(t, err)
		must.SliceLen(t, 3, resolutions)

		// Sorted by name rather than by the id the pages walked.
		test.EqOp(t, "a.compact", resolutions[0].Definition.Name)
		test.EqOp(t, "b.retention", resolutions[1].Definition.Name)
		test.EqOp(t, "c.digest", resolutions[2].Definition.Name)

		test.EqOp(t, SourceDefault, resolutions[0].Source)
		test.EqOp(t, SourceUnset, resolutions[1].Source)
		test.EqOp(t, SourceSubject, resolutions[2].Source)
		test.EqOp(t, "daily", resolutions[2].Raw)

		// An archived definition is out of the catalog, so it is out of this.
		retired := mustCreate(t, env, store, testScope, boolDefinition("d.retired"))
		mustArchive(t, env, store, testScope, retired.ID)

		after, err := store.ResolveAll(t.Context(), env.reader(), testScope, testSubject)
		must.NoError(t, err)
		test.SliceLen(t, 3, after)

		// Another subject in the same catalog sees the same settings and none
		// of the first subject's answers.
		other, err := store.ResolveAll(t.Context(), env.reader(), testScope, Subject{Type: SubjectUser, ID: "user-2"})
		must.NoError(t, err)
		must.SliceLen(t, 3, other)
		test.EqOp(t, SourceDefault, other[2].Source)
		test.EqOp(t, "weekly", other[2].Raw)
	})

	t.Run("resolving everything walks past one page", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		// More definitions than a page holds, so the walk has to follow its
		// cursor rather than answering with the first page.
		const catalog = filtering.DefaultQueryFilterLimit + 5

		for i := range catalog {
			// Zero-padded so the names sort the way the ids do, which is what
			// makes the assertion about the walk rather than about the sort.
			mustCreate(t, env, store, testScope, boolDefinition(fmt.Sprintf("flag.%03d", i)))
		}

		resolutions, err := store.ResolveAll(t.Context(), env.reader(), testScope, testSubject)
		must.NoError(t, err)
		test.SliceLen(t, catalog, resolutions)
	})
}
