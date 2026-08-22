package operations

import (
	stderrors "errors"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestUnretryable(T *testing.T) {
	T.Parallel()

	T.Run("nil stays nil", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, Unretryable(nil))
		test.False(t, IsUnretryable(nil))
	})

	T.Run("marks", func(t *testing.T) {
		t.Parallel()

		test.True(t, IsUnretryable(Unretryable(platformerrors.New("no"))))
		test.False(t, IsUnretryable(platformerrors.New("no")))
	})

	// The reason Unretryable is a wrapper type rather than a joined sentinel:
	// a Runner marking its own sentinel must not lose the sentinel by doing so,
	// or every errors.Is above it starts failing for a reason nobody will guess.
	T.Run("the marked error is still itself", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("no such subject")

		marked := Unretryable(sentinel)

		test.True(t, stderrors.Is(marked, sentinel))
		test.EqOp(t, sentinel.Error(), marked.Error())
	})

	T.Run("survives further wrapping", func(t *testing.T) {
		t.Parallel()

		// A Runner marks, and something above it wraps for context. The mark has
		// to survive that, or the classification depends on how carefully the
		// call stack was written.
		wrapped := platformerrors.Wrap(Unretryable(platformerrors.New("no")), "collecting")

		test.True(t, IsUnretryable(wrapped))
	})
}

func TestFail(T *testing.T) {
	T.Parallel()

	T.Run("carries the code", func(t *testing.T) {
		t.Parallel()

		err := Fail("no_such_subject", "no subject %q", "abc")

		must.Error(t, err)
		test.EqOp(t, "no_such_subject", codeOf(err))
		test.EqOp(t, `no subject "abc"`, err.Error())
	})

	T.Run("the code survives wrapping", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "boom", codeOf(platformerrors.Wrap(Fail("boom", "went wrong"), "running")))
	})

	T.Run("an unclassified error has no code", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", codeOf(platformerrors.New("went wrong")))
		test.EqOp(t, "", codeOf(nil))
	})
}

func TestWithCode(T *testing.T) {
	T.Parallel()

	T.Run("nil stays nil", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, WithCode("boom", nil))
	})

	T.Run("classifies without replacing", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("upstream is down")

		coded := WithCode("dependency_down", sentinel)

		test.EqOp(t, "dependency_down", codeOf(coded))
		test.True(t, stderrors.Is(coded, sentinel))
	})

	// The two markers compose, and have to: "this failed for reason X, and it
	// will fail the same way next time" is the single most useful thing a Runner
	// can say about a failure.
	T.Run("composes with Unretryable", func(t *testing.T) {
		t.Parallel()

		err := Unretryable(Fail("no_such_subject", "no such subject"))

		test.True(t, IsUnretryable(err))
		test.EqOp(t, "no_such_subject", codeOf(err))
	})
}
