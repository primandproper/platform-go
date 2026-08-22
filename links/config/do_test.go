package linkscfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	databasecfg "github.com/primandproper/platform-go/v13/database/config"
	"github.com/primandproper/platform-go/v13/links"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func testDBClient(t *testing.T) database.Client {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	client, err := databasecfg.NewDatabase(t.Context(), &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
	}, nil)
	must.NoError(t, err)

	return client
}

func TestRegisterMinter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, memoryConfig())

		RegisterMinter(i)

		minter, err := do.Invoke[*links.Minter](i)
		must.NoError(t, err)
		test.NotNil(t, minter)
	})

	T.Run("wires up with no observability registered", func(t *testing.T) {
		t.Parallel()

		// A container that registers no pillars still resolves: absent is not
		// an error, only a registered pillar that fails to build is.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, memoryConfig())

		RegisterMinter(i)

		_, err := do.Invoke[*links.Minter](i)
		test.NoError(t, err)
	})
}
