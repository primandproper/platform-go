package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/primandproper/primitives-go/authorization"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewResolver(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewResolver(nil, newTestClient(t).Writer())

		test.True(t, errors.Is(err, platformerrors.ErrNilInputParameter))
	})

	T.Run("rejects a nil executor", func(t *testing.T) {
		t.Parallel()

		_, err := NewResolver(&Config{Dialect: dialect.SQLite}, nil)

		test.True(t, errors.Is(err, ErrNilExecutor))
	})

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := NewResolver(&Config{Dialect: "cockroach"}, newTestClient(t).Writer())

		test.True(t, errors.Is(err, dialect.ErrUnsupported))
	})

	// The prefix is interpolated into queries rather than bound, so it is
	// restricted rather than escaped.
	T.Run("rejects an unsafe table prefix", func(t *testing.T) {
		t.Parallel()

		_, err := NewResolver(
			&Config{Dialect: dialect.SQLite, TablePrefix: "authz\"; DROP TABLE users; --"},
			newTestClient(t).Writer(),
		)

		test.True(t, errors.Is(err, ErrInvalidTablePrefix))
	})

	T.Run("an empty prefix uses the default", func(t *testing.T) {
		t.Parallel()

		r, err := NewResolver(&Config{Dialect: dialect.SQLite}, newTestClient(t).Writer())
		must.NoError(t, err)

		test.EqOp(t, DefaultTablePrefix, r.prefix)
	})
}

func TestResolver_PermissionsForRoles(T *testing.T) {
	T.Parallel()

	T.Run("expands inheritance through the recursive CTE", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		set, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permWrite)))
	})

	T.Run("expands two levels of inheritance", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		set, err := r.PermissionsForRoles(t.Context(), "service_admin")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permWrite, permDelete)))
	})

	T.Run("unions several roles", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		set, err := r.PermissionsForRoles(t.Context(), "admin", "auditor")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permWrite)))
	})

	T.Run("unknown roles contribute nothing", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		set, err := r.PermissionsForRoles(t.Context(), "member", "ghost")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead)))
	})

	T.Run("no roles yields an empty set without querying", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		set, err := r.PermissionsForRoles(t.Context())
		must.NoError(t, err)

		test.True(t, set.IsEmpty())
	})

	T.Run("an empty policy grants nothing", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestResolver(t)

		set, err := r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		test.True(t, set.IsEmpty())
	})
}

// The equivalence that makes the two backends interchangeable. If the recursive
// CTE and ExpandInheritance ever disagree, switching providers silently changes
// who can do what — so it is asserted directly rather than assumed.
func TestResolver_AgreesWithReferenceExpansion(T *testing.T) {
	T.Parallel()

	T.Run("every role resolves identically to ExpandInheritance", func(t *testing.T) {
		t.Parallel()

		roles := testRoles()

		r, client := newTestResolver(t)
		seed(t, r, client, roles...)

		expected, err := authorization.ExpandInheritance(roles...)
		must.NoError(t, err)

		for _, role := range roles {
			got, resolveErr := r.PermissionsForRoles(t.Context(), role.Name)
			must.NoError(t, resolveErr)

			if !got.Equal(expected[role.Name]) {
				t.Errorf("role %q: SQL resolved %v, reference expansion resolved %v",
					role.Name, got.Slice(), expected[role.Name].Slice())
			}
		}
	})

	T.Run("role combinations resolve identically", func(t *testing.T) {
		t.Parallel()

		roles := testRoles()

		r, client := newTestResolver(t)
		seed(t, r, client, roles...)

		expected, err := authorization.ExpandInheritance(roles...)
		must.NoError(t, err)

		got, err := r.PermissionsForRoles(t.Context(), "admin", "auditor")
		must.NoError(t, err)

		want := expected["admin"].Union(expected["auditor"])
		test.True(t, got.Equal(want))
	})
}

func TestResolver_Seed(T *testing.T) {
	T.Parallel()

	T.Run("is idempotent", func(t *testing.T) {
		t.Parallel()

		roles := testRoles()

		r, client := newTestResolver(t)
		seed(t, r, client, roles...)
		seed(t, r, client, roles...)

		set, err := r.PermissionsForRoles(t.Context(), "service_admin")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permWrite, permDelete)))

		got, err := r.Roles(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, len(roles), got)
	})

	// Clear-then-rewrite: a caller who removes a permission from a role's list
	// and re-seeds is entitled to have it actually revoked.
	T.Run("removes a permission dropped from a role", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)

		seed(t, r, client, authorization.Role{
			Name:        "member",
			Permissions: []authorization.Permission{permRead, permWrite},
		})
		seed(t, r, client, authorization.Role{
			Name:        "member",
			Permissions: []authorization.Permission{permRead},
		})

		set, err := r.PermissionsForRoles(t.Context(), "member")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead)))
	})

	T.Run("removes an inheritance edge dropped from a role", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		seed(t, r, client, authorization.Role{
			Name:        "admin",
			Permissions: []authorization.Permission{permWrite},
		})

		set, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permWrite)))
	})

	T.Run("leaves roles it was not given alone", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		seed(t, r, client, authorization.Role{
			Name:        "newcomer",
			Permissions: []authorization.Permission{permRead},
		})

		set, err := r.PermissionsForRoles(t.Context(), "service_admin")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permWrite, permDelete)))
	})

	T.Run("validates before writing anything", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.Seed(t.Context(), q,
				authorization.Role{Name: "a", Inherits: []string{"b"}},
				authorization.Role{Name: "b", Inherits: []string{"a"}},
			)
		})

		test.True(t, errors.Is(err, authorization.ErrInheritanceCycle))

		roles, err := r.Roles(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, roles)
	})

	T.Run("rejects a nil executor", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestResolver(t)

		test.True(t, errors.Is(r.Seed(t.Context(), nil), ErrNilExecutor))
	})

	T.Run("seeding nothing is a no-op", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client)

		roles, err := r.Roles(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, roles)
	})

	// The grants are written one statement each, so what this exercises is the
	// one place a batch still reaches the server whole: the name lookup binds
	// every permission in the policy as a single set, which is a placeholder
	// expansion on two of the three dialects.
	T.Run("handles a policy of a few hundred permissions", func(t *testing.T) {
		t.Parallel()

		const permCount = 250

		perms := make([]authorization.Permission, 0, permCount)
		for i := range permCount {
			perms = append(perms, authorization.Permission(fmt.Sprintf("read.thing_%03d", i)))
		}

		r, client := newTestResolver(t)
		seed(t, r, client, authorization.Role{Name: "bulk", Permissions: perms})

		set, err := r.PermissionsForRoles(t.Context(), "bulk")
		must.NoError(t, err)

		test.EqOp(t, permCount, set.Len())
		test.True(t, set.Has("read.thing_000"))
		test.True(t, set.Has("read.thing_249"))
	})

	T.Run("rolls back with its transaction", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)

		sentinel := errors.New("caller changed their mind")
		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			if seedErr := r.Seed(t.Context(), q, testRoles()...); seedErr != nil {
				return seedErr
			}

			return sentinel
		})
		test.True(t, errors.Is(err, sentinel))

		roles, err := r.Roles(t.Context())
		must.NoError(t, err)
		test.SliceEmpty(t, roles)
	})
}

func TestResolver_Roles(T *testing.T) {
	T.Parallel()

	T.Run("reports the declared policy rather than the closure", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		got, err := r.Roles(t.Context())
		must.NoError(t, err)

		byName := map[string]authorization.Role{}
		for _, role := range got {
			byName[role.Name] = role
		}

		test.SliceLen(t, 4, got)
		test.EqOp(t, "a member", byName["member"].Description)
		// admin's own grant only — permRead comes from member at resolve time.
		test.Eq(t, []authorization.Permission{permWrite}, byName["admin"].Permissions)
		test.Eq(t, []string{"member"}, byName["admin"].Inherits)
		test.SliceEmpty(t, byName["member"].Inherits)
	})

	T.Run("is empty before anything is seeded", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestResolver(t)

		got, err := r.Roles(t.Context())
		must.NoError(t, err)

		test.SliceEmpty(t, got)
	})
}

func TestResolver_UpsertRole(T *testing.T) {
	T.Parallel()

	upsert := func(t *testing.T, r *Resolver, client database.Client, role authorization.Role) error {
		t.Helper()

		return client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.UpsertRole(t.Context(), q, role)
		})
	}

	T.Run("adds a role inheriting an existing one", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, upsert(t, r, client, authorization.Role{
			Name:        "regional_manager",
			Permissions: []authorization.Permission{permDelete},
			Inherits:    []string{"member"},
		}))

		set, err := r.PermissionsForRoles(t.Context(), "regional_manager")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead, permDelete)))
	})

	T.Run("replaces an existing role's grants", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, upsert(t, r, client, authorization.Role{
			Name:        "auditor",
			Permissions: []authorization.Permission{permDelete},
		}))

		set, err := r.PermissionsForRoles(t.Context(), "auditor")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permDelete)))
	})

	T.Run("rejects an unknown parent", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		err := upsert(t, r, client, authorization.Role{Name: "orphan", Inherits: []string{"nobody"}})

		test.True(t, errors.Is(err, authorization.ErrUnknownParentRole))
	})

	// Validated against the policy already in the database, so an edge that
	// would close a cycle is refused rather than written.
	T.Run("rejects an edge that would close a cycle", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		err := upsert(t, r, client, authorization.Role{
			Name:     "member",
			Inherits: []string{"service_admin"},
		})

		test.True(t, errors.Is(err, authorization.ErrInheritanceCycle))
	})

	T.Run("rejects a nil executor", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestResolver(t)

		test.True(t, errors.Is(
			r.UpsertRole(t.Context(), nil, authorization.Role{Name: "x"}),
			ErrNilExecutor,
		))
	})
}

func TestResolver_ArchiveRole(T *testing.T) {
	T.Parallel()

	archive := func(t *testing.T, r *Resolver, client database.Client, name string) error {
		t.Helper()

		return client.WithTransaction(t.Context(), func(q database.Tx) error {
			return r.ArchiveRole(t.Context(), q, name)
		})
	}

	T.Run("an archived role grants nothing", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, archive(t, r, client, "auditor"))

		set, err := r.PermissionsForRoles(t.Context(), "auditor")
		must.NoError(t, err)

		test.True(t, set.IsEmpty())
	})

	T.Run("an archived role disappears from Roles", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, archive(t, r, client, "auditor"))

		got, err := r.Roles(t.Context())
		must.NoError(t, err)

		names := make([]string, 0, len(got))
		for _, role := range got {
			names = append(names, role.Name)
		}
		test.SliceNotContains(t, names, "auditor")
	})

	// Archiving an inherited role stops it contributing to its descendants,
	// which is the property that makes archival a real revocation.
	T.Run("archiving an ancestor revokes its grants downstream", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, archive(t, r, client, "member"))

		set, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permWrite)))
	})

	// The name stays reserved: re-seeding revives the same row rather than
	// colliding on the unique index.
	T.Run("re-seeding revives an archived role", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, archive(t, r, client, "auditor"))
		seed(t, r, client, testRoles()...)

		set, err := r.PermissionsForRoles(t.Context(), "auditor")
		must.NoError(t, err)

		test.True(t, set.Equal(authorization.NewPermissionSet(permRead)))
	})

	T.Run("archiving an unknown role is a no-op", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, archive(t, r, client, "ghost"))
	})

	T.Run("rejects a nil executor", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestResolver(t)

		test.True(t, errors.Is(r.ArchiveRole(t.Context(), nil, "x"), ErrNilExecutor))
	})
}

// writeRoleGrants resolves a parent that is missing from the ID map it was
// handed by querying for it. Neither caller can produce that state today —
// Seed and UpsertRole both validate the full policy first, so every parent is
// already in the map — so the path is driven directly here. It is what makes
// the function safe to call with a partial map, which is the only reason a
// future incremental caller would be able to.
func TestResolver_WriteRoleGrantsResolvesAbsentParents(T *testing.T) {
	T.Parallel()

	T.Run("looks up a parent the ID map does not carry", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			adminID, err := r.lookupRoleID(t.Context(), q, "admin")
			must.NoError(t, err)

			// "member" is deliberately absent, so the parent has to be resolved
			// from the database rather than the map.
			return r.writeRoleGrants(t.Context(), q, map[string]string{"admin": adminID},
				&authorization.Role{
					Name:        "admin",
					Permissions: []authorization.Permission{permWrite},
					Inherits:    []string{"member"},
				})
		}))

		// The inheritance edge survived the rewrite, so the lookup found the
		// same role the map would have named.
		set, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)
		test.True(t, set.Has(permWrite))
		test.True(t, set.Has(permRead))
	})

	T.Run("surfaces a parent that cannot be found", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			adminID, lookupErr := r.lookupRoleID(t.Context(), q, "admin")
			must.NoError(t, lookupErr)

			return r.writeRoleGrants(t.Context(), q, map[string]string{"admin": adminID},
				&authorization.Role{
					Name:     "admin",
					Inherits: []string{"nonexistent"},
				})
		})

		must.Error(t, err)
		test.StrContains(t, err.Error(), "nonexistent")
	})
}

// TestResolver_ResolveNamedIDsReadsBackWhatItMinted covers the window a
// concurrent seed opens between the name lookup and the write: another writer
// inserts the same name and commits, this writer's upsert converges on that
// row, and the id this writer minted is one no row carries. SQLite admits one
// writer at a time, so the race itself cannot be staged here; what can be is
// its exact effect, with a lookup that reports a name absent which the table
// already holds.
func TestResolver_ResolveNamedIDsReadsBackWhatItMinted(T *testing.T) {
	T.Parallel()

	// lyingOnce wraps a table so that its first lookup finds nothing, the way
	// the lookup before a lost race does, and every later one tells the truth.
	lyingOnce := func(table namedTable) (namedTable, *int) {
		calls := 0
		wrapped := table
		wrapped.lookup = func(ctx context.Context, q database.SQLQueryExecutor, names []string) (map[string]namedRow, error) {
			calls++
			if calls == 1 {
				return map[string]namedRow{}, nil
			}

			return table.lookup(ctx, q, names)
		}

		return wrapped, &calls
	}

	T.Run("carries the id a lost race left the name under", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			winner, err := r.lookupRoleID(t.Context(), q, "admin")
			must.NoError(t, err)

			table, calls := lyingOnce(r.roleTable())

			ids, err := r.resolveNamedIDs(t.Context(), q, table, map[string]string{"admin": "the admin"})
			must.NoError(t, err)

			// The name converged on the row that already held it, and the id
			// handed back is that row's rather than the one minted for it.
			test.EqOp(t, winner, ids["admin"])
			test.EqOp(t, 2, *calls)

			// Which is what makes the grants that follow land on a row that
			// exists instead of failing a foreign key.
			return r.writeRoleGrants(t.Context(), q, ids, &authorization.Role{
				Name:        "admin",
				Permissions: []authorization.Permission{permWrite, permDelete},
			})
		}))

		set, err := r.PermissionsForRoles(t.Context(), "admin")
		must.NoError(t, err)
		test.True(t, set.Equal(authorization.NewPermissionSet(permWrite, permDelete)))

		roles, err := r.Roles(t.Context())
		must.NoError(t, err)
		test.SliceLen(t, len(testRoles()), roles)
	})

	T.Run("reads back only the names it minted", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)
		seed(t, r, client, testRoles()...)

		must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
			var lookups [][]string

			table := r.roleTable()
			truthful := table.lookup
			table.lookup = func(ctx context.Context, q database.SQLQueryExecutor, names []string) (map[string]namedRow, error) {
				lookups = append(lookups, names)

				return truthful(ctx, q, names)
			}

			// Nothing minted: one lookup, and nothing read back.
			_, err := r.resolveNamedIDs(t.Context(), q, table, map[string]string{"admin": "", "member": "a member"})
			must.NoError(t, err)
			test.SliceLen(t, 1, lookups)

			// One name minted beside two found: the second lookup is for the
			// minted one alone.
			ids, err := r.resolveNamedIDs(t.Context(), q, table, map[string]string{"admin": "", "member": "a member", "newcomer": ""})
			must.NoError(t, err)
			test.SliceLen(t, 3, lookups)
			test.Eq(t, []string{"newcomer"}, lookups[2])

			landed, err := r.lookupRoleID(t.Context(), q, "newcomer")
			must.NoError(t, err)
			test.EqOp(t, landed, ids["newcomer"])

			return nil
		}))
	})

	T.Run("reports a name that cannot be read back", func(t *testing.T) {
		t.Parallel()

		r, client := newTestResolver(t)

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			table := r.roleTable()
			table.lookup = func(context.Context, database.SQLQueryExecutor, []string) (map[string]namedRow, error) {
				return map[string]namedRow{}, nil
			}
			table.upsert = func(context.Context, database.SQLQueryExecutor, string, string, string) error {
				return nil
			}

			_, err := r.resolveNamedIDs(t.Context(), q, table, map[string]string{"ghost": ""})

			return err
		})

		test.True(t, errors.Is(err, ErrWrittenNameMissing))
		test.StrContains(t, err.Error(), "ghost")
	})
}

func TestDialect_Valid(T *testing.T) {
	T.Parallel()

	T.Run("accepts the supported dialects", func(t *testing.T) {
		t.Parallel()

		test.True(t, dialect.Postgres.Valid())
		test.True(t, dialect.MySQL.Valid())
		test.True(t, dialect.SQLite.Valid())
	})

	T.Run("rejects anything else", func(t *testing.T) {
		t.Parallel()

		test.False(t, dialect.Dialect("").Valid())
		test.False(t, dialect.Dialect("cockroach").Valid())
	})
}
