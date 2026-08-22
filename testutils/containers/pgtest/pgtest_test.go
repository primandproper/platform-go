package pgtest

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/testutils/containers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("defaults", func(t *testing.T) {
		t.Parallel()

		cfg := newOptions(nil)
		test.EqOp(t, DefaultImage, cfg.image)
		test.EqOp(t, defaultCredential, cfg.database)
		test.EqOp(t, defaultCredential, cfg.username)
		test.EqOp(t, defaultCredential, cfg.password)
		test.EqOp(t, 0, cfg.maxOpenConns)
		test.EqOp(t, DefaultMaxConnections, cfg.maxConnections)
		test.EqOp(t, "", cfg.dsnEnvVar)
		test.False(t, cfg.required)
		test.SliceEmpty(t, cfg.customizers)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		cfg := newOptions([]Option{nil, WithImage("postgres:16-alpine")})
		test.EqOp(t, "postgres:16-alpine", cfg.image)
	})

	T.Run("ladder options", func(t *testing.T) {
		t.Parallel()

		cfg := newOptions([]Option{
			WithRequiredPostgres(),
			WithDSNFromEnv("SOME_TEST_POSTGRES_DSN"),
			WithMaxConnections(500),
		})
		test.True(t, cfg.required)
		test.EqOp(t, "SOME_TEST_POSTGRES_DSN", cfg.dsnEnvVar)
		test.EqOp(t, 500, cfg.maxConnections)
	})

	T.Run("options override defaults", func(t *testing.T) {
		t.Parallel()

		cfg := newOptions([]Option{
			WithImage("pgvector/pgvector:pg17"),
			WithCredentials("vectortest", "vectoruser", "vectorpass"),
			WithMaxOpenConns(64),
			WithCustomizers(testcontainers.WithEnv(map[string]string{"FOO": "bar"})),
		})
		test.EqOp(t, "pgvector/pgvector:pg17", cfg.image)
		test.EqOp(t, "vectortest", cfg.database)
		test.EqOp(t, "vectoruser", cfg.username)
		test.EqOp(t, "vectorpass", cfg.password)
		test.EqOp(t, 64, cfg.maxOpenConns)
		test.SliceLen(t, 1, cfg.customizers)
	})

	T.Run("customizers accumulate in call order", func(t *testing.T) {
		t.Parallel()

		cfg := newOptions([]Option{
			WithCustomizers(testcontainers.WithEnv(map[string]string{"FIRST": "1"})),
			WithCustomizers(testcontainers.WithEnv(map[string]string{"SECOND": "2"})),
		})
		test.SliceLen(t, 2, cfg.customizers)
	})
}

func TestOptions_containerOptions(T *testing.T) {
	T.Parallel()

	T.Run("user customizers come last so they can override the defaults", func(t *testing.T) {
		t.Parallel()

		override := testcontainers.WithEnv(map[string]string{"FOO": "bar"})
		got := newOptions([]Option{WithCustomizers(override)}).containerOptions()

		// database, username, password, wait strategy, max_connections, then the
		// caller's own.
		test.SliceLen(t, 6, got)
	})

	T.Run("max_connections is appended to the module's own command line", func(t *testing.T) {
		t.Parallel()

		req := &testcontainers.GenericContainerRequest{}
		req.Cmd = []string{"postgres", "-c", "fsync=off"}

		for _, customizer := range newOptions([]Option{WithMaxConnections(321)}).containerOptions() {
			if apply, ok := customizer.(testcontainers.CustomizeRequestOption); ok {
				must.NoError(t, apply(req))
			}
		}

		test.Eq(t, []string{"postgres", "-c", "fsync=off", "-c", "max_connections=321"}, req.Cmd)
	})

	T.Run("a zero ceiling leaves the image's own default alone", func(t *testing.T) {
		t.Parallel()

		// database, username, password, wait strategy — and no -c.
		test.SliceLen(t, 4, newOptions([]Option{WithMaxConnections(0)}).containerOptions())
	})
}

// TestOptions_dsnFromEnv is not parallel, and neither are its subtests: t.Setenv
// refuses to run anywhere under a parallel test.
//
//nolint:paralleltest // t.Setenv forbids it, here and in every parent
func TestOptions_dsnFromEnv(T *testing.T) {
	T.Run("empty when no variable was named", func(t *testing.T) { //nolint:paralleltest // t.Setenv forbids it
		test.EqOp(t, "", newOptions(nil).dsnFromEnv())
	})

	T.Run("empty when the named variable is unset", func(t *testing.T) { //nolint:paralleltest // t.Setenv forbids it
		test.EqOp(t, "", newOptions([]Option{WithDSNFromEnv("PGTEST_DSN_DEFINITELY_UNSET")}).dsnFromEnv())
	})

	T.Run("reads and trims the named variable", func(t *testing.T) { //nolint:paralleltest // t.Setenv forbids it
		const name = "PGTEST_DSN_FOR_TEST"
		t.Setenv(name, "  postgres://u:p@localhost:5432/db  ")

		test.EqOp(t, "postgres://u:p@localhost:5432/db",
			newOptions([]Option{WithDSNFromEnv(name)}).dsnFromEnv())
	})

	T.Run("whitespace only reads as unset", func(t *testing.T) { //nolint:paralleltest // t.Setenv forbids it
		const name = "PGTEST_DSN_BLANK_FOR_TEST"
		t.Setenv(name, "   ")

		test.EqOp(t, "", newOptions([]Option{WithDSNFromEnv(name)}).dsnFromEnv())
	})
}

func TestInstance_ConnectionStringFor(T *testing.T) {
	T.Parallel()

	T.Run("builds a DSN from the recorded host and port", func(t *testing.T) {
		t.Parallel()

		instance := &Instance{Host: "127.0.0.1", Port: "54321"}

		test.EqOp(t, "postgres://someuser:somepass@127.0.0.1:54321/somedb",
			instance.ConnectionStringFor(t, "somedb", "someuser", "somepass"))
	})

	T.Run("escapes credentials that would otherwise break the URL", func(t *testing.T) {
		t.Parallel()

		instance := &Instance{Host: "127.0.0.1", Port: "54321"}

		test.EqOp(t, "postgres://user%40host:p%2Fss@127.0.0.1:54321/somedb",
			instance.ConnectionStringFor(t, "somedb", "user@host", "p/ss"))
	})
}

// withRunningTests forces the RUN_CONTAINER_TESTS gate open or shut for the
// duration of a test, so the rungs of the ladder below a container can be
// exercised without a Docker daemon. Callers must not be parallel:
// containers.RunningTests is package-level state.
func withRunningTests(tb testing.TB, running bool) {
	tb.Helper()

	original := containers.RunningTests
	tb.Cleanup(func() { containers.RunningTests = original })
	containers.RunningTests = running
}

// TestStart is not parallel, and neither are its subtests: they toggle the
// package-level RUN_CONTAINER_TESTS gate, and some of them set environment
// variables.
//
//nolint:paralleltest // mutates containers.RunningTests and the environment; must run serially
func TestStart(T *testing.T) {
	// A context that is already done, so the rungs past the gate fail
	// immediately instead of spending the retry policy on a Docker daemon that
	// may not be there. What each subtest is asserting is which rung was
	// reached, not what happened once it got there.
	doneContext := func(tb testing.TB) context.Context {
		tb.Helper()

		ctx, cancel := context.WithCancel(tb.Context())
		cancel()

		return ctx
	}

	T.Run("a closed gate is ErrNoPostgres rather than a skip", func(t *testing.T) { //nolint:paralleltest // see parent
		withRunningTests(t, false)

		pg, teardown, err := Start(doneContext(t))

		must.ErrorIs(t, err, ErrNoPostgres)
		test.Nil(t, pg)
		test.Nil(t, teardown)
	})

	T.Run("WithRequiredPostgres goes past the gate", func(t *testing.T) { //nolint:paralleltest // see parent
		withRunningTests(t, false)

		// The point of the option: nothing was started here either, but the
		// reason is a failure to start rather than a decision not to try, and a
		// caller that returns the error gets a red run rather than a quiet one.
		_, _, err := Start(doneContext(t), WithRequiredPostgres())

		must.Error(t, err)
		test.False(t, errors.Is(err, ErrNoPostgres))
	})

	T.Run("an open gate goes past it too", func(t *testing.T) { //nolint:paralleltest // see parent
		withRunningTests(t, true)

		_, _, err := Start(doneContext(t))

		must.Error(t, err)
		test.False(t, errors.Is(err, ErrNoPostgres))
	})

	T.Run("the DSN rung is reached with the gate shut", func(t *testing.T) { //nolint:paralleltest // t.Setenv forbids it
		withRunningTests(t, false)

		const name = "PGTEST_START_LADDER_DSN"
		t.Setenv(name, "://not-a-dsn")

		// Unparseable, so it fails on the spot rather than after ten seconds of
		// dialing — and the failure proves the DSN was consulted ahead of the
		// gate that would otherwise have said ErrNoPostgres.
		_, _, err := Start(doneContext(t), WithDSNFromEnv(name))

		must.Error(t, err)
		test.False(t, errors.Is(err, ErrNoPostgres))
		test.StrContains(t, err.Error(), name)
	})
}

// TestOptions_gate is not parallel: it toggles the package-level
// RUN_CONTAINER_TESTS gate and sets environment variables.
//
//nolint:paralleltest // mutates containers.RunningTests and the environment; must run serially
func TestOptions_gate(T *testing.T) {
	T.Run("an open gate does not skip", func(t *testing.T) { //nolint:paralleltest // see parent
		withRunningTests(t, true)

		newOptions(nil).gate(t)

		test.False(t, t.Skipped())
	})

	T.Run("a named DSN does not consult the container gate", func(t *testing.T) { //nolint:paralleltest // t.Setenv forbids it
		withRunningTests(t, false)

		const name = "PGTEST_GATE_LADDER_DSN"
		t.Setenv(name, "postgres://u:p@localhost:5432/db")

		newOptions([]Option{WithDSNFromEnv(name)}).gate(t)

		test.False(t, t.Skipped())
	})
}

func TestOpenPool(T *testing.T) {
	T.Parallel()

	T.Run("a server that never answers is an error rather than a pool", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		// The pool is opened and then closed again here, since the caller is
		// handed nothing to close it with.
		db, err := openPool(ctx, "postgres://u:p@127.0.0.1:1/db", DefaultIsolatedMaxOpenConns, DefaultIsolatedMaxIdleConns)

		must.Error(t, err)
		test.Nil(t, db)
	})
}

func TestStart_Container(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	T.Run("hands back a live instance and a teardown that closes it", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		pg, teardown, err := Start(ctx)
		must.NoError(t, err)
		must.NotNil(t, pg)
		must.NotNil(t, pg.Container)

		var one int
		must.NoError(t, pg.DB.QueryRowContext(ctx, "SELECT 1").Scan(&one))
		test.EqOp(t, 1, one)

		must.NoError(t, teardown())

		// Nothing registered it, so this is the teardown having run and not a
		// cleanup that happened to fire.
		test.Error(t, pg.DB.PingContext(ctx))
	})
}

func TestRun_Container(T *testing.T) {
	T.Parallel()

	T.Run("hands the closure a queryable database", func(t *testing.T) {
		t.Parallel()

		Run(t, func(ctx context.Context, pg *Instance) {
			must.NotNil(t, pg.DB)
			must.NotNil(t, pg.Container)
			test.EqOp(t, defaultCredential, pg.Database)
			test.StrContains(t, pg.ConnectionString, "sslmode=disable")

			var current string
			must.NoError(t, pg.DB.QueryRowContext(ctx, "SELECT current_database()").Scan(&current))
			test.EqOp(t, defaultCredential, current)
		})
	})

	T.Run("honors credential overrides and reconnects under them", func(t *testing.T) {
		t.Parallel()

		Run(t, func(ctx context.Context, pg *Instance) {
			test.EqOp(t, "othertest", pg.Database)

			db := pg.Open(t, pg.ConnectionStringFor(t, pg.Database, pg.Username, pg.Password))

			var user string
			must.NoError(t, db.QueryRowContext(ctx, "SELECT current_user").Scan(&user))
			test.EqOp(t, "otheruser", user)
		}, WithCredentials("othertest", "otheruser", "otherpass"))
	})
}
