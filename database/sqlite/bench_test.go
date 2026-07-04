package sqlite

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

// BenchmarkSQLiteClient exercises an in-memory SQLite database. The driver
// (modernc.org/sqlite) is pure Go and needs no container, so this runs as part
// of the default `make bench`.
func BenchmarkSQLiteClient(b *testing.B) {
	ctx := b.Context()
	cfg := &testClientConfig{
		connectionString: ":memory:",
		maxPingAttempts:  1,
	}

	client, err := ProvideDatabaseClient(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), cfg, nil)
	must.NoError(b, err)
	b.Cleanup(func() { _ = client.Close() })

	db := client.WriteDB()
	_, err = db.ExecContext(ctx, "CREATE TABLE bench (id INTEGER PRIMARY KEY, name TEXT)")
	must.NoError(b, err)
	_, err = db.ExecContext(ctx, "INSERT INTO bench (id, name) VALUES (1, 'seed')")
	must.NoError(b, err)

	b.Run("QueryRow", func(b *testing.B) {
		for b.Loop() {
			var name string
			_ = db.QueryRowContext(ctx, "SELECT name FROM bench WHERE id = 1").Scan(&name)
		}
	})

	b.Run("Exec", func(b *testing.B) {
		for b.Loop() {
			_, _ = db.ExecContext(ctx, "UPDATE bench SET name = 'x' WHERE id = 1")
		}
	})
}
