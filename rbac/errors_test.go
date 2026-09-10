package rbac

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/authorization"
	"github.com/primandproper/primitives-go/database/dialect"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	"github.com/primandproper/primitives-go/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/observability/metrics/mock"
	metricsnoop "github.com/primandproper/primitives-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// errBoom stands in for any driver-level failure.
var errBoom = errors.New("boom")

// The two timestamps a mock row carries. created_at is NOT NULL in every
// dialect's schema, so the generated scan reads it into a time.Time and a nil
// there is a scan failure rather than a row; archivedStamp is what makes a
// looked-up row read as archived.
var (
	createdStamp  = time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	archivedStamp = createdStamp.Add(time.Hour)
)

// The statements this file drives failures through, as the regular expressions
// sqlmock matches a query with.
//
// They are fragments of the committed corpus rather than whole statements: what
// each one has to do is pick its statement out of the fourteen, and a copy of a
// generated statement in a test is a copy that goes stale the first time the
// generator's spacing changes. The distinguishing fragment is usually a column
// the other statements do not project — a role list's own created_at, against a
// hierarchy read's aliased copy of it.
const (
	resolveQuery      = "WITH RECURSIVE"
	listRolesQuery    = "authz_roles.created_at,"
	rolePermsQuery    = "AS permission_name"
	hierarchyQuery    = "AS parent_name"
	rolesByNamesQuery = "authz_roles.name IN"
	permsByNamesQuery = "authz_permissions.name IN"
	roleIDByNameQuery = "authz_roles.name = "

	upsertRoleExec       = "INSERT INTO authz_roles"
	clearPermissionsExec = "DELETE FROM authz_role_permissions"
	grantPermissionExec  = "INSERT OR IGNORE INTO authz_role_permissions"
	clearHierarchyExec   = "DELETE FROM authz_role_hierarchy"
	inheritExec          = "INSERT OR IGNORE INTO authz_role_hierarchy"
	archiveRoleExec      = "UPDATE authz_roles SET"
)

// The column counts each read's generated row type scans, which is all a mock
// result set has to agree with — sqlmock names its columns and the generated
// scan reads them by position.
func roleColumns() []string {
	return []string{"id", "name", "description", "created_at", "last_updated_at", "archived_at"}
}

func lookupColumns() []string {
	return []string{"id", "name", "description", "archived_at"}
}

func joinedColumns(prefix string) []string {
	return []string{
		"key",
		prefix + "_id", prefix + "_name", prefix + "_description",
		prefix + "_created_at", prefix + "_last_updated_at", prefix + "_archived_at",
	}
}

// newMockResolver wires a Resolver to a sqlmock database, so the failure paths
// a real server almost never takes — a dropped connection mid-scan, a column
// that will not scan — can be driven deliberately.
//
// SQLite rather than Postgres, and the difference is not cosmetic: the bound
// sets in this corpus reach a Postgres driver as an array argument, which is a
// pgx conversion rather than one database/sql's default converter knows about,
// and no mock driver implements it. On SQLite the same set is a placeholder
// expansion and the arguments are the strings a caller passed, which is what
// lets a mock stand in for a server at all.
func newMockResolver(t *testing.T) (*Resolver, sqlmock.Sqlmock) {
	t.Helper()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	r, err := NewResolver(
		&Config{Dialect: dialect.SQLite},
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
		mock.ExpectQuery(resolveQuery).WillReturnError(errBoom)

		_, err := r.PermissionsForRoles(t.Context(), "member")

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a scan failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(resolveQuery).WillReturnRows(
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
		mock.ExpectQuery(resolveQuery).WillReturnRows(
			sqlmock.NewRows([]string{"name"}).AddRow("read.things").RowError(0, errBoom),
		)

		_, err := r.PermissionsForRoles(t.Context(), "member")

		test.ErrorIs(t, err, errBoom)
	})
}

func TestResolver_Roles_Failures(T *testing.T) {
	T.Parallel()

	T.Run("surfaces a roles query failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(listRolesQuery).WillReturnError(errBoom)

		_, err := r.Roles(t.Context())

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a roles scan failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(listRolesQuery).WillReturnRows(
			sqlmock.NewRows(roleColumns()).AddRow("id", nil, "d", nil, nil, nil),
		)

		_, err := r.Roles(t.Context())

		test.Error(t, err)
	})

	T.Run("surfaces a role-permissions query failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectRoleList(mock, "role_id", "member")
		mock.ExpectQuery(rolePermsQuery).WillReturnError(errBoom)

		_, err := r.Roles(t.Context())

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a role-permissions scan failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectRoleList(mock, "role_id", "member")
		mock.ExpectQuery(rolePermsQuery).WillReturnRows(
			sqlmock.NewRows(joinedColumns("permission")).AddRow("role_id", "perm_id", nil, "", nil, nil, nil),
		)

		_, err := r.Roles(t.Context())

		test.Error(t, err)
	})

	T.Run("surfaces a role-hierarchy query failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectRoleList(mock, "role_id", "member")
		mock.ExpectQuery(rolePermsQuery).WillReturnRows(sqlmock.NewRows(joinedColumns("permission")))
		mock.ExpectQuery(hierarchyQuery).WillReturnError(errBoom)

		_, err := r.Roles(t.Context())

		test.ErrorIs(t, err, errBoom)
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
		mock.ExpectQuery(rolesByNamesQuery).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a scan failure resolving names", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(
			sqlmock.NewRows(lookupColumns()).AddRow("role_id", nil, "", nil),
		)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.Error(t, err)
	})

	// A name nothing was found for is minted an id and written.
	T.Run("surfaces a write failure for a name that is not there", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(sqlmock.NewRows(lookupColumns()))
		mock.ExpectExec(upsertRoleExec).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	// A minted name is read back once written, so that a writer that lost the
	// race for it carries the id the name actually got; that read is its own
	// failure path.
	T.Run("surfaces a failure reading back a name it minted", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(sqlmock.NewRows(lookupColumns()))
		mock.ExpectExec(upsertRoleExec).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(rolesByNamesQuery).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	// And a name that is still missing after its own write is a broken
	// invariant, named as such rather than left to fail as a foreign key on the
	// first grant.
	T.Run("reports a minted name that cannot be read back", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(sqlmock.NewRows(lookupColumns()))
		mock.ExpectExec(upsertRoleExec).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(sqlmock.NewRows(lookupColumns()))

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, ErrWrittenNameMissing)
	})

	// An existing row whose description changed is written back under the id it
	// already has, which is its own failure path.
	T.Run("surfaces a write failure for a name whose prose changed", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(
			sqlmock.NewRows(lookupColumns()).AddRow("role_id", "member", "stale", nil),
		)
		mock.ExpectExec(upsertRoleExec).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r),
			authorization.Role{Name: "member", Description: "fresh"})

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a permission-clearing failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectSettledRole(mock, "role_id", "member")
		mock.ExpectExec(clearPermissionsExec).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a failure upserting permissions", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectSettledRole(mock, "role_id", "member")
		mock.ExpectExec(clearPermissionsExec).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(permsByNamesQuery).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a grant-insert failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectSettledRole(mock, "role_id", "member")
		mock.ExpectExec(clearPermissionsExec).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(permsByNamesQuery).WillReturnRows(
			sqlmock.NewRows(lookupColumns()).AddRow("perm_id", "read.things", "", nil),
		)
		mock.ExpectExec(grantPermissionExec).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), role)

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("rejects an empty permission name", func(t *testing.T) {
		t.Parallel()

		r, _ := newMockResolver(t)

		_, err := r.resolveNamedIDs(t.Context(), mockExecutor(t, r), r.roleTable(),
			map[string]string{"": ""})

		test.Error(t, err)
	})

	T.Run("surfaces a hierarchy-clearing failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectSettledRole(mock, "role_id", "member")
		mock.ExpectExec(clearPermissionsExec).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(clearHierarchyExec).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r), authorization.Role{Name: "member"})

		test.ErrorIs(t, err, errBoom)
	})

	// The parent is in the same Seed batch, so its id is already known and the
	// insert runs without a further lookup.
	T.Run("surfaces an inheritance-insert failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(
			sqlmock.NewRows(lookupColumns()).
				AddRow("parent_id", "member", "", nil).
				AddRow("child_id", "admin", "", nil),
		)
		mock.ExpectExec(clearPermissionsExec).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(clearHierarchyExec).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(clearPermissionsExec).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(clearHierarchyExec).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(inheritExec).WillReturnError(errBoom)

		err := r.Seed(t.Context(), mockExecutor(t, r),
			authorization.Role{Name: "member"},
			authorization.Role{Name: "admin", Inherits: []string{"member"}},
		)

		test.ErrorIs(t, err, errBoom)
	})

	// An archived row is written back rather than left alone, which is how a
	// reserved name comes back: the upsert's conflict branch clears archived_at,
	// so the row that already holds the name is revived rather than collided
	// with.
	T.Run("writes an archived row back", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(
			sqlmock.NewRows(lookupColumns()).AddRow("role_id", "member", "", archivedStamp),
		)
		mock.ExpectExec(upsertRoleExec).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(clearPermissionsExec).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(clearHierarchyExec).WillReturnResult(sqlmock.NewResult(0, 0))

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
		mock.ExpectExec(archiveRoleExec).WillReturnError(errBoom)

		err := r.ArchiveRole(t.Context(), mockExecutor(t, r), "member")

		test.ErrorIs(t, err, errBoom)
	})
}

func TestResolver_UpsertRole_Failures(T *testing.T) {
	T.Parallel()

	T.Run("surfaces a failure loading existing policy", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		mock.ExpectQuery(listRolesQuery).WillReturnError(errBoom)

		err := r.UpsertRole(t.Context(), mockExecutor(t, r), authorization.Role{Name: "member"})

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a failure upserting the role itself", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectExistingPolicy(mock, "member_id", "member")
		mock.ExpectQuery(roleIDByNameQuery).WillReturnRows(
			sqlmock.NewRows([]string{"id", "archived_at"}).AddRow("member_id", nil),
		)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnError(errBoom)

		err := r.UpsertRole(t.Context(), mockExecutor(t, r), authorization.Role{Name: "admin"})

		test.ErrorIs(t, err, errBoom)
	})

	T.Run("surfaces a failure writing the role's grants", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectExistingPolicy(mock, "member_id", "member")
		mock.ExpectQuery(roleIDByNameQuery).WillReturnRows(
			sqlmock.NewRows([]string{"id", "archived_at"}).AddRow("member_id", nil),
		)
		mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(
			sqlmock.NewRows(lookupColumns()).AddRow("admin_id", "admin", "", nil),
		)
		mock.ExpectExec(clearPermissionsExec).WillReturnError(errBoom)

		err := r.UpsertRole(t.Context(), mockExecutor(t, r), authorization.Role{Name: "admin"})

		test.ErrorIs(t, err, errBoom)
	})

	// A parent that exists in the stored policy still has to be looked up by
	// name to get its id, and that lookup can fail on its own.
	T.Run("surfaces a parent id lookup failure", func(t *testing.T) {
		t.Parallel()

		r, mock := newMockResolver(t)
		expectExistingPolicy(mock, "parent_id", "member")
		mock.ExpectQuery(roleIDByNameQuery).WillReturnError(errBoom)

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
		mock.ExpectQuery(roleIDByNameQuery).WillReturnError(sql.ErrNoRows)

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

// expectRoleList queues the role listing with one live role in it.
func expectRoleList(mock sqlmock.Sqlmock, id, name string) {
	mock.ExpectQuery(listRolesQuery).WillReturnRows(
		sqlmock.NewRows(roleColumns()).AddRow(id, name, "", createdStamp, nil, nil),
	)
}

// expectExistingPolicy queues the three reads UpsertRole performs before it
// writes anything: the role list, their grants, and their hierarchy.
func expectExistingPolicy(mock sqlmock.Sqlmock, roleID, roleName string) {
	expectRoleList(mock, roleID, roleName)
	mock.ExpectQuery(rolePermsQuery).WillReturnRows(sqlmock.NewRows(joinedColumns("permission")))
	mock.ExpectQuery(hierarchyQuery).WillReturnRows(sqlmock.NewRows(joinedColumns("parent")))
}

// expectSettledRole queues the lookup for a role that is already stored exactly
// as the caller wants it, so the upsert is skipped and the next statement is the
// grant rewrite.
func expectSettledRole(mock sqlmock.Sqlmock, id, name string) {
	mock.ExpectQuery(rolesByNamesQuery).WillReturnRows(
		sqlmock.NewRows(lookupColumns()).AddRow(id, name, "", nil),
	)
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
