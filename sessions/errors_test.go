package sessions

import (
	stderrors "errors"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
)

// The chain is the package's contract with callers: pick the resolution you
// care about and check exactly that one. A middleware deciding whether to
// redirect checks ErrNotFound; a page that wants to explain the sign-out checks
// ErrIdleTimeout. If the wrapping breaks, both keep compiling and one starts
// answering wrong.
func TestErrorChain(T *testing.T) {
	T.Parallel()

	T.Run("every expiry reason is an expiry", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ErrIdleTimeout, ErrExpired)
		test.ErrorIs(t, ErrAbsoluteTimeout, ErrExpired)
	})

	T.Run("every expiry is an absence", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ErrExpired, ErrNotFound)
		test.ErrorIs(t, ErrIdleTimeout, ErrNotFound)
		test.ErrorIs(t, ErrAbsoluteTimeout, ErrNotFound)
	})

	// The other direction has to stay false, or a caller checking for an idle
	// timeout would match a session that simply never existed.
	T.Run("an absence is not an expiry", func(t *testing.T) {
		t.Parallel()

		test.False(t, stderrors.Is(ErrNotFound, ErrExpired))
		test.False(t, stderrors.Is(ErrExpired, ErrIdleTimeout))
		test.False(t, stderrors.Is(ErrIdleTimeout, ErrAbsoluteTimeout))
	})

	T.Run("input sentinels wrap the platform ones", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, ErrIDRequired, platformerrors.ErrEmptyInputParameter)
		test.ErrorIs(t, ErrNilBackend, platformerrors.ErrNilInputParameter)
	})

	// An identifier conflict is emphatically not an absence: reporting it as
	// one would make a Create that hit an existing session look like a session
	// that had ended.
	T.Run("an identifier conflict stands alone", func(t *testing.T) {
		t.Parallel()

		test.False(t, stderrors.Is(ErrIDConflict, ErrNotFound))
	})
}
