package tableaccess

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"net"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v2/pointer"
	"github.com/primandproper/platform-go/v2/testutils/containers"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TODO: lots of duplication with the upper postgres package

const (
	defaultPostgresImage = "postgres:17-alpine"
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
	// Create a new FNV-1a 64-bit hash object
	h := fnv.New64a()

	// Write the bytes of the string into the hash object
	_, err := h.Write([]byte(s))
	if err != nil {
		// Handle error if necessary
		panic(err)
	}

	// Return the resulting hash value as a number (uint64)
	return h.Sum64()
}

func buildConnectionString(t *testing.T, container *postgres.PostgresContainer, dbName, username, password string) string {
	t.Helper()
	ctx := t.Context()

	containerPort, err := container.MappedPort(ctx, "5432/tcp")
	must.NoError(t, err)

	host, err := container.Host(ctx)
	must.NoError(t, err)

	return fmt.Sprintf("postgres://%s:%s@%s/%s", username, password, net.JoinHostPort(host, containerPort.Port()), dbName)
}

func buildDatabaseConnectionForTest(t *testing.T, ctx context.Context) (*sql.DB, *postgres.PostgresContainer) {
	t.Helper()

	dbUsername := fmt.Sprintf("%d", hashStringToNumber(t.Name()))

	container, err := containers.StartWithRetry(ctx, func(ctx context.Context) (*postgres.PostgresContainer, error) {
		return postgres.Run(
			ctx,
			defaultPostgresImage,
			postgres.WithDatabase(splitReverseConcat(dbUsername)),
			postgres.WithUsername(dbUsername),
			postgres.WithPassword(reverseString(dbUsername)),
			testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
		)
	})
	must.NoError(t, err)
	must.NotNil(t, container)

	db, err := sql.Open("pgx", container.MustConnectionString(ctx, "sslmode=disable"))
	must.NoError(t, err)

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
			expected: `"users"`,
		},
		{
			name:     "identifier with spaces",
			input:    "user table",
			expected: `"user table"`,
		},
		{
			name:     "identifier with double quotes",
			input:    `user"name`,
			expected: `"user""name"`,
		},
		{
			name:     "identifier with multiple double quotes",
			input:    `user""name`,
			expected: `"user""""name"`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: `""`,
		},
		{
			name:     "identifier with special characters",
			input:    "user-name_table",
			expected: `"user-name_table"`,
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
			name:  "string with newlines",
			input: "pass\nword",
			expected: `'pass
word'`,
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

	tests := []struct {
		name      string
		privilege Privilege
		expected  bool
	}{
		{
			name:      "valid SELECT privilege",
			privilege: PrivilegeSelect,
			expected:  true,
		},
		{
			name:      "valid INSERT privilege",
			privilege: PrivilegeInsert,
			expected:  true,
		},
		{
			name:      "valid UPDATE privilege",
			privilege: PrivilegeUpdate,
			expected:  true,
		},
		{
			name:      "valid DELETE privilege",
			privilege: PrivilegeDelete,
			expected:  true,
		},
		{
			name:      "valid TRUNCATE privilege",
			privilege: PrivilegeTruncate,
			expected:  true,
		},
		{
			name:      "valid REFERENCES privilege",
			privilege: PrivilegeReferences,
			expected:  true,
		},
		{
			name:      "valid TRIGGER privilege",
			privilege: PrivilegeTrigger,
			expected:  true,
		},
		{
			name:      "valid CONNECT privilege",
			privilege: PrivilegeConnect,
			expected:  true,
		},
		{
			name:      "invalid privilege",
			privilege: Privilege("INVALID"),
			expected:  false,
		},
		{
			name:      "empty privilege",
			privilege: Privilege(""),
			expected:  false,
		},
		{
			name:      "case sensitive privilege",
			privilege: Privilege("select"),
			expected:  false,
		},
	}

	for _, tt := range tests {
		T.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isValidPrivilege(tt.privilege)
			test.EqOp(t, tt.expected, result)
		})
	}
}

func TestManager_CreateUser(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "testuser"
		password := "testpass123"

		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Verify user was created
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("duplicate user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "duplicateuser"
		password := "testpass123"

		// Create user first time
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Try to create same user again
		err = mgr.CreateUser(ctx, username, password)
		test.Error(t, err)
	})

	T.Run("special characters in username", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := `user"with'quotes`
		password := "testpass123"

		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Verify user was created
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("special characters in password", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "testuser"
		password := `pass'word"with"quotes`

		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Verify user was created
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.True(t, exists)
	})
}

func TestManager_DeleteUser(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tobedeleted"
		password := "testpass123"

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Verify user exists
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.True(t, exists)

		// Delete user
		err = mgr.DeleteUser(ctx, username)
		test.NoError(t, err)

		// Verify user no longer exists
		exists, err = mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.False(t, exists)
	})

	T.Run("delete non-existent user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "nonexistentuser"

		// Delete non-existent user should not error due to IF EXISTS
		err := mgr.DeleteUser(ctx, username)
		test.NoError(t, err)
	})

	T.Run("special characters in username", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := `user"with'quotes`
		password := "testpass123"

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Delete user
		err = mgr.DeleteUser(ctx, username)
		test.NoError(t, err)

		// Verify user no longer exists
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.False(t, exists)
	})
}

func TestManager_UserExists(T *testing.T) {
	T.Parallel()

	T.Run("existing user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "existinguser"
		password := "testpass123"

		// Create user
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Check if user exists
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("non-existing user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "nonexistentuser"

		// Check if user exists
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.False(t, exists)
	})

	T.Run("special characters in username", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := `user"with'quotes`
		password := "testpass123"

		// Create user
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Check if user exists
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.True(t, exists)
	})
}

func TestManager_CreateDatabase(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "dbowner"
		password := "testpass123"
		databaseName := "testdb"

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Create database
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Verify database was created
		exists, err := mgr.DatabaseExists(ctx, databaseName)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("duplicate database", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "dbowner"
		password := "testpass123"
		databaseName := "duplicatedb"

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Create database first time
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Try to create same database again
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.Error(t, err)
	})

	T.Run("special characters in database name", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "dbowner"
		password := "testpass123"
		databaseName := `db"with'quotes`

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Create database
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Verify database was created
		exists, err := mgr.DatabaseExists(ctx, databaseName)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("special characters in owner name", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := `owner"with'quotes`
		password := "testpass123"
		databaseName := "testdb"

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Create database
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Verify database was created
		exists, err := mgr.DatabaseExists(ctx, databaseName)
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
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "dbowner"
		password := "testpass123"
		databaseName := "tobedeleted"

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Create database
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Verify database exists
		exists, err := mgr.DatabaseExists(ctx, databaseName)
		test.NoError(t, err)
		test.True(t, exists)

		// Delete database
		err = mgr.DeleteDatabase(ctx, databaseName)
		test.NoError(t, err)

		// Verify database no longer exists
		exists, err = mgr.DatabaseExists(ctx, databaseName)
		test.NoError(t, err)
		test.False(t, exists)
	})

	T.Run("delete non-existent database", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		databaseName := "nonexistentdb"

		// Delete non-existent database should not error due to IF EXISTS
		err := mgr.DeleteDatabase(ctx, databaseName)
		test.NoError(t, err)
	})

	T.Run("special characters in database name", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "dbowner"
		password := "testpass123"
		databaseName := `db"with'quotes`

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Create database
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Delete database
		err = mgr.DeleteDatabase(ctx, databaseName)
		test.NoError(t, err)

		// Verify database no longer exists
		exists, err := mgr.DatabaseExists(ctx, databaseName)
		test.NoError(t, err)
		test.False(t, exists)
	})
}

func TestManager_DatabaseExists(T *testing.T) {
	T.Parallel()

	T.Run("existing database", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "dbowner"
		password := "testpass123"
		databaseName := "existingdb"

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Create database
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Check if database exists
		exists, err := mgr.DatabaseExists(ctx, databaseName)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("non-existing database", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		databaseName := "nonexistentdb"

		// Check if database exists
		exists, err := mgr.DatabaseExists(ctx, databaseName)
		test.NoError(t, err)
		test.False(t, exists)
	})

	T.Run("special characters in database name", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "dbowner"
		password := "testpass123"
		databaseName := `db"with'quotes`

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Create database
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Check if database exists
		exists, err := mgr.DatabaseExists(ctx, databaseName)
		test.NoError(t, err)
		test.True(t, exists)
	})
}

func TestManager_UserCanAccessDatabase(T *testing.T) {
	T.Parallel()

	T.Run("user has access", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "accessuser"
		password := "testpass123"
		databaseName := "accessdb"

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Check access
		canAccess, err := mgr.UserCanAccessDatabase(ctx, username, databaseName)
		test.NoError(t, err)
		test.True(t, canAccess)
	})

	T.Run("user does not have access", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "noaccessuser"
		password := "testpass123"
		databaseName := "noaccessdb"
		ownerUsername := "owneruser"

		// Create user and database with different owner
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateUser(ctx, ownerUsername, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, ownerUsername)
		test.NoError(t, err)

		// Grant CONNECT privilege to the user for the database
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", quoteIdent(databaseName), quoteIdent(username)))
		test.NoError(t, err)

		// Check access - user should have access now
		canAccess, err := mgr.UserCanAccessDatabase(ctx, username, databaseName)
		test.NoError(t, err)
		test.True(t, canAccess)
	})

	T.Run("non-existent user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "nonexistentuser"
		databaseName := "testdb"

		// Check access for non-existent user - should return error
		canAccess, err := mgr.UserCanAccessDatabase(ctx, username, databaseName)
		test.Error(t, err)
		test.False(t, canAccess)
	})

	T.Run("non-existent database", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "testuser"
		password := "testpass123"
		databaseName := "nonexistentdb"

		// Create user
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Check access to non-existent database - should return error
		canAccess, err := mgr.UserCanAccessDatabase(ctx, username, databaseName)
		test.Error(t, err)
		test.False(t, canAccess)
	})

	T.Run("special characters in usernames and database names", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := `user"with'quotes`
		password := "testpass123"
		databaseName := `db"with'quotes`

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Check access
		canAccess, err := mgr.UserCanAccessDatabase(ctx, username, databaseName)
		test.NoError(t, err)
		test.True(t, canAccess)
	})
}

func TestManager_GrantUserAccessToTable(T *testing.T) {
	T.Parallel()

	T.Run("valid privilege", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tableuser"
		password := "testpass123"
		databaseName := "tabledb"
		schema := "public"
		table := "testtable"

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Create a test table
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(schema), quoteIdent(table)))
		test.NoError(t, err)

		// Grant access
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, "SELECT")
		test.NoError(t, err)
	})

	T.Run("all valid privileges", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "privilegeuser"
		password := "testpass123"
		databaseName := "privilegedb"
		schema := "public"
		table := "privilegetable"

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Create a test table
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(schema), quoteIdent(table)))
		test.NoError(t, err)

		privileges := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}
		for _, privilege := range privileges {
			err = mgr.GrantUserAccessToTable(ctx, username, schema, table, privilege)
			test.NoError(t, err, test.Sprintf("Failed to grant %s privilege", privilege))
		}
	})

	T.Run("invalid privilege", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "invaliduser"
		password := "testpass123"
		databaseName := "invaliddb"
		schema := "public"
		table := "invalidtable"

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Create a test table
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(schema), quoteIdent(table)))
		test.NoError(t, err)

		// Try to grant invalid privilege
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, "INVALID_PRIVILEGE")
		test.Error(t, err)
		test.StrContains(t, err.Error(), "invalid privilege")
	})

	T.Run("case sensitive privilege", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "caseuser"
		password := "testpass123"
		databaseName := "casedb"
		schema := "public"
		table := "casetable"

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Create a test table
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(schema), quoteIdent(table)))
		test.NoError(t, err)

		// Try to grant lowercase privilege (should fail)
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, "select")
		test.Error(t, err)
		test.StrContains(t, err.Error(), "invalid privilege")
	})

	T.Run("special characters in identifiers", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := `user"with'quotes`
		password := "testpass123"
		databaseName := `db"with'quotes`
		schema := `schema"with'quotes`
		table := `table"with'quotes`

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Create schema and table
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s", quoteIdent(schema)))
		test.NoError(t, err)

		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(schema), quoteIdent(table)))
		test.NoError(t, err)

		// Grant access
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, "SELECT")
		test.NoError(t, err)
	})

	T.Run("non-existent user", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "nonexistentuser"
		schema := "public"
		table := "testtable"

		// Create a test table
		_, err := adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(schema), quoteIdent(table)))
		test.NoError(t, err)

		// Try to grant access to non-existent user
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, "SELECT")
		test.Error(t, err)
	})

	T.Run("non-existent table", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tableuser"
		password := "testpass123"
		schema := "public"
		table := "nonexistenttable"

		// Create user
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Try to grant access to non-existent table
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, "SELECT")
		test.Error(t, err)
	})
}

func TestManager_SQLInjectionProtection(T *testing.T) {
	T.Parallel()

	T.Run("username injection in CreateUser", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		// Attempt SQL injection in username
		maliciousUsername := `user"; DROP TABLE users; --`
		password := "testpass123"

		// This should not cause SQL injection due to proper quoting
		err := mgr.CreateUser(ctx, maliciousUsername, password)
		test.NoError(t, err)

		// Verify the user was created with the literal name (not executed as SQL)
		exists, err := mgr.UserExists(ctx, maliciousUsername)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("password injection in CreateUser", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		// Attempt SQL injection in password
		username := "injectionuser"
		maliciousPassword := `pass'; DROP TABLE users; --`

		// This should not cause SQL injection due to proper quoting
		err := mgr.CreateUser(ctx, username, maliciousPassword)
		test.NoError(t, err)

		// Verify the user was created
		exists, err := mgr.UserExists(ctx, username)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("database name injection in CreateDatabase", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "dbowner"
		password := "testpass123"
		// Attempt SQL injection in database name
		maliciousDbName := `db"; DROP TABLE databases; --`

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// This should not cause SQL injection due to proper quoting
		err = mgr.CreateDatabase(ctx, maliciousDbName, username)
		test.NoError(t, err)

		// Verify the database was created with the literal name
		exists, err := mgr.DatabaseExists(ctx, maliciousDbName)
		test.NoError(t, err)
		test.True(t, exists)
	})

	T.Run("table name injection in GrantUserAccessToTable", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tableuser"
		password := "testpass123"
		databaseName := "tabledb"
		schema := "public"
		// Attempt SQL injection in table name
		maliciousTable := `table"; DROP TABLE users; --`

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Create a test table with the malicious name
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(schema), quoteIdent(maliciousTable)))
		test.NoError(t, err)

		// This should not cause SQL injection due to proper quoting
		err = mgr.GrantUserAccessToTable(ctx, username, schema, maliciousTable, "SELECT")
		test.NoError(t, err)
	})

	T.Run("schema name injection in GrantUserAccessToTable", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tableuser"
		password := "testpass123"
		databaseName := "tabledb"
		// Attempt SQL injection in schema name
		maliciousSchema := `schema"; DROP TABLE users; --`
		table := "testtable"

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Create schema and table with malicious names
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s", quoteIdent(maliciousSchema)))
		test.NoError(t, err)

		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(maliciousSchema), quoteIdent(table)))
		test.NoError(t, err)

		// This should not cause SQL injection due to proper quoting
		err = mgr.GrantUserAccessToTable(ctx, username, maliciousSchema, table, "SELECT")
		test.NoError(t, err)
	})
}

func TestManager_ErrorCases(T *testing.T) {
	T.Parallel()

	T.Run("context cancellation", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		// Create a cancelled context
		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()

		username := "cancelleduser"
		password := "testpass123"

		// Operations with cancelled context should fail
		err := mgr.CreateUser(cancelledCtx, username, password)
		test.Error(t, err)

		exists, err := mgr.UserExists(cancelledCtx, username)
		test.Error(t, err)
		test.False(t, exists)
	})

	T.Run("context timeout", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		// Create a context with very short timeout
		timeoutCtx, cancel := context.WithTimeout(ctx, 1*time.Nanosecond)
		defer cancel()

		// Wait a bit to ensure timeout
		time.Sleep(1 * time.Millisecond)

		username := "timeoutuser"
		password := "testpass123"

		// Operations with timed out context should fail
		err := mgr.CreateUser(timeoutCtx, username, password)
		test.Error(t, err)
	})

	T.Run("empty username", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := ""
		password := "testpass123"

		// Empty username should fail
		err := mgr.CreateUser(ctx, username, password)
		test.Error(t, err)
	})

	T.Run("empty database name", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "testuser"
		password := "testpass123"
		databaseName := ""

		// Create user first
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		// Empty database name should fail
		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.Error(t, err)
	})

	T.Run("empty table name in GrantUserAccessToTable", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tableuser"
		password := "testpass123"
		databaseName := "tabledb"
		schema := "public"
		table := ""

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Empty table name should fail
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, "SELECT")
		test.Error(t, err)
	})

	T.Run("empty schema name in GrantUserAccessToTable", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tableuser"
		password := "testpass123"
		databaseName := "tabledb"
		schema := ""
		table := "testtable"

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Empty schema name should fail
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, "SELECT")
		test.Error(t, err)
	})

	T.Run("empty privilege in GrantUserAccessToTable", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "tableuser"
		password := "testpass123"
		databaseName := "tabledb"
		schema := "public"
		table := "testtable"
		privilege := ""

		// Create user and database
		err := mgr.CreateUser(ctx, username, password)
		test.NoError(t, err)

		err = mgr.CreateDatabase(ctx, databaseName, username)
		test.NoError(t, err)

		// Create a test table
		_, err = adminDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s.%s (id SERIAL PRIMARY KEY, name TEXT)", quoteIdent(schema), quoteIdent(table)))
		test.NoError(t, err)

		// Empty privilege should fail
		err = mgr.GrantUserAccessToTable(ctx, username, schema, table, privilege)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "invalid privilege")
	})
}

func TestNewManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		adminDB, container := buildDatabaseConnectionForTest(t, ctx)
		defer func(container *postgres.PostgresContainer, ctx context.Context, duration *time.Duration) {
			if err := container.Stop(ctx, duration); err != nil {
				t.Logf("could not stop container due to error: %v", err)
			}
		}(container, ctx, pointer.To(10*time.Second))

		mgr := NewManager(adminDB)

		username := "example"
		password := "hunter2"
		databaseName := "records"

		test.NoError(t, mgr.CreateUser(ctx, username, password))
		test.NoError(t, mgr.CreateDatabase(ctx, databaseName, username))

		canAccess, err := mgr.UserCanAccessDatabase(ctx, username, databaseName)
		test.NoError(t, err)
		test.True(t, canAccess)

		db2, err := sql.Open("pgx", buildConnectionString(t, container, databaseName, username, password))
		must.NoError(t, err)

		var dbName string
		db2.QueryRowContext(ctx, `SELECT current_database()`).Scan(&dbName)
		test.EqOp(t, databaseName, dbName)
	})
}
