package tableaccess

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v2/database"
	"github.com/primandproper/platform-go/v2/errors"
)

type Privilege string

const (
	PrivilegeSelect     Privilege = "SELECT"
	PrivilegeInsert     Privilege = "INSERT"
	PrivilegeUpdate     Privilege = "UPDATE"
	PrivilegeDelete     Privilege = "DELETE"
	PrivilegeTruncate   Privilege = "TRUNCATE"
	PrivilegeReferences Privilege = "REFERENCES"
	PrivilegeTrigger    Privilege = "TRIGGER"
	PrivilegeConnect    Privilege = "CONNECT" // for database-level ops
)

func isValidPrivilege(p Privilege) bool {
	switch p {
	case PrivilegeSelect,
		PrivilegeInsert,
		PrivilegeUpdate,
		PrivilegeDelete,
		PrivilegeTruncate,
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

// quoteIdent safely wraps a Postgres identifier in double‑quotes,
// doubling any embedded double‑quotes per the SQL spec.
func quoteIdent(id string) string {
	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}

// quoteLiteral safely wraps a Postgres string literal in single‑quotes,
// doubling any embedded single‑quotes per the SQL spec.
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// CreateUser issues a CREATE USER with a safely-quoted password literal.
func (p *manager) CreateUser(ctx context.Context, username, password string) error {
	_, err := p.db.ExecContext(ctx, fmt.Sprintf(
		"CREATE USER %s WITH PASSWORD %s",
		quoteIdent(username),
		quoteLiteral(password),
	))
	return err
}

func (p *manager) DeleteUser(ctx context.Context, username string) error {
	_, err := p.db.ExecContext(ctx, fmt.Sprintf("DROP USER IF EXISTS %s", quoteIdent(username)))
	return err
}

func (p *manager) CreateDatabase(ctx context.Context, dbName, owner string) error {
	_, err := p.db.ExecContext(ctx, fmt.Sprintf(
		"CREATE DATABASE %s OWNER %s",
		quoteIdent(dbName),
		quoteIdent(owner),
	))
	return err
}

func (p *manager) DeleteDatabase(ctx context.Context, dbName string) error {
	_, err := p.db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", quoteIdent(dbName)))
	return err
}

func (p *manager) UserExists(ctx context.Context, username string) (bool, error) {
	var exists bool
	err := p.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, username).Scan(&exists)
	return exists, err
}

func (p *manager) DatabaseExists(ctx context.Context, dbName string) (bool, error) {
	var exists bool
	err := p.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&exists)
	return exists, err
}

func (p *manager) UserCanAccessDatabase(ctx context.Context, username, dbName string) (bool, error) {
	var hasPrivilege bool
	err := p.db.QueryRowContext(ctx, `SELECT has_database_privilege($1, $2, 'CONNECT')`, username, dbName).Scan(&hasPrivilege)
	return hasPrivilege, err
}

// GrantUserAccessToTable grants a specific privilege on a table to a user.
func (p *manager) GrantUserAccessToTable(ctx context.Context, username, schema, table, privilege string) error {
	if !isValidPrivilege(Privilege(privilege)) {
		return errors.Newf("invalid privilege: %s", privilege)
	}

	_, err := p.db.ExecContext(ctx, fmt.Sprintf("GRANT %s ON TABLE %s TO %s", privilege, fmt.Sprintf("%s.%s", quoteIdent(schema), quoteIdent(table)), quoteIdent(username)))
	return err
}
