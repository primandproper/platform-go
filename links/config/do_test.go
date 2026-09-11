package linkscfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/links/database/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newLinksDB builds a throwaway SQLite client with the action link table
// created, which is the pair of things every Minter this package assembles now
// needs.
func newLinksDB(t *testing.T) database.Client {
	t.Helper()

	client := testDBClient(t)
	must.NoError(t, createLinksTable(t, client))

	return client
}

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

// createLinksTable runs the shipped DDL against a client, which is what a
// consumer does through their own migration run.
func createLinksTable(t *testing.T, client database.Client) error {
	t.Helper()

	stmts, err := migrations.Statements(dialect.SQLite, "")
	if err != nil {
		return err
	}

	for _, stmt := range stmts {
		if _, execErr := client.Writer().ExecContext(t.Context(), stmt); execErr != nil {
			return execErr
		}
	}

	return nil
}

func TestRegisterMinter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, newLinksDB(t))
		do.ProvideValue(i, testConfig())

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
		do.ProvideValue[database.Client](i, newLinksDB(t))
		do.ProvideValue(i, testConfig())

		RegisterMinter(i)

		_, err := do.Invoke[*links.Minter](i)
		test.NoError(t, err)
	})
}
