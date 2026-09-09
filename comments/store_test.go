package comments

import (
	"errors"
	"testing"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore_SQLite runs the behavioral suite against SQLite, which is the
// engine every developer has. TestSQLStore_RealServers runs the identical suite
// against Postgres and MySQL — see containers_test.go.
func TestSQLStore_SQLite(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is everything this store promises, run against whichever
// database it is handed.
//
// It is one function rather than a file of top-level tests because the three
// engines have to be held to the same behavior: the placeholder rendering, the
// archived predicates and the :execrows count every write reads as its answer
// are spelled three ways, and a suite that ran only against SQLite would prove
// the one spelling SQLite accepts.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("writes", func(t *testing.T) {
		t.Parallel()

		runWriteSuite(t, env)
	})

	t.Run("targets", func(t *testing.T) {
		t.Parallel()

		runTargetSuite(t, env)
	})

	t.Run("threads", func(t *testing.T) {
		t.Parallel()

		runThreadSuite(t, env)
	})

	t.Run("reads", func(t *testing.T) {
		t.Parallel()

		runReadSuite(t, env)
	})

	t.Run("sweeps", func(t *testing.T) {
		t.Parallel()

		runSweepSuite(t, env)
	})

	t.Run("transactions", func(t *testing.T) {
		t.Parallel()

		runTransactionSuite(t, env)
	})
}

func runWriteSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a written comment comes back with what the database assigned", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		c := newComment(testAuthor, "this was delicious")
		must.NoError(t, env.create(t, store, testScope, c))

		// The id is minted here and the creation time is the database's, read
		// back rather than left as a zero time a caller would serialize as a
		// date in the year one.
		test.NotEqOp(t, "", c.ID)
		test.False(t, c.CreatedAt.IsZero())
		test.Nil(t, c.LastUpdatedAt)
		test.True(t, c.Root())

		read, err := store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
		test.EqOp(t, c.ID, read.ID)
		test.EqOp(t, testAuthor, read.Author)
		test.EqOp(t, "this was delicious", read.Body)
		test.EqOp(t, testTarget, read.Target)
		test.EqOp(t, RootParentID, read.ParentID)
	})

	t.Run("a caller-supplied id is kept", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		c := newComment(testAuthor, "words")
		c.ID = "comment_of_my_own"
		must.NoError(t, env.create(t, store, testScope, c))

		read, err := store.GetComment(t.Context(), env.reader(), testScope, "comment_of_my_own")
		must.NoError(t, err)
		test.EqOp(t, "comment_of_my_own", read.ID)
	})

	t.Run("a comment written by nobody or saying nothing is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		anonymous := newComment("", "words")
		must.ErrorIs(t, env.create(t, store, testScope, anonymous), ErrEmptyAuthor)

		// Whitespace is nothing said. A row holding it is a comment a client
		// renders as an empty bubble.
		silent := newComment(testAuthor, "   \n\t ")
		must.ErrorIs(t, env.create(t, store, testScope, silent), ErrEmptyBody)
	})

	t.Run("a nil comment and an unset scope are refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		must.ErrorIs(t, env.create(t, store, testScope, nil), ErrNilComment)

		// The scope the write binds is the argument's, so the unset one that has
		// to be refused is the argument. tenancy.Scope answers for that itself:
		// an unset scope is a driver error rather than a wider write.
		must.ErrorIs(t,
			env.create(t, store, tenancy.Scope{}, newComment(testAuthor, "words")),
			tenancy.ErrNoScope)
	})

	t.Run("a comment that names another scope than the write is refused", func(t *testing.T) {
		t.Parallel()

		// The ruling the port settled: the argument is what the statement binds,
		// so a Comment carrying a different scope is a caller writing one
		// tenant's comment into another. It is refused rather than corrected, the
		// same way a reply naming a target its parent is not on is refused.
		store := env.newStore(t)

		elsewhere := newComment(testAuthor, "written by somebody next door")
		elsewhere.Scope = otherScope

		must.ErrorIs(t, env.create(t, store, testScope, elsewhere), ErrScopeMismatch)

		page, err := store.ListCommentsByAuthor(t.Context(), env.reader(), testScope, testAuthor, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)
	})

	t.Run("a comment that names no scope adopts the write's", func(t *testing.T) {
		t.Parallel()

		// The other half of the same ruling, and the one that keeps a caller
		// assembling a fresh comment from spelling the scope twice. tenancy.Scope
		// tells its zero value apart from Global(), so "unset" here is unset
		// rather than the global scope written shortly.
		store := env.newStore(t)

		fresh := newComment(testAuthor, "no scope of its own")
		fresh.Scope = tenancy.Scope{}

		must.NoError(t, env.create(t, store, testScope, fresh))
		test.EqOp(t, testScope, fresh.Scope)

		read, err := store.GetComment(t.Context(), env.reader(), testScope, fresh.ID)
		must.NoError(t, err)
		test.EqOp(t, testScope, read.Scope)
	})

	t.Run("an edit revises the body and stamps the row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		c := written(t, env, store, newComment(testAuthor, "frist"))

		c.Body = "first"
		must.NoError(t, env.update(t, store, testScope, c))

		read, err := store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
		test.EqOp(t, "first", read.Body)

		// The stamp a client renders "edited" from. It is nil until somebody
		// revises the comment, which is what makes it readable as a fact about
		// the words rather than about the row.
		must.NotNil(t, read.LastUpdatedAt)
	})

	t.Run("an edit cannot move a comment to another target, parent or author", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		root := written(t, env, store, newComment(testAuthor, "root"))
		child := written(t, env, store, reply(root.ID, otherAuthor, "child"))

		// Everything but the body changed, which is everything the statement
		// does not assign: the write succeeds and none of it lands.
		child.Body = "child, edited"
		child.Target = Target{Type: mealType, ID: "meal_9"}
		child.ParentID = RootParentID
		child.Author = "somebody_else"
		must.NoError(t, env.update(t, store, testScope, child))

		read, err := store.GetComment(t.Context(), env.reader(), testScope, child.ID)
		must.NoError(t, err)
		test.EqOp(t, "child, edited", read.Body)
		test.EqOp(t, testTarget, read.Target)
		test.EqOp(t, root.ID, read.ParentID)
		test.EqOp(t, otherAuthor, read.Author)
	})

	t.Run("an edit that empties the body is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		c := written(t, env, store, newComment(testAuthor, "words"))

		c.Body = " "
		must.ErrorIs(t, env.update(t, store, testScope, c), ErrEmptyBody)

		read, err := store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
		test.EqOp(t, "words", read.Body)
	})

	t.Run("a write cannot reach another scope's comment", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		c := written(t, env, store, newComment(testAuthor, "mine"))

		theirs := *c
		theirs.Scope = otherScope
		theirs.Body = "not yours to edit"

		must.ErrorIs(t, env.update(t, store, otherScope, &theirs), ErrCommentNotFound)
		must.ErrorIs(t, env.archive(t, store, otherScope, c.ID), ErrCommentNotFound)

		read, err := store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
		test.EqOp(t, "mine", read.Body)
	})

	t.Run("an archived comment leaves the discussion and cannot be archived twice", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		c := written(t, env, store, newComment(testAuthor, "off topic"))

		must.NoError(t, env.archive(t, store, testScope, c.ID))

		_, err := store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.ErrorIs(t, err, ErrCommentNotFound)

		// The statement excludes archived rows, so a second archive addresses
		// nothing — which is the honest answer rather than a quiet success.
		must.ErrorIs(t, env.archive(t, store, testScope, c.ID), ErrCommentNotFound)

		page, err := store.ListRootComments(t.Context(), env.reader(), testScope, testTarget, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)
	})
}

func runTargetSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a target type nobody registered is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		c := newComment(testAuthor, "about a thing that does not exist here")
		c.Target = Target{Type: unknownType, ID: "x"}

		must.ErrorIs(t, env.create(t, store, testScope, c), ErrUnknownTargetType)
	})

	t.Run("a store with no catalog accepts nothing", func(t *testing.T) {
		t.Parallel()

		// The reading webhooks takes of an absent event catalog. A store built
		// without one is a wiring mistake, and failing on the first write beats
		// storing rows under types nothing will ever list.
		store := env.newStore(t, WithTargets(Targets{}))

		must.ErrorIs(t, env.create(t, store, testScope, newComment(testAuthor, "words")),
			ErrUnknownTargetType)
	})

	t.Run("a malformed target is refused before the catalog is consulted", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		typeless := newComment(testAuthor, "words")
		typeless.Target = Target{ID: "recipe_1"}
		must.ErrorIs(t, env.create(t, store, testScope, typeless), ErrEmptyTargetType)

		// The empty id is not a wildcard: a comment holding it would be about
		// every recipe and no recipe at once.
		idless := newComment(testAuthor, "words")
		idless.Target = Target{Type: recipeType}
		must.ErrorIs(t, env.create(t, store, testScope, idless), ErrEmptyTargetID)
	})

	t.Run("a registered existence check is consulted, in the comment's scope", func(t *testing.T) {
		t.Parallel()

		check := newRecordingCheck(true, nil)
		store := env.newStore(t, WithTargets(Targets{
			recipeType: {Description: "a recipe", Exists: check.exists},
		}))

		must.NoError(t, env.create(t, store, testScope, newComment(testAuthor, "words")))

		test.Eq(t, []string{testTarget.ID}, check.asked)
		test.Eq(t, []tenancy.Scope{testScope}, check.scopes)
	})

	t.Run("a target the check cannot find is refused and nothing is written", func(t *testing.T) {
		t.Parallel()

		check := newRecordingCheck(false, nil)
		store := env.newStore(t, WithTargets(Targets{
			recipeType: {Description: "a recipe", Exists: check.exists},
		}))

		c := newComment(testAuthor, "about a deleted recipe")
		must.ErrorIs(t, env.create(t, store, testScope, c), ErrTargetNotFound)

		page, err := store.ListCommentsByTargetType(t.Context(), env.reader(), testScope, recipeType, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, page.Data)
	})

	t.Run("a check that fails is not a target that is absent", func(t *testing.T) {
		t.Parallel()

		// The two lead to opposite actions and only one of them is recoverable by
		// trying again, so a hook that could not reach its table fails the write
		// rather than deciding the target is gone.
		check := newRecordingCheck(false, errCheckUnavailable)
		store := env.newStore(t, WithTargets(Targets{
			recipeType: {Description: "a recipe", Exists: check.exists},
		}))

		err := env.create(t, store, testScope, newComment(testAuthor, "words"))
		must.ErrorIs(t, err, errCheckUnavailable)
		test.False(t, errors.Is(err, ErrTargetNotFound))
	})

	t.Run("the catalog is copied, so a later mutation changes nothing", func(t *testing.T) {
		t.Parallel()

		catalog := Targets{recipeType: {Description: "a recipe"}}
		store := env.newStore(t, WithTargets(catalog))

		catalog[unknownType] = TargetDefinition{Description: "smuggled in afterwards"}

		c := newComment(testAuthor, "words")
		c.Target = Target{Type: unknownType, ID: "x"}

		must.ErrorIs(t, env.create(t, store, testScope, c), ErrUnknownTargetType)
		test.Eq(t, []TargetType{recipeType}, store.TargetTypes())
	})

	t.Run("a read is not gated on the catalog", func(t *testing.T) {
		t.Parallel()

		// The withdrawal case, which is the whole reason reads are ungated: the
		// rows written under a type that has since left the catalog are exactly
		// the ones an operator needs to reach.
		store, prefix := env.newStoreWithPrefix(t)
		written(t, env, store, newComment(testAuthor, "written while the type was live"))

		withdrawn, err := NewSQLStore(env.client,
			WithTablePrefix(prefix), WithTargets(Targets{mealType: {Description: "a meal"}}))
		must.NoError(t, err)

		page, err := withdrawn.ListCommentsByTargetType(t.Context(), env.reader(), testScope, recipeType, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)

		// And the write is still refused, which is the asymmetry stated as a
		// pair rather than as one half.
		must.ErrorIs(t, env.create(t, withdrawn, testScope, newComment(testAuthor, "too late")),
			ErrUnknownTargetType)
	})
}

func runThreadSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a reply adopts its parent's target", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		root := written(t, env, store, newComment(testAuthor, "root"))

		child := reply(root.ID, otherAuthor, "child")
		must.NoError(t, env.create(t, store, testScope, child))

		test.EqOp(t, testTarget, child.Target)

		read, err := store.GetComment(t.Context(), env.reader(), testScope, child.ID)
		must.NoError(t, err)
		test.EqOp(t, testTarget, read.Target)
		test.EqOp(t, root.ID, read.ParentID)
		test.False(t, read.Root())
	})

	t.Run("a reply naming its parent's target is accepted", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		root := written(t, env, store, newComment(testAuthor, "root"))

		child := reply(root.ID, otherAuthor, "child")
		child.Target = testTarget

		must.NoError(t, env.create(t, store, testScope, child))
	})

	t.Run("a reply naming a different target is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		root := written(t, env, store, newComment(testAuthor, "root"))

		child := reply(root.ID, otherAuthor, "child")
		child.Target = Target{Type: mealType, ID: "meal_9"}

		must.ErrorIs(t, env.create(t, store, testScope, child), ErrTargetMismatch)
	})

	t.Run("a reply to a reply is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		root := written(t, env, store, newComment(testAuthor, "root"))
		child := written(t, env, store, reply(root.ID, otherAuthor, "child"))

		grandchild := reply(child.ID, testAuthor, "grandchild")
		must.ErrorIs(t, env.create(t, store, testScope, grandchild), ErrNestedReply)
	})

	t.Run("a reply to a comment that is not in the scope is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		orphan := reply("no_such_comment", testAuthor, "into the void")
		must.ErrorIs(t, env.create(t, store, testScope, orphan), ErrParentNotFound)

		// Another scope's comment reads as absent from here, which is the same
		// answer and for the same reason a get gives it.
		mine := written(t, env, store, newComment(testAuthor, "root"))

		crossScope := reply(mine.ID, testAuthor, "from next door")
		crossScope.Scope = otherScope
		must.ErrorIs(t, env.create(t, store, otherScope, crossScope), ErrParentNotFound)
	})

	t.Run("a reply to an archived comment is refused", func(t *testing.T) {
		t.Parallel()

		// A moderator removed the root; replying to it now would put a comment
		// under something no discussion renders.
		store := env.newStore(t)
		root := written(t, env, store, newComment(testAuthor, "root"))
		must.NoError(t, env.archive(t, store, testScope, root.ID))

		must.ErrorIs(t, env.create(t, store, testScope, reply(root.ID, otherAuthor, "late")),
			ErrParentNotFound)
	})

	t.Run("archiving a root leaves its replies where they are", func(t *testing.T) {
		t.Parallel()

		// The ruling in the package doc: a reply outlives the comment it replies
		// to, and "in reply to a removed comment" is what a discussion renders.
		store := env.newStore(t)
		root := written(t, env, store, newComment(testAuthor, "root"))
		child := written(t, env, store, reply(root.ID, otherAuthor, "child"))

		must.NoError(t, env.archive(t, store, testScope, root.ID))

		replies, err := store.ListReplies(t.Context(), env.reader(), testScope, testTarget, root.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, replies.Data)
		test.EqOp(t, child.ID, replies.Data[0].ID)
	})

	t.Run("the roots and the replies are different halves of the discussion", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		first := written(t, env, store, newComment(testAuthor, "first root"))
		second := written(t, env, store, newComment(otherAuthor, "second root"))
		child := written(t, env, store, reply(first.ID, otherAuthor, "a reply"))

		roots, err := store.ListRootComments(t.Context(), env.reader(), testScope, testTarget, nil)
		must.NoError(t, err)
		must.SliceLen(t, 2, roots.Data)
		test.Eq(t, []string{first.ID, second.ID}, ids(roots.Data))

		replies, err := store.ListReplies(t.Context(), env.reader(), testScope, testTarget, first.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, replies.Data)
		test.EqOp(t, child.ID, replies.Data[0].ID)

		// The other root has none, which is a page rather than an error.
		none, err := store.ListReplies(t.Context(), env.reader(), testScope, testTarget, second.ID, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, none.Data)
	})

	t.Run("a reply read that names no parent is refused rather than answered with the roots", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		written(t, env, store, newComment(testAuthor, "root"))

		_, err := store.ListReplies(t.Context(), env.reader(), testScope, testTarget, RootParentID, nil)
		must.ErrorIs(t, err, ErrEmptyParent)
	})
}

func runReadSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a discussion is scoped to its target and its tenant", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := written(t, env, store, newComment(testAuthor, "on recipe_1"))

		elsewhere := newComment(testAuthor, "on recipe_2")
		elsewhere.Target = Target{Type: recipeType, ID: "recipe_2"}
		written(t, env, store, elsewhere)

		nextDoor := newComment(testAuthor, "in another tenant")
		nextDoor.Scope = otherScope
		written(t, env, store, nextDoor)

		page, err := store.ListRootComments(t.Context(), env.reader(), testScope, testTarget, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, mine.ID, page.Data[0].ID)
	})

	t.Run("a target-type list spans every target of the type, replies included", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		root := written(t, env, store, newComment(testAuthor, "on recipe_1"))
		child := written(t, env, store, reply(root.ID, otherAuthor, "a reply"))

		second := newComment(testAuthor, "on recipe_2")
		second.Target = Target{Type: recipeType, ID: "recipe_2"}
		written(t, env, store, second)

		meal := newComment(testAuthor, "on a meal")
		meal.Target = Target{Type: mealType, ID: "meal_1"}
		written(t, env, store, meal)

		page, err := store.ListCommentsByTargetType(t.Context(), env.reader(), testScope, recipeType, nil)
		must.NoError(t, err)
		test.Eq(t, []string{root.ID, child.ID, second.ID}, ids(page.Data))
	})

	t.Run("an author's list is theirs alone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		mine := written(t, env, store, newComment(testAuthor, "mine"))
		written(t, env, store, newComment(otherAuthor, "theirs"))

		page, err := store.ListCommentsByAuthor(t.Context(), env.reader(), testScope, testAuthor, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, mine.ID, page.Data[0].ID)
	})

	t.Run("an archived comment is out of every list until it is asked for", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		c := written(t, env, store, newComment(testAuthor, "off topic"))
		must.NoError(t, env.archive(t, store, testScope, c.ID))

		hidden, err := store.ListCommentsByAuthor(t.Context(), env.reader(), testScope, testAuthor, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, hidden.Data)

		shown, err := store.ListCommentsByAuthor(t.Context(), env.reader(), testScope, testAuthor,
			&filtering.QueryFilter{IncludeArchived: pointer.To(true)})
		must.NoError(t, err)
		must.SliceLen(t, 1, shown.Data)
		must.NotNil(t, shown.Data[0].ArchivedAt)
	})

	t.Run("a descending page walks the same rows the other way", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first := written(t, env, store, newComment(testAuthor, "first"))
		second := written(t, env, store, newComment(testAuthor, "second"))

		descending := &filtering.QueryFilter{SortBy: filtering.SortDescending}

		page, err := store.ListRootComments(t.Context(), env.reader(), testScope, testTarget, descending)
		must.NoError(t, err)
		test.Eq(t, []string{second.ID, first.ID}, ids(page.Data))
	})

	t.Run("a page carries the count of everything it is a page of", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		for _, body := range []string{"one", "two", "three"} {
			written(t, env, store, newComment(testAuthor, body))
		}

		page, err := store.ListRootComments(t.Context(), env.reader(), testScope, testTarget,
			&filtering.QueryFilter{MaxResponseSize: pointer.To(uint16(2))})
		must.NoError(t, err)

		must.SliceLen(t, 2, page.Data)
		test.EqOp(t, uint64(3), page.FilteredCount)
	})

	t.Run("a read of an absent or another tenant's comment is the same answer", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		c := written(t, env, store, newComment(testAuthor, "mine"))

		_, err := store.GetComment(t.Context(), env.reader(), testScope, "no_such_comment")
		must.ErrorIs(t, err, ErrCommentNotFound)

		_, err = store.GetComment(t.Context(), env.reader(), otherScope, c.ID)
		must.ErrorIs(t, err, ErrCommentNotFound)
	})

	t.Run("a read that names nothing to read is refused", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.ListCommentsByAuthor(t.Context(), env.reader(), testScope, "", nil)
		must.ErrorIs(t, err, ErrEmptyAuthor)

		_, err = store.ListCommentsByTargetType(t.Context(), env.reader(), testScope, "", nil)
		must.ErrorIs(t, err, ErrEmptyTargetType)

		_, err = store.ListRootComments(t.Context(), env.reader(), testScope, Target{Type: recipeType}, nil)
		must.ErrorIs(t, err, ErrEmptyTargetID)
	})
}

func runSweepSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a sweep destroys every comment about one thing, archived and replies alike", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		root := written(t, env, store, newComment(testAuthor, "root"))
		written(t, env, store, reply(root.ID, otherAuthor, "child"))

		gone := written(t, env, store, newComment(testAuthor, "already archived"))
		must.NoError(t, env.archive(t, store, testScope, gone.ID))

		elsewhere := newComment(testAuthor, "about another recipe")
		elsewhere.Target = Target{Type: recipeType, ID: "recipe_2"}
		survivor := written(t, env, store, elsewhere)

		deleted := sweep(t, env, store, testScope, testTarget)
		test.EqOp(t, int64(3), deleted)

		page, err := store.ListCommentsByTargetType(t.Context(), env.reader(), testScope, recipeType, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, survivor.ID, page.Data[0].ID)
	})

	t.Run("a sweep of a type the catalog no longer holds still runs", func(t *testing.T) {
		t.Parallel()

		// The whole point of the sweep: a target type on its way out is exactly
		// the one whose rows have to be reachable.
		store, prefix := env.newStoreWithPrefix(t)
		written(t, env, store, newComment(testAuthor, "written while the type was live"))

		withdrawn, err := NewSQLStore(env.client,
			WithTablePrefix(prefix), WithTargets(Targets{mealType: {Description: "a meal"}}))
		must.NoError(t, err)

		test.EqOp(t, int64(1), sweep(t, env, withdrawn, testScope, testTarget))
	})

	t.Run("a sweep cannot reach another scope", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		c := written(t, env, store, newComment(testAuthor, "mine"))

		test.EqOp(t, int64(0), sweep(t, env, store, otherScope, testTarget))

		_, err := store.GetComment(t.Context(), env.reader(), testScope, c.ID)
		must.NoError(t, err)
	})

	t.Run("an erasure destroys one author's comments and nobody else's", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		written(t, env, store, newComment(testAuthor, "mine"))

		// Archived and still theirs: an erasure has to reach what a soft delete
		// hid, or a subject's words survive their own request.
		archived := written(t, env, store, newComment(testAuthor, "archived but still mine"))
		must.NoError(t, env.archive(t, store, testScope, archived.ID))

		theirs := written(t, env, store, newComment(otherAuthor, "theirs"))

		var deleted int64

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			var err error
			deleted, err = store.DeleteCommentsByAuthor(t.Context(), tx, testScope, testAuthor)

			return err
		}))

		test.EqOp(t, int64(2), deleted)

		mine, err := store.ListCommentsByAuthor(t.Context(), env.reader(), testScope, testAuthor, nil)
		must.NoError(t, err)
		test.SliceEmpty(t, mine.Data)

		survivor, err := store.GetComment(t.Context(), env.reader(), testScope, theirs.ID)
		must.NoError(t, err)
		test.EqOp(t, otherAuthor, survivor.Author)
	})

	t.Run("an erasure leaves the replies to what it erased", func(t *testing.T) {
		t.Parallel()

		// Cascading would mean erasing other people's words to satisfy one
		// person's request. The reply stays and its parent is gone, which is the
		// same state a moderator's archive produces.
		store := env.newStore(t)
		root := written(t, env, store, newComment(testAuthor, "root"))
		child := written(t, env, store, reply(root.ID, otherAuthor, "child"))

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := store.DeleteCommentsByAuthor(t.Context(), tx, testScope, testAuthor)

			return err
		}))

		replies, err := store.ListReplies(t.Context(), env.reader(), testScope, testTarget, root.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, replies.Data)
		test.EqOp(t, child.ID, replies.Data[0].ID)
	})

	t.Run("a subject who wrote nothing is not a failure", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		var deleted int64

		must.NoError(t, env.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			var err error
			deleted, err = store.DeleteCommentsByAuthor(t.Context(), tx, testScope, "never_said_anything")

			return err
		}))

		test.EqOp(t, int64(0), deleted)
	})

	t.Run("a delete outside a transaction is refused", func(t *testing.T) {
		t.Parallel()

		// Both deletes run inside somebody else's transaction — the sweep in the
		// one that removes the target, the erasure in the one that removes the
		// person — so neither reaches for the store's own writer.
		store := env.newStore(t)

		_, err := store.DeleteCommentsByAuthor(t.Context(), nil, testScope, testAuthor)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.DeleteCommentsForTarget(t.Context(), nil, testScope, testTarget)
		must.ErrorIs(t, err, ErrNilExecutor)
	})
}

// runTransactionSuite is the commit boundary, which is the whole of what this
// store's signatures buy its caller.
//
// Every write takes the caller's transaction and every read takes an executor, so
// what is under test here is not that the statements work — the other five suites
// cover that — but which side of a commit each of them lands on, and what a read
// handed the transaction can see. Those are the questions a store that opened its
// own transaction answered for its caller, and answered wrong.
func runTransactionSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("a write and a read inside one transaction observe each other", func(t *testing.T) {
		t.Parallel()

		// The property the reads were widened for, and the one no auto-committing
		// write could express: inside the transaction the comment is there, and
		// from outside it is not there yet. A read narrowed to the client's
		// reader would have been reading a database that does not hold the row
		// its own caller just wrote.
		store := env.newStore(t)

		created := newComment(testAuthor, "written and read on one executor")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if err := store.CreateComment(t.Context(), tx, testScope, created); err != nil {
				return err
			}

			read, err := store.GetComment(t.Context(), tx, testScope, created.ID)
			if err != nil {
				return err
			}

			test.EqOp(t, "written and read on one executor", read.Body)

			page, err := store.ListRootComments(t.Context(), tx, testScope, testTarget, nil)
			if err != nil {
				return err
			}

			test.Eq(t, []string{created.ID}, ids(page.Data))

			// And the same read, on the client, cannot see it: the transaction
			// has not committed, so this is the other half of the same fact
			// rather than a second one.
			outside, err := store.ListRootComments(t.Context(), env.reader(), testScope, testTarget, nil)
			if err != nil {
				return err
			}

			test.SliceEmpty(t, outside.Data)

			return nil
		}))

		// After the commit both executors agree, which is what makes the reading
		// above about visibility rather than about two different rows.
		read, err := store.GetComment(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, created.ID, read.ID)
	})

	t.Run("the three writes commit with the caller's transaction", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		created := newComment(testAuthor, "written inside")
		edited := written(t, env, store, newComment(testAuthor, "before the edit"))
		doomed := written(t, env, store, newComment(testAuthor, "on the way out"))

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if err := store.CreateComment(t.Context(), tx, testScope, created); err != nil {
				return err
			}

			edited.Body = "after the edit"
			if err := store.UpdateComment(t.Context(), tx, testScope, edited); err != nil {
				return err
			}

			return store.ArchiveComment(t.Context(), tx, testScope, doomed.ID)
		}))

		// The create reads its creation time back through the caller's executor,
		// so the value the caller is handed is the row this transaction wrote
		// rather than a zero time waiting on a commit.
		test.NotEqOp(t, "", created.ID)
		test.False(t, created.CreatedAt.IsZero())

		read, err := store.GetComment(t.Context(), env.reader(), testScope, created.ID)
		must.NoError(t, err)
		test.EqOp(t, "written inside", read.Body)

		read, err = store.GetComment(t.Context(), env.reader(), testScope, edited.ID)
		must.NoError(t, err)
		test.EqOp(t, "after the edit", read.Body)

		_, err = store.GetComment(t.Context(), env.reader(), testScope, doomed.ID)
		must.ErrorIs(t, err, ErrCommentNotFound)
	})

	t.Run("a rolled back transaction takes all three writes with it", func(t *testing.T) {
		t.Parallel()

		// This is the whole point of the signature, seen from the side that
		// matters: the consumer's companion write fails, and the comment goes
		// back with it rather than surviving in a transaction it was never part
		// of.
		store := env.newStore(t)

		created := newComment(testAuthor, "never committed")
		edited := written(t, env, store, newComment(testAuthor, "the original"))
		doomed := written(t, env, store, newComment(testAuthor, "still here"))

		err := env.inTx(t, func(tx database.Tx) error {
			if txErr := store.CreateComment(t.Context(), tx, testScope, created); txErr != nil {
				return txErr
			}

			edited.Body = "the edit"
			if txErr := store.UpdateComment(t.Context(), tx, testScope, edited); txErr != nil {
				return txErr
			}

			if txErr := store.ArchiveComment(t.Context(), tx, testScope, doomed.ID); txErr != nil {
				return txErr
			}

			return errCompanionWrite
		})
		must.ErrorIs(t, err, errCompanionWrite)

		// The id was minted onto the caller's value on the way through. Nothing
		// undoes that, and nothing should: what rolled back is the row.
		test.NotEqOp(t, "", created.ID)

		_, err = store.GetComment(t.Context(), env.reader(), testScope, created.ID)
		must.ErrorIs(t, err, ErrCommentNotFound)

		read, err := store.GetComment(t.Context(), env.reader(), testScope, edited.ID)
		must.NoError(t, err)
		test.EqOp(t, "the original", read.Body)

		read, err = store.GetComment(t.Context(), env.reader(), testScope, doomed.ID)
		must.NoError(t, err)
		test.EqOp(t, "still here", read.Body)
		test.Nil(t, read.ArchivedAt)
	})

	t.Run("a reply finds a parent written in the same transaction", func(t *testing.T) {
		t.Parallel()

		// The parent read runs on the caller's executor, so a discussion opened
		// and answered in one transaction resolves instead of reporting the
		// parent absent.
		store := env.newStore(t)

		root := newComment(testAuthor, "root")
		child := reply("", otherAuthor, "child")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			if err := store.CreateComment(t.Context(), tx, testScope, root); err != nil {
				return err
			}

			child.ParentID = root.ID

			return store.CreateComment(t.Context(), tx, testScope, child)
		}))

		// And it adopted the target it never named, which it could only do
		// having read the parent.
		test.EqOp(t, testTarget, child.Target)

		replies, err := store.ListReplies(t.Context(), env.reader(), testScope, testTarget, root.ID, nil)
		must.NoError(t, err)
		must.SliceLen(t, 1, replies.Data)
		test.EqOp(t, child.ID, replies.Data[0].ID)
	})

	t.Run("every method refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		// Every one of the ten, not a representative one. There is no connection
		// of the store's own to fall back to, so a method that did anything but
		// refuse would be reaching for something that is not there.
		store := env.newStore(t)

		must.ErrorIs(t,
			store.CreateComment(t.Context(), nil, testScope, newComment(testAuthor, "words")),
			ErrNilExecutor)
		must.ErrorIs(t,
			store.UpdateComment(t.Context(), nil, testScope, newComment(testAuthor, "words")),
			ErrNilExecutor)
		must.ErrorIs(t,
			store.ArchiveComment(t.Context(), nil, testScope, "cmt_1"),
			ErrNilExecutor)

		_, err := store.DeleteCommentsForTarget(t.Context(), nil, testScope, testTarget)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.DeleteCommentsByAuthor(t.Context(), nil, testScope, testAuthor)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.GetComment(t.Context(), nil, testScope, "cmt_1")
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListRootComments(t.Context(), nil, testScope, testTarget, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListReplies(t.Context(), nil, testScope, testTarget, "cmt_1", nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListCommentsByTargetType(t.Context(), nil, testScope, recipeType, nil)
		must.ErrorIs(t, err, ErrNilExecutor)

		_, err = store.ListCommentsByAuthor(t.Context(), nil, testScope, testAuthor, nil)
		must.ErrorIs(t, err, ErrNilExecutor)
	})

	t.Run("a refused write inside a transaction leaves the transaction usable", func(t *testing.T) {
		t.Parallel()

		// Every check the writes make runs before any statement they would send,
		// so a refusal is the store declining rather than the database aborting.
		// A caller that inspects one and carries on has a transaction to carry on
		// in, which is what lets these be collected here and asserted outside.
		store := env.newStore(t)

		unknown := newComment(testAuthor, "about something this application does not have")
		unknown.Target = Target{Type: unknownType, ID: "whatever_1"}

		elsewhere := newComment(testAuthor, "belonging to somebody else")
		elsewhere.Scope = otherScope

		var (
			nilCreate, unknownCreate, mismatchedCreate error
			nilUpdate, emptyBody, missingUpdate        error
			missingArchive                             error
		)

		survivor := newComment(testAuthor, "written after all the refusals")

		must.NoError(t, env.inTx(t, func(tx database.Tx) error {
			nilCreate = store.CreateComment(t.Context(), tx, testScope, nil)
			unknownCreate = store.CreateComment(t.Context(), tx, testScope, unknown)
			mismatchedCreate = store.CreateComment(t.Context(), tx, testScope, elsewhere)

			nilUpdate = store.UpdateComment(t.Context(), tx, testScope, nil)

			silent := newComment(testAuthor, "  ")
			silent.ID = "cmt_never_written"
			emptyBody = store.UpdateComment(t.Context(), tx, testScope, silent)

			absent := newComment(testAuthor, "an edit to nothing")
			absent.ID = "cmt_never_written"
			missingUpdate = store.UpdateComment(t.Context(), tx, testScope, absent)

			missingArchive = store.ArchiveComment(t.Context(), tx, testScope, "cmt_never_written")

			return store.CreateComment(t.Context(), tx, testScope, survivor)
		}))

		must.ErrorIs(t, nilCreate, ErrNilComment)
		must.ErrorIs(t, unknownCreate, ErrUnknownTargetType)
		must.ErrorIs(t, mismatchedCreate, ErrScopeMismatch)
		must.ErrorIs(t, nilUpdate, ErrNilComment)
		must.ErrorIs(t, emptyBody, ErrEmptyBody)
		must.ErrorIs(t, missingUpdate, ErrCommentNotFound)
		must.ErrorIs(t, missingArchive, ErrCommentNotFound)

		read, err := store.GetComment(t.Context(), env.reader(), testScope, survivor.ID)
		must.NoError(t, err)
		test.EqOp(t, "written after all the refusals", read.Body)
	})

	t.Run("the existence hook gates the write and reads outside the transaction", func(t *testing.T) {
		t.Parallel()

		// The hook takes a scope and an id and no executor, so whatever it reads,
		// it does not read through the caller's transaction. That is the
		// documented limit of CreateComment, and this is what it looks like: the
		// check still gates the write, and it still answers for the world as it
		// was committed.
		check := newRecordingCheck(false, nil)
		store := env.newStore(t, WithTargets(Targets{
			recipeType: {Description: "a recipe", Exists: check.exists},
		}))

		err := env.inTx(t, func(tx database.Tx) error {
			return store.CreateComment(t.Context(), tx, testScope,
				newComment(testAuthor, "about a recipe nobody can find"))
		})
		must.ErrorIs(t, err, ErrTargetNotFound)

		must.SliceLen(t, 1, check.asked)
		test.EqOp(t, testTarget.ID, check.asked[0])
		test.EqOp(t, testScope, check.scopes[0])
	})
}

// sweep runs DeleteCommentsForTarget in its own transaction and returns the
// count, since every caller of it here wants exactly that.
func sweep(t *testing.T, env *storeEnv, store *SQLStore, scope tenancy.Scope, target Target) int64 {
	t.Helper()

	var deleted int64

	must.NoError(t, env.inTx(t, func(tx database.Tx) error {
		var err error
		deleted, err = store.DeleteCommentsForTarget(t.Context(), tx, scope, target)

		return err
	}))

	return deleted
}

// ids is the ids of a page, in the order it came back.
func ids(page []*Comment) []string {
	out := make([]string, 0, len(page))
	for _, c := range page {
		out = append(out, c.ID)
	}

	return out
}

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		must.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("refuses a prefix that would not render an identifier", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		_, err := NewSQLStore(env.client, WithTablePrefix("no-hyphens-allowed"))
		test.Error(t, err)
	})

	T.Run("reports the prefix it was built with", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, WithTablePrefix("cm"))
		must.NoError(t, err)
		test.EqOp(t, "cm", store.TablePrefix())
	})

	T.Run("names the dialect it has no statements for", func(t *testing.T) {
		t.Parallel()

		_, err := commentsdbDialect(dialect.Dialect("cassandra"))
		must.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}
