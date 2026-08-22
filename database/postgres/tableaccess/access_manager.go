package tableaccess

import (
	"context"
	"database/sql"
	stderrors "errors"
	"fmt"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/jackc/pgx/v5/pgconn"
)

// serviceName scopes this package's spans and logger.
const serviceName = "postgres_table_access"

// Span and log attribute keys. Credentials are never among them: CreateUser
// takes a password and nothing here records it, on the span or anywhere else.
const (
	usernameKey  = "table_access.username"
	databaseKey  = "table_access.database"
	schemaKey    = "table_access.schema"
	tableKey     = "table_access.table"
	privilegeKey = "table_access.privilege"
	existsKey    = "table_access.exists"
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

var _ database.Manager = (*Manager)(nil)

// Manager is the PostgreSQL database.Manager implementation. It is exported,
// and returned by NewManager, so a caller who has chosen PostgreSQL can depend on
// that choice rather than on the interface every dialect's manager shares.
type Manager struct {
	db   *sql.DB
	o11y observability.Observer
}

func NewManager(db *sql.DB, opts ...Option) *Manager {
	o := newOptions(opts)

	return &Manager{
		db:   db,
		o11y: observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}
}

// bindCreateUserArgs stashes the new role's name and password in transaction-local
// settings. Both travel as bind parameters, so neither reaches the statement text.
//
// The setting names are two-part on purpose: Postgres accepts a custom setting
// only under an "extension.name" spelling, and rejects anything with more or
// fewer dots.
const bindCreateUserArgs = `SELECT set_config('tableaccess.create_user_username', $1, true),
       set_config('tableaccess.create_user_password', $2, true)`

// createUserFromSettings reads those settings back and quotes them server-side.
// format's %I and %L are Postgres' own identifier and literal quoting — the same
// job quoteIdent does here, done where the credential does not have to be spelled
// out to get there.
const createUserFromSettings = `DO $do$
BEGIN
	EXECUTE format(
		'CREATE USER %I WITH PASSWORD %L',
		current_setting('tableaccess.create_user_username'),
		current_setting('tableaccess.create_user_password')
	);
END
$do$`

// duplicateObject is the SQLSTATE Postgres raises for CREATE USER against a
// role that already exists. There is no CREATE USER IF NOT EXISTS to lean on, so
// the code is the only thing that separates "somebody already made this user"
// from a connection that dropped mid-statement.
//
// A DO block does not swallow it: nothing here catches the exception, so
// PL/pgSQL re-raises it with its SQLSTATE intact and the driver still hands back
// a *pgconn.PgError carrying this code.
const duplicateObject = "42710"

// CreateUser creates a role with the given password.
//
// The password never appears in statement text. CREATE USER is a utility
// statement and accepts no bind parameters, so the direct spelling has to
// interpolate the credential into the SQL — and otelsql copies statement text
// onto the db.statement span attribute whenever LOG_QUERIES is on, which puts a
// live credential on a span that may well be exported to a third party. Binding
// the arguments into settings and letting the server do the quoting means every
// statement that goes over the wire is a constant.
//
// The transaction is what scopes the settings: set_config's local flag ties them
// to it, so they are gone whether it commits or rolls back, and no later caller
// on a pooled connection can read them. CREATE ROLE is transactional in Postgres,
// so the role and the settings share one unit of work.
//
// A username already in use comes back wrapping database.ErrUserAlreadyExists,
// which errors/http and errors/grpc map to a conflict rather than a 500. The
// driver's own error is preserved underneath it: the SQLSTATE is what identified
// the failure, and a caller that wants the detail should not have to re-run the
// statement to get it.
func (p *Manager) CreateUser(ctx context.Context, username, password string) (err error) {
	ctx, op := p.o11y.Begin(ctx, observability.WithValue(usernameKey, username))
	defer op.End()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return op.Error(err, "beginning create user transaction")
	}

	defer func() {
		// Rolling back an already-committed transaction is ErrTxDone and means
		// only that the happy path happened; anything else is a connection in a
		// state the caller should hear about.
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !stderrors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, op.Error(rollbackErr, "rolling back create user transaction"))
		}
	}()

	if _, err = tx.ExecContext(ctx, bindCreateUserArgs, username, password); err != nil {
		return op.Error(err, "binding create user arguments")
	}

	if _, err = tx.ExecContext(ctx, createUserFromSettings); err != nil {
		var pgErr *pgconn.PgError
		if stderrors.As(err, &pgErr) && pgErr.Code == duplicateObject {
			return op.Error(errors.Join(database.ErrUserAlreadyExists, err), "creating user")
		}

		return op.Error(err, "creating user")
	}

	if err = tx.Commit(); err != nil {
		return op.Error(err, "committing create user transaction")
	}

	return nil
}

func (p *Manager) DeleteUser(ctx context.Context, username string) error {
	ctx, op := p.o11y.Begin(ctx, observability.WithValue(usernameKey, username))
	defer op.End()

	if _, err := p.db.ExecContext(ctx, fmt.Sprintf("DROP USER IF EXISTS %s", dialect.Postgres.QuoteIdentifier(username))); err != nil {
		return op.Error(err, "dropping user")
	}

	return nil
}

func (p *Manager) CreateDatabase(ctx context.Context, dbName, owner string) error {
	ctx, op := p.o11y.Begin(ctx,
		observability.WithValue(databaseKey, dbName),
		observability.WithValue(usernameKey, owner),
	)
	defer op.End()

	if _, err := p.db.ExecContext(ctx, fmt.Sprintf(
		"CREATE DATABASE %s OWNER %s",
		dialect.Postgres.QuoteIdentifier(dbName),
		dialect.Postgres.QuoteIdentifier(owner),
	)); err != nil {
		return op.Error(err, "creating database")
	}

	return nil
}

func (p *Manager) DeleteDatabase(ctx context.Context, dbName string) error {
	ctx, op := p.o11y.Begin(ctx, observability.WithValue(databaseKey, dbName))
	defer op.End()

	if _, err := p.db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", dialect.Postgres.QuoteIdentifier(dbName))); err != nil {
		return op.Error(err, "dropping database")
	}

	return nil
}

func (p *Manager) UserExists(ctx context.Context, username string) (bool, error) {
	ctx, op := p.o11y.Begin(ctx, observability.WithValue(usernameKey, username))
	defer op.End()

	var exists bool
	if err := p.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, username).Scan(&exists); err != nil {
		return false, op.Error(err, "checking whether user exists")
	}

	op.SpanOnly(existsKey, exists)

	return exists, nil
}

func (p *Manager) DatabaseExists(ctx context.Context, dbName string) (bool, error) {
	ctx, op := p.o11y.Begin(ctx, observability.WithValue(databaseKey, dbName))
	defer op.End()

	var exists bool
	if err := p.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, dbName).Scan(&exists); err != nil {
		return false, op.Error(err, "checking whether database exists")
	}

	op.SpanOnly(existsKey, exists)

	return exists, nil
}

func (p *Manager) UserCanAccessDatabase(ctx context.Context, username, dbName string) (bool, error) {
	ctx, op := p.o11y.Begin(ctx,
		observability.WithValue(usernameKey, username),
		observability.WithValue(databaseKey, dbName),
	)
	defer op.End()

	var hasPrivilege bool
	if err := p.db.QueryRowContext(ctx, `SELECT has_database_privilege($1, $2, 'CONNECT')`, username, dbName).Scan(&hasPrivilege); err != nil {
		return false, op.Error(err, "checking whether user can access database")
	}

	return hasPrivilege, nil
}

// GrantUserAccessToTable grants a specific privilege on a table to a user.
func (p *Manager) GrantUserAccessToTable(ctx context.Context, username, schema, table, privilege string) error {
	ctx, op := p.o11y.Begin(ctx,
		observability.WithValue(usernameKey, username),
		observability.WithValue(schemaKey, schema),
		observability.WithValue(tableKey, table),
		observability.WithValue(privilegeKey, privilege),
	)
	defer op.End()

	if !isValidPrivilege(Privilege(privilege)) {
		return op.Error(errors.Newf("invalid privilege: %s", privilege), "granting table access")
	}

	if _, err := p.db.ExecContext(ctx, fmt.Sprintf("GRANT %s ON TABLE %s TO %s", privilege, fmt.Sprintf("%s.%s", dialect.Postgres.QuoteIdentifier(schema), dialect.Postgres.QuoteIdentifier(table)), dialect.Postgres.QuoteIdentifier(username))); err != nil {
		return op.Error(err, "granting table access")
	}

	return nil
}
