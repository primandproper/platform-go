package database

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/authorization"
	"github.com/primandproper/platform-go/v13/database/dialect"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// errBoom stands in for any driver-level failure.
var errBoom = errors.New("boom")

// newMockResolver wires a Resolver to a sqlmock database, so the failure paths
// a real server almost never takes — a dropped connection mid-scan, a column
// that will not scan — can be driven deliberately.
func newMockResolver(t *testing.T) (*Resolver, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	r, err := NewResolver(
		&Config{Dialect: dialect.Postgres},
		db,
		WithLogger(loggingnoop.NewLogger()),
		WithTracerProvider(tracingnoop.NewTracerProvider()),
		WithMetricsProvider(metricsnoop.NewMetricsProvider()),
	)
	must.NoError(t, err)

	return r, mock
}

func TestResolver_PermissionsForRoles_Failures(T *testing.T) {
	T.Parallel()

	T.Run("surfaces a query failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("WITH RECURSIVE").WillReturnError(errBoom)

		_, err := r.PermissionsForRoles(t.Context(), "member")

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a scan failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("WITH RECURSIVE").WillReturnRows(
			sqlmock.NewRows([]string{"name"}).AddRow(nil),
		)

		_, err := r.PermissionsForRoles(t.Context(), "member")

		test.Error(t, err)
	})

	// A result set can fail after the last row rather than at the query, which
	// is what a connection dropped mid-stream looks like.
	T.Run("surfaces a row iteration failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("WITH RECURSIVE").WillReturnRows(
			sqlmock.NewRows([]string{"name"}).AddRow("read.things").RowError(0, errBoom),
		)

		_, err := r.PermissionsForRoles(t.Context(), "member")

		test.ErrorIs(t, err, errBoom)
	})
}

// A close failure is surfaced only when nothing worse already went wrong, so
// the real cause is never masked by the cleanup.
func TestScanRows_CloseFailure(T *testing.T) {
	T.Parallel()

	T.Run("surfaces a close failure when the scan succeeded", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("WITH RECURSIVE").WillReturnRows(
			sqlmock.NewRows([]string{"name"}).AddRow("read.things").CloseError(errBoom),
		)

		_, err := r.PermissionsForRoles(t.Context(), "member")

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("a scan failure outranks a close failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("WITH RECURSIVE").WillReturnRows(
			sqlmock.NewRows([]string{"name"}).AddRow("read.things").
				RowError(0, errRowFailure).CloseError(errBoom),
		)

		_, err := r.PermissionsForRoles(t.Context(), "member")

		test.ErrorIs(t, err, errRowFailure)
	})
}

var errRowFailure = errors.New("row failure")

func TestResolver_Roles_Failures(T *testing.T) {
	T.Parallel()

	T.Run("surfaces a roles query failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description FROM").WillReturnError(errBoom)

		_, err := r.Roles(t.Context())

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a roles scan failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description FROM").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description"}).AddRow("id", nil, "d"),
		)

		_, err := r.Roles(t.Context())

		test.Error(t, err)
	})

	T.Run("surfaces a role-permissions query failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description FROM").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description"}).AddRow("id", "member", ""),
		)
		mock.ExpectQuery("role_permissions").WillReturnError(errBoom)

		_, err := r.Roles(t.Context())

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a role-hierarchy query failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description FROM").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description"}).AddRow("id", "member", ""),
		)
		mock.ExpectQuery("role_permissions").WillReturnRows(
			sqlmock.NewRows([]string{"role_id", "name"}),
		)
		mock.ExpectQuery("role_hierarchy").WillReturnError(errBoom)

		_, err := r.Roles(t.Context())

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a pair scan failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description FROM").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description"}).AddRow("id", "member", ""),
		)
		mock.ExpectQuery("role_permissions").WillReturnRows(
			sqlmock.NewRows([]string{"role_id", "name"}).AddRow("id", nil),
		)

		_, err := r.Roles(t.Context())

		test.Error(t, err)
	})
}

func TestResolver_Seed_Failures(T *testing.T) {
	T.Parallel()

	role := authorization.Role{
		Name:        "member",
		Permissions: []authorization.Permission{"read.things"},
	}

	T.Run("surfaces a name lookup failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces an insert failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}),
		)
		mock.ExpectExec("INSERT INTO authz_roles").WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	// An existing row whose description changed is refreshed rather than
	// re-inserted; that update is its own failure path.
	T.Run("surfaces a refresh failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("role_id", "member", "stale", false),
		)
		mock.ExpectExec("UPDATE authz_roles").WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r),
			authorization.Role{Name: "member", Description: "fresh"})

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a permission-clearing failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("role_id", "member", "", false),
		)
		mock.ExpectExec("DELETE FROM authz_role_permissions").WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a failure upserting permissions", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("role_id", "member", "", false),
		)
		mock.ExpectExec("DELETE FROM authz_role_permissions").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a scan failure resolving names", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("role_id", nil, "", false),
		)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.Error(t, err)
	})

	T.Run("surfaces a grant-insert failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("role_id", "member", "", false),
		)
		mock.ExpectExec("DELETE FROM authz_role_permissions").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("perm_id", "read.things", "", false),
		)
		mock.ExpectExec("INSERT INTO authz_role_permissions").WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("rejects an empty permission name", func(t *testing.T) {
		t.Parallel()

		r, _ := newMockResolver(t)

		_, err := r.resolveNamedIDs(t.Context(), mockExecutor(t, r), rolesTable,
			map[string]string{"": ""})

		test.Error(t, err)
	})

	T.Run("surfaces a hierarchy-clearing failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("role_id", "member", "", false),
		)
		mock.ExpectExec("DELETE FROM authz_role_permissions").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("DELETE FROM authz_role_hierarchy").WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), authorization.Role{Name: "member"})

		test.ErrorIs(t, err, errBoom)
	})

	// The parent is in the same Seed batch, so its id is already known and the
	// insert runs without a further lookup.
	T.Run("surfaces an inheritance-insert failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("parent_id", "member", "", false).
				AddRow("child_id", "admin", "", false),
		)
		mock.ExpectExec("DELETE FROM authz_role_permissions").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("DELETE FROM authz_role_hierarchy").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("DELETE FROM authz_role_permissions").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("DELETE FROM authz_role_hierarchy").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("INSERT INTO authz_role_hierarchy").WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r),
			authorization.Role{Name: "member"},
			authorization.Role{Name: "admin", Inherits: []string{"member"}},
		)

		test.ErrorIs(t, err, errBoom)
	})

	// An archived row is revived by the same UPDATE that refreshes a
	// description, which is how a reserved name comes back rather than
	// colliding on its unique index.
	T.Run("revives an archived row", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("role_id", "member", "", true),
		)
		mock.ExpectExec("UPDATE authz_roles").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("DELETE FROM authz_role_permissions").WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec("DELETE FROM authz_role_hierarchy").WillReturnResult(sqlmock.NewResult(0, 0))

		err := r.Seed(t.Context(), mockExecutor(t, r), authorization.Role{Name: "member"})

		must.NoError(t, err)
		must.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestResolver_ArchiveRole_Failure(T *testing.T) {
	T.Parallel()

	T.Run("surfaces an archive failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectExec("UPDATE authz_roles SET archived_at").WillReturnError(errBoom)

		err := r.ArchiveRole(t.Context(), mockExecutor(t, r), "member")

		test.ErrorIs(t, err, errBoom)
	})
}

func TestResolver_UpsertRole_Failures(T *testing.T) {
	T.Parallel()

	T.Run("surfaces a failure loading existing policy", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description FROM").WillReturnError(errBoom)

		err := r.UpsertRole(t.Context(), mockExecutor(t, r), authorization.Role{Name: "member"})

		test.ErrorIs(t, err, errBoom)
	})

	// expectExistingPolicy queues the three reads UpsertRole performs before it
	// writes anything: the role list, their grants, and their hierarchy.
	expectExistingPolicy := func(mock sqlmock.Sqlmock, roleID, roleName string) {
		mock.ExpectQuery("SELECT id, name, description FROM").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description"}).AddRow(roleID, roleName, ""),
		)
		mock.ExpectQuery("role_permissions").WillReturnRows(sqlmock.NewRows([]string{"role_id", "name"}))
		mock.ExpectQuery("role_hierarchy").WillReturnRows(sqlmock.NewRows([]string{"child_role_id", "name"}))
	}

	T.Run("surfaces a failure upserting the role itself", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectExistingPolicy(mock, "member_id", "member")
		mock.ExpectQuery("SELECT id, archived_at IS NOT NULL FROM").WillReturnRows(
			sqlmock.NewRows([]string{"id", "archived"}).AddRow("member_id", false),
		)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnError(errBoom)

		err := r.UpsertRole(t.Context(), mockExecutor(t, r), authorization.Role{Name: "admin"})

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a failure writing the role's grants", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectExistingPolicy(mock, "member_id", "member")
		mock.ExpectQuery("SELECT id, archived_at IS NOT NULL FROM").WillReturnRows(
			sqlmock.NewRows([]string{"id", "archived"}).AddRow("member_id", false),
		)
		mock.ExpectQuery("SELECT id, name, description, archived_at").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description", "archived"}).
				AddRow("admin_id", "admin", "", false),
		)
		mock.ExpectExec("DELETE FROM authz_role_permissions").WillReturnError(errBoom)

		err := r.UpsertRole(t.Context(), mockExecutor(t, r), authorization.Role{Name: "admin"})

		test.ErrorIs(t, err, errBoom)
	})

	// A parent that exists in the stored policy still has to be looked up by
	// name to get its id, and that lookup can fail on its own.
	T.Run("surfaces a parent id lookup failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, name, description FROM").WillReturnRows(
			sqlmock.NewRows([]string{"id", "name", "description"}).AddRow("parent_id", "member", ""),
		)
		mock.ExpectQuery("role_permissions").WillReturnRows(sqlmock.NewRows([]string{"role_id", "name"}))
		mock.ExpectQuery("role_hierarchy").WillReturnRows(sqlmock.NewRows([]string{"child_role_id", "name"}))
		mock.ExpectQuery("SELECT id, archived_at IS NOT NULL FROM").WillReturnError(errBoom)

		err := r.UpsertRole(t.Context(), mockExecutor(t, r),
			authorization.Role{Name: "admin", Inherits: []string{"member"}})

		test.ErrorIs(t, err, errBoom)
	})
}

func TestResolver_LookupID_Failure(T *testing.T) {
	T.Parallel()

	T.Run("surfaces a missing row", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery("SELECT id, archived_at IS NOT NULL FROM").WillReturnError(sql.ErrNoRows)

		_, err := r.lookupRoleID(t.Context(), mockExecutor(t, r), "ghost")

		test.ErrorIs(t, err, sql.ErrNoRows)
	})
}

func TestNewResolver_MetricsFailure(T *testing.T) {
	T.Parallel()

	// Counter construction is the one thing NewResolver does that can fail for
	// a reason unrelated to its arguments. Failing each counter in turn also
	// proves none of them is silently skipped.
	for _, failOn := range []string{
		serviceName + "_resolutions",
		serviceName + "_errors",
	} {
		T.Run("surfaces a failure creating "+failOn, func(t *testing.T) {
			t.Parallel()

			db, _, err := sqlmock.New()
			must.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })

			mp := &metricsmock.ProviderMock{
				NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
					if name == failOn {
						return nil, errBoom
					}

					return &metricsmock.Int64CounterMock{}, nil
				},
			}

			_, err = NewResolver(&Config{Dialect: dialect.Postgres}, db, WithMetricsProvider(mp))

			test.ErrorIs(t, err, errBoom)
		})
	}
}

// mockExecutor returns the resolver's own executor, which the sqlmock database
// backs. Writes normally take the caller's transaction; here the same handle
// serves both so expectations queue in one place.
func mockExecutor(t *testing.T, r *Resolver) *sql.DB {
	t.Helper()

	db, ok := r.db.(*sql.DB)
	must.True(t, ok)

	return db
}
