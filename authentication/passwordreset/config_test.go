package passwordreset

import (
	"strings"
	"testing"

	"github.com/primandproper/primitives-go/v2/database/ddl"

	"github.com/shoenig/test"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("with no prefix", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
	})

	T.Run("with a namespace", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Config{TablePrefix: "ddb"}).ValidateWithContext(t.Context()))
	})

	T.Run("with a trailing separator", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{TablePrefix: "ddb_"}).ValidateWithContext(t.Context()))
	})

	T.Run("with an illegal identifier", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{TablePrefix: "has spaces"}).ValidateWithContext(t.Context()))
	})

	// The reason the prefix is vetted against the schema rather than a pattern:
	// this one is a legal name on its own and pushes the expiry index past the
	// limit.
	T.Run("with a prefix that overruns an index name", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{
			TablePrefix: strings.Repeat("a", ddl.MaxIdentifierLength),
		}).ValidateWithContext(t.Context()))
	})
}
