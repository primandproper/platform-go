package assembled_test

import (
	"path/filepath"
	"testing"

	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/database/dialect"
)

// TestConformance_AssembledSQLite boots a service over SQLite.
//
// It is the assembled subject with no Docker, so it runs wherever the direct
// subjects do. What it cannot prove is anything the database engine decides; the
// real servers in containers_test.go are for that.
func TestConformance_AssembledSQLite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "assembled.db")

	assemble(t, &databasecfg.Config{
		Provider:        databasecfg.ProviderSQLite,
		ReadConnection:  databasecfg.ConnectionDetails{Database: path},
		WriteConnection: databasecfg.ConnectionDetails{Database: path},
		// SQLite has one writer. A pool wider than one is a pool of
		// connections waiting on each other's locks, which is a SQLite
		// deployment's configuration rather than this harness's workaround.
		MaxOpenConns: 1,
	}, dialect.SQLite)
}
