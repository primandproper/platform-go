package commentscfg

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/primandproper/platform-go/v14/comments"

	"github.com/primandproper/primitives-go/v2/database"
	databasecfg "github.com/primandproper/primitives-go/v2/database/config"

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

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue(i, testTargets)

		RegisterStore(i)

		store, err := do.Invoke[comments.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("with no observability registered", func(t *testing.T) {
		t.Parallel()

		// A container that registers no pillars still wires up: absent is fine,
		// and only a registered one that fails to build is an error.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{TablePrefix: "ddb"})
		do.ProvideValue(i, testTargets)

		RegisterStore(i)

		store, err := do.Invoke[comments.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("errors when no comments.Targets is registered", func(t *testing.T) {
		t.Parallel()

		// The catalog is the application's declaration of what can be commented
		// on, each type optionally carrying a function that reads the
		// application's own tables — so there is nothing an environment variable
		// could have supplied, and a container that registers none is one whose
		// store would have refused every write.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})

		RegisterStore(i)

		_, err := do.Invoke[comments.Store](i)
		must.Error(t, err)
	})

	T.Run("surfaces a bad config", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{TablePrefix: "has space"})
		do.ProvideValue(i, testTargets)

		RegisterStore(i)

		_, err := do.Invoke[comments.Store](i)
		must.Error(t, err)
	})
}
