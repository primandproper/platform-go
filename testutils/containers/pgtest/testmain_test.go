package pgtest

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log"
	"os"
	"testing"

	"github.com/primandproper/platform-go/v13/testutils/containers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the package eating its own cooking: the per-binary setup that
// Run performs for a test, performed instead from the one place in a test
// binary where there is no testing.TB to perform it with. Everything the
// TestMain shape needs — Start, NewTemplate, an explicit teardown, a postgres
// that is simply absent — is exercised here, and the tests below clone from it
// with a *testing.T in hand, which is the whole point of the arrangement.
var (
	// testMainTemplate is the template TestMain migrates once for the binary.
	// Nil when no postgres was available, which is the ordinary case for a bare
	// `go test ./...`.
	testMainTemplate *Template

	// errTestMainSetup is what went wrong standing that up, deliberately not fatal:
	// the rest of this package's tests are unit tests that want nothing from a
	// database, and TestStart_TestMainShape_Container is where it is reported.
	errTestMainSetup error
)

func TestMain(m *testing.M) {
	// os.Exit does not run deferred functions, so the body that has teardowns to
	// run is a function that returns a code and the exit stays out here. A
	// TestMain that defers teardown next to os.Exit(m.Run()) leaks its container.
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	// Start does not consult -short, because it cannot know whether its caller has
	// parsed flags — and testing.Short() before flag.Parse panics rather than
	// reporting false. So the gate lives here, one flag.Parse ahead of it: without
	// it a -short run pays for a container and then skips every test that would
	// have queried it.
	flag.Parse()

	if testing.Short() {
		return m.Run()
	}

	ctx := context.Background()

	pg, teardown, err := Start(ctx)
	if err != nil {
		// ErrNoPostgres is the RUN_CONTAINER_TESTS gate being closed, and
		// nothing was started. A suite whose backend is only ever exercised here
		// would return 1; this one lets its tests skip.
		if !errors.Is(err, ErrNoPostgres) {
			errTestMainSetup = err
		}

		return m.Run()
	}

	defer func() {
		if teardownErr := teardown(); teardownErr != nil {
			log.Printf("pgtest: tearing down the TestMain instance: %v", teardownErr)
		}
	}()

	template, dropTemplate, err := pg.NewTemplate(ctx, WithLabel("testmain"), WithMigration(seedWidgets))
	if err != nil {
		errTestMainSetup = err

		return m.Run()
	}

	defer func() {
		if dropErr := dropTemplate(); dropErr != nil {
			log.Printf("pgtest: dropping the TestMain template: %v", dropErr)
		}
	}()

	testMainTemplate = template

	return m.Run()
}

// seedWidgets migrates and seeds, so a clone that carries the template's rows is
// distinguishable from one that merely carries its tables.
func seedWidgets(ctx context.Context, db *sql.DB) error {
	if err := createWidgets(ctx, db); err != nil {
		return err
	}

	_, err := db.ExecContext(ctx, `INSERT INTO widgets (label) VALUES ('from-the-template')`)

	return err
}

func TestStart_TestMainShape_Container(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)
	must.NoError(T, errTestMainSetup)
	must.NotNil(T, testMainTemplate)

	T.Run("the template carries the label it was given rather than a test's name", func(t *testing.T) {
		t.Parallel()

		test.StrHasPrefix(t, templatePrefix+"_testmain_", testMainTemplate.Name)
	})

	T.Run("a test clones the per-binary template with a testing.TB in hand", func(t *testing.T) {
		t.Parallel()

		clone := testMainTemplate.Clone(t)

		test.NotEqOp(t, testMainTemplate.Name, clone.Name)
		test.EqOp(t, 1, countIn(t, t.Context(), clone.DB, "widgets"))
	})

	T.Run("clones of it are isolated from each other", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		first, second := testMainTemplate.Clone(t), testMainTemplate.Clone(t)

		_, err := first.DB.ExecContext(ctx, `INSERT INTO widgets (label) VALUES ('only-here')`)
		must.NoError(t, err)

		test.EqOp(t, 2, countIn(t, ctx, first.DB, "widgets"))
		test.EqOp(t, 1, countIn(t, ctx, second.DB, "widgets"))
	})

	T.Run("the instance underneath still hands out schemas", func(t *testing.T) {
		t.Parallel()

		// Instance.Schema needs no companion because a test is always in hand by
		// then — including when the Instance came from Start.
		isolated := testMainTemplate.instance.Schema(t, WithMigration(createWidgets))

		test.EqOp(t, 0, countIn(t, t.Context(), isolated.DB, "widgets"))
	})
}
