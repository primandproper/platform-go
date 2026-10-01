package comments

import (
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestTargets(T *testing.T) {
	T.Parallel()

	T.Run("knows what it was given and nothing else", func(t *testing.T) {
		t.Parallel()

		catalog := Targets{articleType: {Description: "an article"}}

		test.True(t, catalog.Known(articleType))
		test.False(t, catalog.Known(unknownType))

		// The zero value is a catalog that holds nothing, which is what a store
		// built without one enforces.
		test.False(t, Targets(nil).Known(articleType))
	})

	T.Run("renders its types sorted", func(t *testing.T) {
		t.Parallel()

		// Sorted because a console renders this list and a map's order is not
		// one: an unsorted list is a settings page whose rows move between
		// refreshes.
		catalog := Targets{
			newsletterType: {Description: "a newsletter"},
			articleType:    {Description: "an article"},
		}

		test.Eq(t, []TargetType{articleType, newsletterType}, catalog.TargetTypes())
		test.SliceEmpty(t, Targets{}.TargetTypes())
	})
}

func TestTarget_Validate(T *testing.T) {
	T.Parallel()

	T.Run("accepts a target naming both halves", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, Target{Type: articleType, ID: "article_1"}.Validate())
	})

	T.Run("refuses a half that is missing or only whitespace", func(t *testing.T) {
		t.Parallel()

		// Whitespace is not a name. A target holding it renders as blank in every
		// console and matches nothing anyone would search for.
		must.ErrorIs(t, Target{ID: "article_1"}.Validate(), ErrEmptyTargetType)
		must.ErrorIs(t, Target{Type: " ", ID: "article_1"}.Validate(), ErrEmptyTargetType)
		must.ErrorIs(t, Target{Type: articleType}.Validate(), ErrEmptyTargetID)
		must.ErrorIs(t, Target{Type: articleType, ID: "\t"}.Validate(), ErrEmptyTargetID)
	})

	T.Run("says nothing about whether the catalog holds the type", func(t *testing.T) {
		t.Parallel()

		// The shape check is exported so a handler can answer 400 before a store
		// is reached; whether the type exists is the store's, because only the
		// store has the catalog.
		test.NoError(t, Target{Type: unknownType, ID: "x"}.Validate())
	})
}

func TestTarget_Zero(t *testing.T) {
	t.Parallel()

	// What a reply that adopts its parent's target looks like on the way in. A
	// half-filled target is not zero: it is a caller who meant something and got
	// it wrong, and Validate is what tells them so.
	test.True(t, Target{}.Zero())
	test.False(t, Target{Type: articleType}.Zero())
	test.False(t, Target{ID: "article_1"}.Zero())
}

func TestComment_Root(t *testing.T) {
	t.Parallel()

	test.True(t, (&Comment{}).Root())
	test.True(t, (&Comment{ParentID: RootParentID}).Root())
	test.False(t, (&Comment{ParentID: "comment_1"}).Root())

	// A nil comment replies to nothing and is not a root either; the method
	// answers rather than panicking, because it is read off values that came
	// back from a store.
	test.False(t, (*Comment)(nil).Root())
}

func TestTargetType_String(t *testing.T) {
	t.Parallel()

	// Spelled at the observability seams rather than left to a reflective
	// default: a defined string type is neither string nor fmt.Stringer to the
	// switch that records an attribute.
	test.EqOp(t, "article", articleType.String())
}
