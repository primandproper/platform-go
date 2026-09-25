package migrations_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// clientConfig is the minimum database.ClientConfig a client needs.
type clientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*clientConfig)(nil)

func (c *clientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *clientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *clientConfig) GetMaxPingAttempts() uint64        { return 30 }
func (c *clientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *clientConfig) GetMaxIdleConns() int              { return 1 }
func (c *clientConfig) GetMaxOpenConns() int              { return 1 }
func (c *clientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// shippedRow is a token as a release before version 3 wrote it: no
// signed_in_at, because that release had no such column.
type shippedRow struct {
	issuedAt time.Time
	secret   string
	scope    string
	family   string
	subject  string
	spent    bool
}

// placeholder is the dialect's spelling of the nth bound parameter, 1-based.
func placeholder(d dialect.Dialect, n int) string {
	if d == dialect.Postgres {
		return fmt.Sprintf("$%d", n)
	}

	return "?"
}

func placeholders(d dialect.Dialect, count int) string {
	out := ""

	for n := 1; n <= count; n++ {
		if n > 1 {
			out += ", "
		}

		out += placeholder(d, n)
	}

	return out
}

func exec(tb testing.TB, client database.Client, stmts []string) {
	tb.Helper()

	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, err := client.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, err, must.Sprintf("executing %s", stmt))
	}
}

// runUpgradeSuite is the claim a versioned schema exists to make: a table
// created by an earlier release, holding that release's rows, becomes this
// release's table by running what StatementsSince owes it — and the store then
// works on it exactly as it does on a fresh install.
//
// Each starting version is a table of its own under a prefix of its own, so one
// server serves both without either reading the other's rows.
func runUpgradeSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	for range 30 {
		if _, err := client.Writer().ExecContext(t.Context(), "SELECT 1"); err == nil {
			break
		}

		time.Sleep(time.Second)
	}

	for _, from := range []uint64{1, 2} {
		t.Run(fmt.Sprintf("from version %d", from), func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			prefix := fmt.Sprintf("from%d", from)
			table := prefix + "_signin_refresh_tokens"

			stmts, err := migrations.StatementsThrough(d, prefix, from)
			must.NoError(t, err)
			exec(t, client, stmts)

			store, err := refreshtokens.NewSQLStore(&refreshtokens.Config{TablePrefix: prefix}, client)
			must.NoError(t, err)

			// Whole seconds, because SQLite stores no finer, and far enough from
			// now that the live rows are still live when the store reads them.
			began := time.Now().UTC().Truncate(time.Second).Add(-3 * time.Hour)

			rows := []shippedRow{
				// One login that has refreshed once: its first token spent, its
				// successor live. Both belong to the login that began at began.
				{secret: "phone-first", scope: "tenant_a", family: "family_phone", subject: "user_a", issuedAt: began, spent: true},
				{secret: "phone-second", scope: "tenant_a", family: "family_phone", subject: "user_a", issuedAt: began.Add(time.Hour)},
				// A second login by the same person, which began later.
				{secret: "laptop-first", scope: "tenant_a", family: "family_laptop", subject: "user_a", issuedAt: began.Add(30 * time.Minute)},
				// The same family identifier in another directory, which is a
				// different login and must not be read as the first's.
				{secret: "elsewhere-first", scope: "tenant_b", family: "family_phone", subject: "user_b", issuedAt: began.Add(2 * time.Hour)},
			}

			insert := fmt.Sprintf(`INSERT INTO %s
				(hash, scope, family_id, subject_id, active_account_id, administrative,
				 issued_at, expires_at, purge_after, redeemed_at)
				VALUES (%s)`, table, placeholders(d, 10))

			expires, purge := time.Now().UTC().Add(24*time.Hour), time.Now().UTC().Add(48*time.Hour)

			for i := range rows {
				row := &rows[i]

				var redeemedAt *time.Time
				if row.spent {
					at := row.issuedAt.Add(time.Hour)
					redeemedAt = &at
				}

				_, err = client.Writer().ExecContext(ctx, insert,
					store.Digest(row.secret), row.scope, row.family, row.subject, "account_01", false,
					row.issuedAt, expires, purge, redeemedAt)
				must.NoError(t, err)
			}

			owed, err := migrations.StatementsSince(d, prefix, from)
			must.NoError(t, err)
			exec(t, client, owed)

			t.Run("stamps every row with its family's earliest issued_at", func(t *testing.T) {
				want := map[string]time.Time{
					"phone-first":     began,
					"phone-second":    began,
					"laptop-first":    began.Add(30 * time.Minute),
					"elsewhere-first": began.Add(2 * time.Hour),
				}

				query := fmt.Sprintf("SELECT signed_in_at FROM %s WHERE hash = %s", table, placeholder(d, 1))

				for secret, at := range want {
					var got time.Time
					must.NoError(t, client.Reader().QueryRowContext(ctx, query, store.Digest(secret)).Scan(&got))
					test.True(t, at.Equal(got), test.Sprintf("%s: want %v, got %v", secret, at, got))
				}
			})

			// The same insert with and without the column's value, so the refusal
			// is known to be the NOT NULL rather than anything else the statement
			// got wrong.
			t.Run("refuses a row without one", func(t *testing.T) {
				insertSigned := fmt.Sprintf(`INSERT INTO %s
					(hash, scope, family_id, subject_id, active_account_id, administrative,
					 issued_at, signed_in_at, expires_at, purge_after)
					VALUES (%s)`, table, placeholders(d, 10))

				_, unsignedErr := client.Writer().ExecContext(ctx, insertSigned,
					"unsigned", "tenant_c", "family_unsigned", "user_c", "account_01", false,
					began, nil, expires, purge)
				test.Error(t, unsignedErr)

				_, signedErr := client.Writer().ExecContext(ctx, insertSigned,
					"signed", "tenant_c", "family_signed", "user_c", "account_01", false,
					began, began, expires, purge)
				test.NoError(t, signedErr)
			})

			// The rebuild renames the old table aside and drops it, and the rename
			// took the three indexes with it: what has to be true afterwards is
			// that they are on the new table and the old name is gone.
			if d == dialect.SQLite {
				t.Run("leaves the rebuilt table indexed and nothing behind it", func(t *testing.T) {
					var indexes int
					must.NoError(t, client.Reader().QueryRowContext(ctx,
						"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name LIKE '%\\_idx' ESCAPE '\\'",
						table).Scan(&indexes))
					test.EqOp(t, 3, indexes)

					var leftovers int
					must.NoError(t, client.Reader().QueryRowContext(ctx,
						"SELECT COUNT(*) FROM sqlite_master WHERE name LIKE ?", table+"\\_rebuild%").Scan(&leftovers))
					test.EqOp(t, 0, leftovers)
				})
			}

			t.Run("lists the logins the backfill dated", func(t *testing.T) {
				signIns, listErr := store.ListActiveSignIns(ctx, client.Reader(), tenancy.Of("tenant_a"), "user_a", 10)
				must.NoError(t, listErr)

				began := map[string]time.Time{}
				for _, s := range signIns {
					began[s.FamilyID] = s.SignedInAt
				}

				must.MapLen(t, 2, began)
				test.True(t, rows[0].issuedAt.Equal(began["family_phone"]),
					test.Sprintf("family_phone listed as beginning %v", began["family_phone"]))
				test.True(t, rows[2].issuedAt.Equal(began["family_laptop"]),
					test.Sprintf("family_laptop listed as beginning %v", began["family_laptop"]))
			})

			// Every column the later versions added is written here: the retry
			// key and successor by the idempotent exchange, and signed_in_at by
			// the successor's mint.
			t.Run("exchanges a token the earlier release minted", func(t *testing.T) {
				scope := tenancy.Of("tenant_a")

				var successor *signin.RefreshTokenIssuance

				must.NoError(t, client.WithTransaction(ctx, func(tx database.Tx) error {
					spent, redeemErr := store.RedeemIdempotently(ctx, tx, scope, "phone-second", "upgrade_key_01")
					if redeemErr != nil {
						return redeemErr
					}

					var issueErr error
					successor, issueErr = store.Issue(ctx, tx, scope, &signin.RefreshTokenRequest{
						TTL:             time.Hour,
						FamilyID:        spent.FamilyID,
						SignedInAt:      spent.SignedInAt,
						SubjectID:       spent.SubjectID,
						ActiveAccountID: spent.ActiveAccountID,
					})
					if issueErr != nil {
						return issueErr
					}

					return store.RecordSuccessor(ctx, tx, scope, "phone-second", successor.Secret)
				}))

				must.NotNil(t, successor)
				test.True(t, began.Equal(successor.Token.SignedInAt),
					test.Sprintf("the successor says its login began %v", successor.Token.SignedInAt))
			})
		})
	}
}

// SQLite is in-process, so its upgrade runs with every `go test` rather than
// only behind the container gate — and it is the dialect whose version 3 does
// the most, rebuilding the table rather than altering it.
func TestUpgrade_SQLite(T *testing.T) {
	T.Parallel()

	client, err := sqlite.NewDatabaseClient(T.Context(),
		&clientConfig{connectionString: filepath.Join(T.TempDir(), "upgrade.db")})
	must.NoError(T, err)
	T.Cleanup(func() { _ = client.Close() })

	runUpgradeSuite(T, client, dialect.SQLite)
}

func TestUpgrade_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &clientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runUpgradeSuite(T, client, dialect.Postgres)
	})
}

func TestUpgrade_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &clientConfig{connectionString: my.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runUpgradeSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("upgradetest", "upgradetest", "upgradetest"))
}
