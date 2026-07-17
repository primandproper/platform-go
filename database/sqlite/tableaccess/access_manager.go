package tableaccess

import (
	"context"

	"github.com/primandproper/platform-go/v5/database"
	"github.com/primandproper/platform-go/v5/errors"
)

// ErrNotSupported is returned for operations that SQLite does not support.
// SQLite has no concept of users, roles, permissions, or multiple databases.
var ErrNotSupported = errors.New("operation not supported by SQLite")

type manager struct{}

func NewManager() database.Manager {
	return &manager{}
}

func (m *manager) CreateUser(_ context.Context, _, _ string) error {
	return ErrNotSupported
}

func (m *manager) DeleteUser(_ context.Context, _ string) error {
	return ErrNotSupported
}

func (m *manager) CreateDatabase(_ context.Context, _, _ string) error {
	return ErrNotSupported
}

func (m *manager) DeleteDatabase(_ context.Context, _ string) error {
	return ErrNotSupported
}

func (m *manager) UserExists(_ context.Context, _ string) (bool, error) {
	return false, ErrNotSupported
}

func (m *manager) DatabaseExists(_ context.Context, _ string) (bool, error) {
	return false, ErrNotSupported
}

func (m *manager) GrantUserAccessToTable(_ context.Context, _, _, _, _ string) error {
	return ErrNotSupported
}

func (m *manager) UserCanAccessDatabase(_ context.Context, _, _ string) (bool, error) {
	return false, ErrNotSupported
}
