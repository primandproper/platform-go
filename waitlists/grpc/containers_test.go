package grpc_test

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/mysql"
	"github.com/primandproper/primitives-go/database/postgres"
	"github.com/primandproper/primitives-go/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// defaultMySQLImage pins the MariaDB flavor this suite exercises; mysqltest's
// default is stock MySQL.
const defaultMySQLImage = "mariadb:11"

// TestSurface_RealServers runs the surface's own decisions against real servers,
// and it exists for a narrower reason than the store's container suite does.
//
// The store's suite is about the SQL. This one is about what the *handler* does
// with a real driver underneath it, and there are two things only a real server
// can answer. The first is the read-back inside the transaction: Invite writes
// and then reads through the same database.Tx, and whether that read sees the
// write it follows is the driver's answer rather than SQLite's. The second is
// the affected-row count a guarded transition rests on, which each driver
// reports its own way — a Convert that lost its race has to reach a client as
// FailedPrecondition on Postgres and MySQL alike, and not as a server fault.
//
// It gates on RUN_CONTAINER_TESTS through pgtest and mysqltest, and skips
// otherwise.
func TestSurface_RealServers(T *testing.T) {
	T.Parallel()

	T.Run("postgres", func(t *testing.T) {
		t.Parallel()

		pgtest.Run(t, func(_ context.Context, pg *pgtest.Instance) {
			client, err := postgres.NewDatabaseClient(t.Context(),
				&testClientConfig{connectionString: pg.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			runSurfaceSuite(t, client, dialect.Postgres)
		})
	})

	T.Run("mysql", func(t *testing.T) {
		t.Parallel()

		mysqltest.Run(t, func(ctx context.Context, my *mysqltest.Instance) {
			client, err := mysql.NewDatabaseClient(ctx,
				&testClientConfig{connectionString: my.ConnectionString})
			must.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			runSurfaceSuite(t, client, dialect.MySQL)
		}, mysqltest.WithImage(defaultMySQLImage))
	})
}

// runSurfaceSuite is the behavior every dialect has to agree on.
//
// The subtests are sequential rather than parallel: each builds a harness of its
// own, and a shared server is a shared connection pool that a parallel DDL would
// invalidate underneath.
func runSurfaceSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	newServer := func(tb testing.TB) *harness {
		tb.Helper()

		return newHarnessOn(tb, client, d, permitWithdrawals(),
			waitlistsScopeResolver(),
		)
	}

	// The join and the read-back the transition depends on.
	t.Run("a transition answers with the moment it stamped", func(t *testing.T) {
		h := newServer(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "ada@example.com")

		res, err := h.server.Invite(h.ctx(t), &waitlistspb.InviteRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		test.EqOp(t, waitlistspb.SignupStatus_SIGNUP_STATUS_INVITED, res.GetResult().GetStatus())
		must.NotNil(t, res.GetResult().GetStatusChangedAt())
		test.False(t, res.GetResult().GetStatusChangedAt().AsTime().IsZero())
	})

	// The affected-row count, which is what makes a transition happen exactly
	// once and which each driver reports its own way.
	t.Run("a lost transition is a refusal rather than a fault", func(t *testing.T) {
		h := newServer(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "grace@example.com")

		_, err := h.server.Invite(h.ctx(t), &waitlistspb.InviteRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		res, err := h.server.Invite(h.ctx(t), &waitlistspb.InviteRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		test.Nil(t, res)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrWrongStatus)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})

	// The closing time compared against a real temporal type rather than
	// against text, reached through the public RPC that pages by it.
	t.Run("the open catalog is decided by the store's clock", func(t *testing.T) {
		h := newServer(t)
		open := h.seedOpenList(t, testScope)
		h.seedList(t, testScope, testNow.Add(-time.Hour))

		res, err := h.server.ListOpenLists(h.anonCtx(t), &waitlistspb.ListOpenListsRequest{})
		must.NoError(t, err)

		must.SliceLen(t, 1, res.GetResults())
		test.EqOp(t, open.ID, res.GetResults()[0].GetId())
	})

	// The suppression, which is the obligation the package is shaped around and
	// which rests on a unique key MySQL bounds at 3072 bytes.
	t.Run("a withdrawal outlives the address it was about", func(t *testing.T) {
		h := newServer(t)
		list := h.seedOpenList(t, testScope)
		signup := h.seedSignup(t, testScope, list.ID, "alan@example.com")

		_, err := h.server.Withdraw(h.anonCtx(t), &waitlistspb.WithdrawRequest{
			ListId: list.ID, SignupId: signup.ID,
		})
		must.NoError(t, err)

		joined, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{
			ListId: list.ID, Contact: "ALAN@example.com",
		})
		test.Nil(t, joined)
		must.Error(t, err)

		test.ErrorIs(t, err, waitlists.ErrContactWithdrawn)
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))
	})
}
