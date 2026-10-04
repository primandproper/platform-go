// Package txcount counts the statements a store sends through a transaction,
// and holds nothing else.
//
// It exists for one claim the store hooks make: that a store handed NoopHooks
// pays nothing for them, because the read of the row an update is about to
// overwrite is made only for hooks that might use it. That claim is a statement
// count, and it is counted on the Tx rather than on a store's generated
// querier because the Tx has four methods and the querier has dozens — a
// counting querier that forgets to override one undercounts, and nothing says
// so. The Tx cannot be got wrong that way.
package txcount

import (
	"context"
	"database/sql"
	"sync/atomic"

	"github.com/primandproper/primitives-go/v2/database"
)

// Tx is a database.Tx that counts every statement sent through it.
type Tx struct {
	database.Tx

	statements atomic.Int64
}

var _ database.Tx = (*Tx)(nil)

// Wrap counts the statements sent through tx from here on.
func Wrap(tx database.Tx) *Tx {
	return &Tx{Tx: tx}
}

// Statements is how many statements have been sent through the Tx so far.
func (t *Tx) Statements() int64 {
	return t.statements.Load()
}

// ExecContext counts the statement and sends it.
func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	t.statements.Add(1)
	return t.Tx.ExecContext(ctx, query, args...)
}

// PrepareContext counts the statement and prepares it.
func (t *Tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	t.statements.Add(1)
	return t.Tx.PrepareContext(ctx, query)
}

// QueryContext counts the statement and sends it.
func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	t.statements.Add(1)
	return t.Tx.QueryContext(ctx, query, args...)
}

// QueryRowContext counts the statement and sends it.
func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	t.statements.Add(1)
	return t.Tx.QueryRowContext(ctx, query, args...)
}
