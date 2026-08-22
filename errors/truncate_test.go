package errors_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
)

func TestTruncateError(T *testing.T) {
	T.Parallel()

	T.Run("renders a nil error as empty", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", errors.TruncateError(nil, 100))
	})

	T.Run("leaves a short message alone", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "boom", errors.TruncateError(errors.New("boom"), 100))
	})

	T.Run("bounds a long message", func(t *testing.T) {
		t.Parallel()

		got := errors.TruncateError(errors.New(strings.Repeat("x", 4096)), 1024)

		test.EqOp(t, 1024, len(got))
	})

	T.Run("cuts a multi-byte message on a rune boundary", func(t *testing.T) {
		t.Parallel()

		// The regression: a byte-wise cut here writes half a rune into the
		// column, which a UTF-8 column rejects outright.
		got := errors.TruncateError(errors.New(strings.Repeat("é", 100)), 11)

		test.True(t, len(got) <= 11)
		test.True(t, utf8.ValidString(got))
		test.EqOp(t, strings.Repeat("é", 5), got)
	})
}
