package rbac

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/rbac/migrations"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

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

	must.NoError(t, env.client.WithTransaction(ctx, func(q database.Tx) error {
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
		must.NoError(t, env.client.WithTransaction(ctx, func(q database.Tx) error {
			return r.Seed(ctx, q, roles...)
		}))

		got, rolesErr := r.Roles(ctx)
		must.NoError(t, rolesErr)
		test.SliceLen(t, len(roles), got)
	})

	// Exercises the one batch that still reaches a server whole — the name
	// lookup's bound set — against that server's real bind-parameter limit and
	// its own view of statement size.
	t.Run("handles a policy of a few hundred permissions", func(t *testing.T) {
		const permCount = 250

		perms := make([]authorization.Permission, 0, permCount)
		for i := range permCount {
			perms = append(perms, authorization.Permission(fmt.Sprintf("bulk.thing_%03d", i)))
		}

		must.NoError(t, env.client.WithTransaction(ctx, func(q database.Tx) error {
			return r.Seed(ctx, q, authorization.Role{Name: "bulk", Permissions: perms})
		}))

		set, resolveErr := r.PermissionsForRoles(ctx, "bulk")
		must.NoError(t, resolveErr)

		test.EqOp(t, permCount, set.Len())
	})

	t.Run("upsert validates against stored policy", func(t *testing.T) {
		err = env.client.WithTransaction(ctx, func(q database.Tx) error {
			return r.UpsertRole(ctx, q, authorization.Role{
				Name:     "member",
				Inherits: []string{"service_admin"},
			})
		})

		test.ErrorIs(t, err, authorization.ErrInheritanceCycle)
	})

	t.Run("archiving an ancestor revokes downstream", func(t *testing.T) {
		must.NoError(t, env.client.WithTransaction(ctx, func(q database.Tx) error {
			return r.ArchiveRole(ctx, q, "member")
		}))

		set, resolveErr := r.PermissionsForRoles(ctx, "admin")
		must.NoError(t, resolveErr)

		test.True(t, set.Equal(authorization.NewPermissionSet(permWrite)))
	})
}

// runConcurrentSeeds is the deployment shape Seed's doc invites, run against
// a real server: every replica of a service migrating and then seeding the
// same policy at the same moment, with nothing but the database between them.
//
// It needs a real server because it needs real concurrency. SQLite admits one
// writer at a time and the pool of one the other suites run through admits
// one transaction at a time, so the race this exercises — a writer whose
// lookup ran before another's commit, and a grant written between another's
// DELETE and INSERT — cannot happen anywhere else. What is asserted is that
// every seed commits, retrying where the engine refused one as Seed says a
// caller should, and that what they converged on is the policy each was given,
// on a first seed where every name is minted at once, on a re-seed where the
// grants are cleared and rewritten under one another, and on a re-seed that
// changes the policy.
//
// Convergence is the subject rather than first-attempt success. Whether a given
// replica needed one attempt or two is the engine's business and Seed's
// documentation says so; what must hold either way is that no policy lands half
// applied and that all four replicas end up describing the same one.
func runConcurrentSeeds(t *testing.T, env *dialectEnv) {
	t.Helper()

	const seeders = 4

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

	// seedWithRetry runs one Seed in its own transaction, retrying a transaction the
	// engine refused.
	//
	// Seed documents that an engine may refuse one of two overlapping seeds
	// where its locking rules leave it no other answer — SQLite admits one
	// writer at a time and reports the second as busy, MySQL's default
	// isolation can declare a deadlock between two seeds of a role that had no
	// grants stored yet — and that what it reports is an error on a transaction
	// that wrote nothing, for the caller to retry. Asserting that every seeder
	// commits on its first attempt therefore asserts something stronger than
	// Seed promises, and MySQL makes good on the difference often enough to red
	// a pull request that touched nothing here.
	//
	// The refusal is deliberately not matched on. A transient refusal and a
	// broken seed are told apart by whether retrying works: a seed that fails
	// for a reason concurrency did not cause fails every attempt and arrives at
	// the assertion as the error it always was. Matching would mean a SQLSTATE
	// list — primitives-go's database/postgres/pgretry holds Postgres's and
	// there is no MySQL counterpart to reach for — and a second copy of one,
	// kept in a test, is the copy that rots without anybody noticing.
	seedWithRetry := func(roles []authorization.Role) error {
		const attempts = 3

		var seedErr error

		for attempt := range attempts {
			if attempt > 0 {
				// Long enough that the retry does not re-collide with whoever
				// won, short enough to stay invisible in the suite's runtime.
				time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
			}

			seedErr = env.client.WithTransaction(ctx, func(q database.Tx) error {
				return r.Seed(ctx, q, roles...)
			})
			if seedErr == nil {
				return nil
			}
		}

		return seedErr
	}

	// seedFromEveryReplica runs one Seed per replica, each in its own
	// transaction, released together so that they overlap rather than queue.
	seedFromEveryReplica := func(t *testing.T, roles []authorization.Role) {
		t.Helper()

		start := make(chan struct{})
		errs := make(chan error, seeders)

		var wg sync.WaitGroup
		for range seeders {
			wg.Go(func() {
				<-start

				errs <- seedWithRetry(roles)
			})
		}

		close(start)
		wg.Wait()
		close(errs)

		for seedErr := range errs {
			test.NoError(t, seedErr)
		}
	}

	// assertPolicy checks that what the replicas converged on is what each of
	// them was given: the declared policy row for row, and its resolution
	// against the reference expansion.
	assertPolicy := func(t *testing.T, roles []authorization.Role) {
		t.Helper()

		expected, expandErr := authorization.ExpandInheritance(roles...)
		must.NoError(t, expandErr)

		declared, rolesErr := r.Roles(ctx)
		must.NoError(t, rolesErr)
		must.SliceLen(t, len(roles), declared)

		byName := make(map[string]authorization.Role, len(declared))
		for i := range declared {
			byName[declared[i].Name] = declared[i]
		}

		for i := range roles {
			want := &roles[i]
			got, ok := byName[want.Name]
			must.True(t, ok, must.Sprintf("role %q was not written", want.Name))

			test.True(t, authorization.NewPermissionSet(got.Permissions...).Equal(authorization.NewPermissionSet(want.Permissions...)),
				test.Sprintf("role %q: declared grants %v, seeded %v", want.Name, got.Permissions, want.Permissions))

			resolved, resolveErr := r.PermissionsForRoles(ctx, want.Name)
			must.NoError(t, resolveErr)
			test.True(t, resolved.Equal(expected[want.Name]),
				test.Sprintf("role %q: resolved %v, reference expansion resolved %v", want.Name, resolved.Slice(), expected[want.Name].Slice()))
		}
	}

	roles := testRoles()

	t.Run("a first seed from every replica converges", func(t *testing.T) {
		seedFromEveryReplica(t, roles)
		assertPolicy(t, roles)
	})

	t.Run("a re-seed from every replica converges", func(t *testing.T) {
		seedFromEveryReplica(t, roles)
		assertPolicy(t, roles)
	})

	t.Run("a re-seed that changes the policy converges on the new one", func(t *testing.T) {
		changed := testRoles()
		changed[1].Permissions = []authorization.Permission{permWrite, "audit.things"}
		changed[3].Inherits = []string{"member"}

		seedFromEveryReplica(t, changed)
		assertPolicy(t, changed)
	})
}

func TestAuthorizationDatabase_ConcurrentSeeds_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString, openConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runConcurrentSeeds(T, &dialectEnv{
			dialect: dialect.Postgres,
			prefix:  DefaultTablePrefix,
			client:  client,
		})
	})
}

func TestAuthorizationDatabase_ConcurrentSeeds_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString, openConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runConcurrentSeeds(T, &dialectEnv{
			dialect: dialect.MySQL,
			prefix:  DefaultTablePrefix,
			client:  client,
		})
	}, mysqltest.WithCredentials("authztest", "authztest", "authztest"))
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
