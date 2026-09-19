package shredding

import (
	"strings"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSubject_validate pins the two bounds on the pair this table is keyed on.
//
// The mint is an insert-ignore, and MySQL's IGNORE downgrades a value too long
// for its column to a warning that truncates and stores it. Here that is worse
// than wrong data: two subjects sharing a 255-byte id prefix would collapse onto
// one row, the second mint would report zero affected and read back the first
// subject's key, and shredding either would destroy the key the other's
// ciphertext depends on.
//
// Each refusal case is one byte over its column, because one byte over is the
// case a limit written down as the wrong number still passes.
func TestSubject_validate(T *testing.T) {
	T.Parallel()

	T.Run("admits a subject that names somebody", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, Subject{Type: "user", ID: "user_1"}.validate())
	})

	T.Run("refuses a subject with no ID", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, Subject{Type: "user"}.validate(), ErrEmptySubjectID)
	})

	T.Run("refuses an ID one byte over the column", func(t *testing.T) {
		t.Parallel()

		err := Subject{Type: "user", ID: strings.Repeat("i", MaxSubjectIDLength+1)}.validate()

		test.ErrorIs(t, err, ErrSubjectValueTooLong)

		// Answered as a bad request by the platform mapper rather than by a case
		// of this package's own — see internal/sentinelmatrix.
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)

		// The message says which of the two it was, because "too long" with two
		// candidates is a refusal the caller has to guess at.
		test.StrContains(t, err.Error(), "id")
	})

	T.Run("refuses a type one byte over the column", func(t *testing.T) {
		t.Parallel()

		err := Subject{Type: strings.Repeat("t", MaxSubjectTypeLength+1), ID: "user_1"}.validate()

		test.ErrorIs(t, err, ErrSubjectValueTooLong)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)
		test.StrContains(t, err.Error(), "type")
	})

	T.Run("admits a subject exactly at each limit", func(t *testing.T) {
		t.Parallel()

		subject := Subject{
			Type: strings.Repeat("t", MaxSubjectTypeLength),
			ID:   strings.Repeat("i", MaxSubjectIDLength),
		}

		must.NoError(t, subject.validate())
	})

	// The empty type is its own namespace rather than an unset field, so the
	// bound must not turn it into a refusal.
	T.Run("admits a subject with no type", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, Subject{ID: "user_1"}.validate())
	})
}
