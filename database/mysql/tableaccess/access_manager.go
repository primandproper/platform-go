package tableaccess

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v5/database"
	"github.com/primandproper/platform-go/v5/errors"
)

type Privilege string

const (
	PrivilegeSelect     Privilege = "SELECT"
	PrivilegeInsert     Privilege = "INSERT"
	PrivilegeUpdate     Privilege = "UPDATE"
	PrivilegeDelete     Privilege = "DELETE"
	PrivilegeReferences Privilege = "REFERENCES"
	PrivilegeTrigger    Privilege = "TRIGGER"
	PrivilegeConnect    Privilege = "CONNECT"
)

func isValidPrivilege(p Privilege) bool {
	switch p {
	case PrivilegeSelect,
		PrivilegeInsert,
		PrivilegeUpdate,
		PrivilegeDelete,
		PrivilegeReferences,
		PrivilegeTrigger,
		PrivilegeConnect:
		return true
	default:
		return false
	}
}

type manager struct {
	db *sql.DB
}

func NewManager(db *sql.DB) database.Manager {
	return &manager{db: db}
}

// quoteIdent safely wraps a MySQL identifier in backticks,
// doubling any embedded backticks per MySQL quoting rules.
func quoteIdent(id string) string {
	return "`" + strings.ReplaceAll(id, "`", "``") + "`"
}

// quoteLiteral safely wraps a MySQL string literal in single-quotes. MySQL (unlike
// standard SQL) treats backslash as an escape character by default, so a value
// ending in a backslash would otherwise escape the closing quote and break out of
// the literal. Double both backslashes and single-quotes to neutralize that. This
// assumes the default (NO_BACKSLASH_ESCAPES off) SQL mode.
func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return `'` + s + `'`
}

// CreateUser issues a CREATE USER with a safely-quoted password literal.
func (m *manager) CreateUser(ctx context.Context, username, password string) error {
	_, err := m.db.ExecContext(ctx, fmt.Sprintf(
		"CREATE USER %s@'%%' IDENTIFIED BY %s",
		quoteLiteral(username),
		quoteLiteral(password),
	))
	return err
}

func (m *manager) DeleteUser(ctx context.Context, username string) error {
	_, err := m.db.ExecContext(ctx, fmt.Sprintf("DROP USER IF EXISTS %s@'%%'", quoteLiteral(username)))
	return err
}

func (m *manager) CreateDatabase(ctx context.Context, dbName, owner string) error {
	if _, err := m.db.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %s", quoteIdent(dbName))); err != nil {
		return err
	}

	// MySQL has no OWNER concept; grant all privileges instead.
	_, err := m.db.ExecContext(ctx, fmt.Sprintf(
		"GRANT ALL PRIVILEGES ON %s.* TO %s@'%%'",
		quoteIdent(dbName),
		quoteLiteral(owner),
	))
	return err
}

func (m *manager) DeleteDatabase(ctx context.Context, dbName string) error {
	_, err := m.db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(dbName)))
	return err
}

func (m *manager) UserExists(ctx context.Context, username string) (bool, error) {
	var exists bool
	err := m.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM mysql.user WHERE User = ? AND Host = '%')`, username).Scan(&exists)
	return exists, err
}

func (m *manager) DatabaseExists(ctx context.Context, dbName string) (bool, error) {
	var exists bool
	err := m.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?)`, dbName).Scan(&exists)
	return exists, err
}

func (m *manager) UserCanAccessDatabase(ctx context.Context, username, dbName string) (bool, error) {
	var count int
	err := m.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.SCHEMA_PRIVILEGES WHERE GRANTEE = CONCAT('''', ?, '''@''%''') AND TABLE_SCHEMA = ?`,
		username, dbName,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GrantUserAccessToTable grants a specific privilege on a table to a user.
func (m *manager) GrantUserAccessToTable(ctx context.Context, username, schema, table, privilege string) error {
	if !isValidPrivilege(Privilege(privilege)) {
		return errors.Newf("invalid privilege: %s", privilege)
	}

	_, err := m.db.ExecContext(ctx, fmt.Sprintf(
		"GRANT %s ON %s.%s TO %s@'%%'",
		privilege,
		quoteIdent(schema),
		quoteIdent(table),
		quoteLiteral(username),
	))
	return err
}
