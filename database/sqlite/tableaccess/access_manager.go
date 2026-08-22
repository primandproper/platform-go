// Package tableaccess is the SQLite database.Manager, and every one of its
// operations refuses.
//
// SQLite has no users, no roles, no grants, and no notion of several databases
// on one server: a database is a file, and access to it is access to the file.
// There is nothing for the interface's methods to do, so each reports
// ErrNotSupported rather than pretending to have provisioned something.
//
// It exists so that dialect selection stays uniform — a service configured
// against SQLite still resolves a database.Manager — and so the refusal is
// visible. Every method opens a span and records the error, because a caller
// finding out at runtime which dialect it was handed should see the refusal in
// the same trace it would have seen the grant in; a refusal that leaves no trace
// cannot be told from a call that never happened.
//
// Provisioning a SQLite deployment is a filesystem matter: file permissions and
// where the file lives, not statements sent to a server.
package tableaccess

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
)

// serviceName scopes this package's spans and logger.
const serviceName = "sqlite_table_access"

// ErrNotSupported is returned for operations that SQLite does not support.
// SQLite has no concept of users, roles, permissions, or multiple databases.
var ErrNotSupported = errors.New("operation not supported by SQLite")

var _ database.Manager = (*Manager)(nil)

// Manager is the SQLite database.Manager implementation: every operation
// reports ErrNotSupported, because SQLite has no users, roles, or grants. It is
// exported, and returned by NewManager, so a caller who has chosen SQLite can
// depend on that choice rather than on the interface every dialect's manager
// shares.
//
// It is observable anyway, and for the same reason its siblings are: a caller
// that has been handed a database.Manager and is finding out at runtime which
// dialect it got should see the refusal in the same trace it would have seen
// the grant in. A refusal that leaves no trace is indistinguishable from a call
// that never happened.
type Manager struct {
	o11y observability.Observer
}

func NewManager(opts ...Option) *Manager {
	o := newOptions(opts)

	return &Manager{o11y: observability.NewObserver(serviceName, o.logger, o.tracerProvider)}
}

// refuse records the refusal and returns it. Every method here is one call to
// this: what is worth observing is that something asked, not which of the eight
// unsupported things it asked for — the span carries that in its name.
func (m *Manager) refuse(ctx context.Context, description string) error {
	_, op := m.o11y.BeginCustom(ctx, serviceName+"."+description)
	defer op.End()

	return op.Error(ErrNotSupported, "%s", description)
}

func (m *Manager) CreateUser(ctx context.Context, _, _ string) error {
	return m.refuse(ctx, "creating user")
}

func (m *Manager) DeleteUser(ctx context.Context, _ string) error {
	return m.refuse(ctx, "dropping user")
}

func (m *Manager) CreateDatabase(ctx context.Context, _, _ string) error {
	return m.refuse(ctx, "creating database")
}

func (m *Manager) DeleteDatabase(ctx context.Context, _ string) error {
	return m.refuse(ctx, "dropping database")
}

func (m *Manager) UserExists(ctx context.Context, _ string) (bool, error) {
	return false, m.refuse(ctx, "checking whether user exists")
}

func (m *Manager) DatabaseExists(ctx context.Context, _ string) (bool, error) {
	return false, m.refuse(ctx, "checking whether database exists")
}

func (m *Manager) GrantUserAccessToTable(ctx context.Context, _, _, _, _ string) error {
	return m.refuse(ctx, "granting table access")
}

func (m *Manager) UserCanAccessDatabase(ctx context.Context, _, _ string) (bool, error) {
	return false, m.refuse(ctx, "checking whether user can access database")
}
