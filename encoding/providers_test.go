package encoding

import (
	"testing"

	"github.com/shoenig/test"
)

func TestNewContentType(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, ContentTypeJSON, NewContentType(Config{ContentType: "application/json"}))
	})
}
