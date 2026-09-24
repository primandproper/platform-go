package assembled_test

import (
	"context"
	"net"
	"strconv"
	"testing"

	databasecfg "github.com/primandproper/primitives-go/v2/database/config"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/shoenig/test/must"
)

// The environment variables that point this suite at a server somebody else
// provided. Unset, each starts a container; set, the identical assertions run
// against whatever is on the other end. They are the direct subjects' variables,
// so one CI job's databases serve both.
const (
	postgresDSNEnv = "CONFORMANCE_POSTGRES_DSN"
	mysqlDSNEnv    = "CONFORMANCE_MYSQL_DSN"
)

// TestConformance_AssembledRealServers boots a service over Postgres and over
// MySQL 8.
//
// The connection reaches the service the way a deployment's does, as the
// DATABASE_* fields of a databasecfg.Config, rather than as a client this harness
// opened. That is the point of the subject: which client the composition root
// builds from a config is part of what it composes.
//
// It gates on RUN_CONTAINER_TESTS through pgtest and mysqltest, except where a
// DSN names a server, which starts nothing and so has nothing to gate.
func TestConformance_AssembledRealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			var conn databasecfg.ConnectionDetails
			must.NoError(t, conn.LoadFromURL(pg.ConnectionString))

			assemble(t, &databasecfg.Config{
				Provider:        databasecfg.ProviderPostgres,
				ReadConnection:  conn,
				WriteConnection: conn,
			}, dialect.Postgres)
		}, pgtest.WithDSNFromEnv(postgresDSNEnv))
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		mysqltest.Run(t, func(_ context.Context, my *mysqltest.Instance) {
			conn := mysqlConnection(t, my.ConnectionString)

			assemble(t, &databasecfg.Config{
				Provider:        databasecfg.ProviderMySQL,
				ReadConnection:  conn,
				WriteConnection: conn,
			}, dialect.MySQL)
		}, mysqltest.WithDSNFromEnv(mysqlDSNEnv))
	})
}

// mysqlConnection reads a MySQL DSN into the fields a deployment configures.
//
// A MySQL DSN is not a URL, so LoadFromURL cannot read it; the driver's own
// parser can. The parameters the DSN carried are not kept: databasecfg writes
// the ones a deployment gets, and those are what is under test.
func mysqlConnection(t *testing.T, dsn string) databasecfg.ConnectionDetails {
	t.Helper()

	parsed, err := mysqldriver.ParseDSN(dsn)
	must.NoError(t, err)

	host, portText, err := net.SplitHostPort(parsed.Addr)
	must.NoError(t, err)

	port, err := strconv.ParseUint(portText, 10, 16)
	must.NoError(t, err)

	return databasecfg.ConnectionDetails{
		Username: parsed.User,
		Password: parsed.Passwd,
		Database: parsed.DBName,
		Host:     host,
		Port:     uint16(port),
	}
}
