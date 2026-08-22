package database

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/database/ddl"

	"github.com/shoenig/test"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts an empty prefix, which is the ordinary case", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
	})

	T.Run("accepts a namespace the schema can render", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Config{TablePrefix: "ddb"}).ValidateWithContext(t.Context()))
	})

	// Vetted against the schema rather than against a pattern: a prefix that is
	// a legal identifier on its own can still push the index name past what the
	// engines accept, and that failure would otherwise surface as a migration
	// that half ran.
	T.Run("rejects a namespace that renders an over-long identifier", func(t *testing.T) {
		t.Parallel()

		err := (&Config{TablePrefix: strings.Repeat("a", ddl.MaxIdentifierLength)}).ValidateWithContext(t.Context())

		// The message rather than the sentinel: ozzo collects field errors into
		// a map whose Error joins them, and nothing in that chain unwraps.
		test.ErrorContains(t, err, ddl.ErrPrefixTooLong.Error())
	})

	T.Run("rejects a namespace that ends in the separator", func(t *testing.T) {
		t.Parallel()

		err := (&Config{TablePrefix: "ddb_"}).ValidateWithContext(t.Context())
		test.ErrorContains(t, err, ddl.ErrPrefixTrailingSeparator.Error())
	})
}
