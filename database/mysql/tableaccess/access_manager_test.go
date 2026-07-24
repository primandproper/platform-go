package tableaccess

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v6/pointer"
	"github.com/primandproper/platform-go/v6/testutils/containers"

	_ "github.com/go-sql-driver/mysql"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	mysqlcontainers "github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	defaultMySQLImage = "mariadb:11"
)

func reverseString(input string) string {
	runes := []rune(input)
	length := len(runes)

	for i, j := 0, length-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}

	return string(runes)
}

func splitReverseConcat(input string) string {
	length := len(input)
	halfLength := length / 2

	firstHalf := input[:halfLength]
	secondHalf := input[halfLength:]

	reversedFirstHalf := reverseString(firstHalf)
	reversedSecondHalf := reverseString(secondHalf)

	return reversedSecondHalf + reversedFirstHalf
}

func hashStringToNumber(s string) uint64 {
	h := fnv.New64a()

	_, err := h.Write([]byte(s))
	if err != nil {
		panic(err)
	}

	return h.Sum64()
}

func buildDatabaseConnectionForTest(t *testing.T, ctx context.Context) (*sql.DB, *mysqlcontainers.MySQLContainer) {
	t.Helper()

	dbUsername := fmt.Sprintf("u%d", hashStringToNumber(t.Name()))
	dbPassword := reverseString(dbUsername)
	dbName := splitReverseConcat(dbUsername)

	container, err := containers.StartWithRetry(ctx, func(ctx context.Context) (*mysqlcontainers.MySQLContainer, error) {
		return mysqlcontainers.Run(
			ctx,
			defaultMySQLImage,
			mysqlcontainers.WithDatabase(dbName),
			mysqlcontainers.WithUsername(dbUsername),
			mysqlcontainers.WithPassword(dbPassword),
			testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForLog("ready for connections").WithOccurrence(2)),
		)
	})
	must.NoError(t, err)
	must.NotNil(t, container)

	// Connect as root for admin operations (CREATE USER, GRANT, etc.).
	// WithDefaultCredentials sets MYSQL_ROOT_PASSWORD to the same value as MYSQL_PASSWORD.
	connStr := container.MustConnectionString(ctx, "allowCleartextPasswords=true", "multiStatements=true")
	// Replace the non-root user with root in the DSN.
	connStr = "root:" + dbPassword + "@" + connStr[strings.Index(connStr, "@")+1:]
	db, err := sql.Open("mysql", connStr)
	must.NoError(t, err)

	must.NoError(t, db.PingContext(ctx))

	return db, container
}

func TestQuoteIdent(T *testing.T) {
	T.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple identifier",
			input:    "users",
			expected: "`users`",
		},
		{
			name:     "identifier with spaces",
			input:    "user table",
			expected: "`user table`",
		},
		{
			name:     "identifier with backticks",
			input:    "user`name",
			expected: "`user``name`",
		},
		{
			name:     "identifier with multiple backticks",
			input:    "user``name",
			expected: "`user````name`",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "``",
		},
		{
			name:     "identifier with special characters",
			input:    "user-name_table",
			expected: "`user-name_table`",
		},
	}

	for _, tt := range tests {
		T.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := quoteIdent(tt.input)
			test.EqOp(t, tt.expected, result)
		})
	}
}

func TestQuoteLiteral(T *testing.T) {
	T.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple string",
			input:    "password",
			expected: `'password'`,
		},
		{
			name:     "string with single quotes",
			input:    "user's password",
			expected: `'user''s password'`,
		},
		{
			name:     "string with multiple single quotes",
			input:    "user''s password",
			expected: `'user''''s password'`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: `''`,
		},
		{
			name:     "string with special characters",
			input:    "p@ssw0rd!@#$%",
			expected: `'p@ssw0rd!@#$%'`,
		},
		{
			name:     "trailing backslash is doubled",
			input:    `secret\`,
			expected: `'secret\\'`,
		},
		{
			// MySQL treats backslash as an escape by default, so a naive
			// single-quote-only quoter would let `\'` escape the closing quote and
			// break out of the literal. Doubling the backslash neutralizes it: the
			// value stays entirely inside the quoted string.
			name:     "backslash-quote injection attempt does not break out",
			input:    `x\' OR 1=1 --`,
			expected: `'x\\'' OR 1=1 --'`,
		},
	}

	for _, tt := range tests {
		T.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := quoteLiteral(tt.input)
			test.EqOp(t, tt.expected, result)
		})
	}
}

func TestIsValidPrivilege(T *testing.T) {
	T.Parallel()

	T.Run("valid privileges", func(t *testing.T) {
		t.Parallel()

		validPrivileges := []Privilege{
			PrivilegeSelect,
			PrivilegeInsert,
			PrivilegeUpdate,
			PrivilegeDelete,
			PrivilegeReferences,
			PrivilegeTrigger,
			PrivilegeConnect,
		}

		for _, p := range validPrivileges {
			test.True(t, isValidPrivilege(p), test.Sprintf("expected %q to be valid", p))
		}
	})

	T.Run("invalid privilege", func(t *testing.T) {
		t.Parallel()
		test.False(t, isValidPrivilege("INVALID"))
	})
}

func TestManager_GrantUserAccessToTable_InvalidPrivilege(T *testing.T) {
	T.Parallel()

	T.Run("returns error for invalid privilege", func(t *testing.T) {
		t.Parallel()

		m := NewManager(nil)
		err := m.GrantUserAccessToTable(t.Context(), "user", "schema", "table", "INVALID")
		test.Error(t, err)
		test.StrContains(t, err.Error(), "invalid privilege")
	})
}

func TestNewManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		m := NewManager(nil)
		test.NotNil(t, m)
	})
}

func TestManager_CreateUser(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *mysqlcontainers.MySQLContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "testuser"
		password := "testpass123"

		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("duplicate user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *mysqlcontainers.MySQLContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "duplicateuser"
		password := "testpass123"

		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateUser(ctx, username, password)
		test.Error(t, err)
	})
}

func TestManager_DeleteUser(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *mysqlcontainers.MySQLContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tobedeleted"
		password := "testpass123"

		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.DeleteUser(ctx, username)
		test.NoError(t, err)

		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.False(t, exists)
	})

	T.Run("delete non-existent user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *mysqlcontainers.MySQLContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		err := mgr.DeleteUser(ctx, "nonexistentuser")
		test.NoError(t, err)
	})
}

func TestManager_CreateDatabase(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *mysqlcontainers.MySQLContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		owner := "dbowner"
		err := mgr.CreateUser(ctx, owner, "pass")
		must.NoError(t, err)

		dbName := "testdb"
		err = mgr.CreateDatabase(ctx, dbName, owner)
		test.NoError(t, err)

		exists, err := mgr.DatabaseExists(ctx, dbName)
		test.NoError(t, err)
		test.True(t, exists)
	})
}

func TestManager_DeleteDatabase(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *mysqlcontainers.MySQLContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		owner := "deldbowner"
		err := mgr.CreateUser(ctx, owner, "pass")
		must.NoError(t, err)

		dbName := "deldb"
		err = mgr.CreateDatabase(ctx, dbName, owner)
		must.NoError(t, err)

		err = mgr.DeleteDatabase(ctx, dbName)
		test.NoError(t, err)

		exists, err := mgr.DatabaseExists(ctx, dbName)
		test.NoError(t, err)
		test.False(t, exists)
	})
}

func TestManager_UserExists(T *testing.T) {
	T.Parallel()

	T.Run("nonexistent user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *mysqlcontainers.MySQLContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		exists, err := mgr.UserExists(ctx, "nonexistent_user_xyz")
		test.NoError(t, err)
		test.False(t, exists)
	})
}

func TestManager_DatabaseExists(T *testing.T) {
	T.Parallel()

	T.Run("nonexistent database", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *mysqlcontainers.MySQLContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		exists, err := mgr.DatabaseExists(ctx, "nonexistent_db_xyz")
		test.NoError(t, err)
		test.False(t, exists)
	})
}
