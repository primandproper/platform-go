package database

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/authorization/database/migrations"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/mysql"
	"github.com/primandproper/platform-go/v13/database/postgres"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/testutils/containers/mysqltest"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// dialectEnv is one dialect's worth of wiring for the shared suite.
type dialectEnv struct {
	client  database.Client
	dialect dialect.Dialect
	prefix  string
}

// waitForServer polls until the server accepts a trivial statement.
//
// A container's readiness log precedes it actually accepting connections —
// MySQL's entrypoint in particular logs "ready for connections" and then
// restarts — and NewDatabaseClient does not ping on construction. Without this
// the first DDL statement lands on a socket that is still closing and fails
// with an unhelpful "invalid connection". IsReady would say the same thing, but
// it lives on the concrete clients rather than database.Client.
func waitForServer(tb testing.TB, ctx context.Context, q database.SQLQueryExecutor) {
	tb.Helper()

	var lastErr error
	for range 30 {
		if _, err := q.ExecContext(ctx, "SELECT 1"); err == nil {
			return
		} else { //nolint:revive // the error is only reported if every attempt fails
			lastErr = err
		}

		time.Sleep(time.Second)
	}

	tb.Fatalf("database never accepted a statement: %v", lastErr)
}

// runDialectSuite is the same behavioral suite the SQLite tests run, executed
// against a real server.
//
// SQLite already covers the logic; what only a real server can validate is
// whether the recursive CTE, the multi-row inserts, and the DDL are actually
// accepted by Postgres and MySQL. MySQL in particular restricts where a
// recursive CTE's self-reference may appear, and that restriction is invisible
// until MySQL parses the query.
func runDialectSuite(t *testing.T, env *dialectEnv) {
	t.Helper()

	ctx := t.Context()

	waitForServer(t, ctx, env.client.Writer())

	stmts, err := migrations.Statements(env.dialect, env.prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := env.client.Writer().ExecContext(ctx, stmt)
		must.NoError(t, execErr)
	}

	r, err := NewResolver(
		&Config{Dialect: env.dialect, TablePrefix: env.prefix},
		env.client.Writer(),
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
	)
	must.NoError(t, err)

	roles := testRoles()

	must.NoError(t, env.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		return r.Seed(ctx, q, roles...)
	}))

	t.Run("resolves inheritance through the recursive CTE", func(t *testing.T) {
		set, resolveErr := r.PermissionsForRoles(ctx, "service_admin")
		must.NoError(t, resolveErr)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permWrite, permDelete)))
	})

	// The property that makes the backends interchangeable, asserted against a
	// real server rather than only against SQLite.
	t.Run("agrees with the reference expansion", func(t *testing.T) {
		expected, expandErr := authorization.ExpandInheritance(roles...)
		must.NoError(t, expandErr)

		for i := range roles {
			name := roles[i].Name

			got, resolveErr := r.PermissionsForRoles(ctx, name)
			must.NoError(t, resolveErr)

			if !got.Equal(expected[name]) {
				t.Errorf("role %q: server resolved %v, reference expansion resolved %v",
					name, got.Slice(), expected[name].Slice())
			}
		}
	})

	t.Run("unions several roles", func(t *testing.T) {
		set, resolveErr := r.PermissionsForRoles(ctx, "admin", "auditor")
		must.NoError(t, resolveErr)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permWrite)))
	})

	t.Run("unknown roles contribute nothing", func(t *testing.T) {
		set, resolveErr := r.PermissionsForRoles(ctx, "ghost")
		must.NoError(t, resolveErr)

		test.True(t, set.IsEmpty())
	})

	t.Run("reports the declared policy", func(t *testing.T) {
		got, rolesErr := r.Roles(ctx)
		must.NoError(t, rolesErr)

		test.SliceLen(t, len(roles), got)
	})

	t.Run("seeding is idempotent", func(t *testing.T) {
		must.NoError(t, env.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
			return r.Seed(ctx, q, roles...)
		}))

		got, rolesErr := r.Roles(ctx)
		must.NoError(t, rolesErr)
		test.SliceLen(t, len(roles), got)
	})

	// Exercises the chunked multi-row inserts against a server's real
	// bind-parameter limits and its own view of statement size.
	t.Run("handles a policy larger than one insert batch", func(t *testing.T) {
		const permCount = 250

		perms := make([]authorization.Permission, 0, permCount)
		for i := range permCount {
			perms = append(perms, authorization.Permission(fmt.Sprintf("bulk.thing_%03d", i)))
		}

		must.NoError(t, env.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
			return r.Seed(ctx, q, authorization.Role{Name: "bulk", Permissions: perms})
		}))

		set, resolveErr := r.PermissionsForRoles(ctx, "bulk")
		must.NoError(t, resolveErr)

		test.EqOp(t, permCount, set.Len())
	})

	t.Run("upsert validates against stored policy", func(t *testing.T) {
		err = env.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
			return r.UpsertRole(ctx, q, authorization.Role{
				Name:     "member",
				Inherits: []string{"service_admin"},
			})
		})

		test.ErrorIs(t, err, authorization.ErrInheritanceCycle)
	})

	t.Run("archiving an ancestor revokes downstream", func(t *testing.T) {
		must.NoError(t, env.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
			return r.ArchiveRole(ctx, q, "member")
		}))

		set, resolveErr := r.PermissionsForRoles(ctx, "admin")
		must.NoError(t, resolveErr)

		test.True(t, set.Equal(authorization.NewPermissionSet(permWrite)))
	})
}

func TestAuthorizationDatabase_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, &dialectEnv{
			dialect: dialect.Postgres,
			prefix:  DefaultTablePrefix,
			client:  client,
		})
	})
}

// runWithMySQL boots a MySQL container via mysqltest and hands its closure a
// database.Client against it.
func runWithMySQL(tb testing.TB, fn func(ctx context.Context, client database.Client)) {
	tb.Helper()

	mysqltest.Run(tb, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString})
		must.NoError(tb, err)
		tb.Cleanup(func() { _ = client.Close() })

		fn(ctx, client)
	}, mysqltest.WithCredentials("authztest", "authztest", "authztest"))
}

func TestAuthorizationDatabase_MySQL(T *testing.T) {
	T.Parallel()

	runWithMySQL(T, func(_ context.Context, client database.Client) {
		runDialectSuite(T, &dialectEnv{
			dialect: dialect.MySQL,
			prefix:  DefaultTablePrefix,
			client:  client,
		})
	})
}
