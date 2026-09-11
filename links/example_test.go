package links_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/links"
	linksdatabase "github.com/primandproper/platform-go/v14/links/database"
	"github.com/primandproper/platform-go/v14/links/database/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
)

// newExampleMinter wires a Minter over a throwaway SQLite database. A real
// application hands migrations.SQL to its own migration run and builds the
// store over the database it already has — links needs no cache and no lock
// service, and the sweeper it would normally start is left off here because
// nothing in an example lives long enough to collect.
func newExampleMinter() (*links.Minter, error) {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "links-example")
	if err != nil {
		return nil, err
	}

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{
		connectionString: filepath.Join(dir, "links.db"),
	})
	if err != nil {
		return nil, err
	}

	stmts, err := migrations.Statements(dialect.SQLite, linksdatabase.DefaultTablePrefix)
	if err != nil {
		return nil, err
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			return nil, err
		}
	}

	store, err := linksdatabase.New(&linksdatabase.Config{}, client)
	if err != nil {
		return nil, err
	}

	return links.NewMinter(store,
		links.WithAction("magic_login", links.ActionPolicy{
			URL: "https://app.example.com/auth/magic/{token}",
			TTL: 15 * time.Minute,
		}),
		links.WithAction("unsubscribe", links.ActionPolicy{
			URL: "https://app.example.com/unsubscribe?t={token}",
			TTL: 365 * 24 * time.Hour,
		}),
	)
}

// Mint a link, deliver its URL, and redeem it once.
func Example() {
	ctx := context.Background()

	minter, err := newExampleMinter()
	if err != nil {
		panic(err)
	}

	link, err := minter.Mint(ctx, "magic_login", "user_123",
		links.WithMetadata(map[string]string{"next": "/dashboard"}))
	if err != nil {
		panic(err)
	}

	// Deliver link.URL by email. Record link.ID in the audit log — never the
	// URL and never the token, both of which are the credential itself.
	_ = link.URL

	claims, err := minter.Redeem(ctx, link.Token)
	if err != nil {
		panic(err)
	}

	fmt.Println("signing in", claims.Subject, "then sending them to", claims.Metadata["next"])

	// The single-use guarantee, reporting itself.
	if _, err = minter.Redeem(ctx, link.Token); errors.Is(err, links.ErrLinkAlreadyRedeemed) {
		fmt.Println("second redemption refused")
	}

	// Output:
	// signing in user_123 then sending them to /dashboard
	// second redemption refused
}

// A password reset spans two requests: the GET that renders the form must not
// consume the link, or a mail scanner's prefetch spends it before the user ever
// sees it.
func Example_twoStepRedemption() {
	ctx := context.Background()

	minter, err := newExampleMinter()
	if err != nil {
		panic(err)
	}

	link, err := minter.Mint(ctx, "magic_login", "user_123")
	if err != nil {
		panic(err)
	}

	// GET /reset/{token} — a mail scanner gets here first, and finds a link it
	// leaves intact.
	if _, err = minter.Inspect(ctx, link.Token); err != nil {
		panic(err)
	}

	fmt.Println("scanner fetched the URL")

	// GET /reset/{token} — the user, rendering the same form from the same
	// still-unspent link.
	claims, err := minter.Inspect(ctx, link.Token)
	if err != nil {
		panic(err)
	}

	fmt.Println("rendering the form for", claims.Subject)

	// POST /reset — the button press, which is where the link is spent.
	if _, err = minter.Redeem(ctx, link.Token); err != nil {
		panic(err)
	}

	fmt.Println("password changed")

	// Output:
	// scanner fetched the URL
	// rendering the form for user_123
	// password changed
}

// Revoke withdraws a link that is still sitting in somebody's mailbox, using
// the ID recorded at mint time rather than the token nobody kept.
func Example_revoking() {
	ctx := context.Background()

	minter, err := newExampleMinter()
	if err != nil {
		panic(err)
	}

	link, err := minter.Mint(ctx, "magic_login", "user_123")
	if err != nil {
		panic(err)
	}

	// Months later, from the audit entry that recorded the mint.
	if err = minter.Revoke(ctx, link.ID); err != nil {
		panic(err)
	}

	_, err = minter.Redeem(ctx, link.Token)
	fmt.Println(errors.Is(err, links.ErrLinkRevoked))

	// Output:
	// true
}

// RevokeForSubject withdraws every link a person still holds, without being
// told which ones exist. A completed password reset, a locked account and an
// erasure all ask this question and none of them knows the IDs.
func Example_revokingEverythingForASubject() {
	ctx := context.Background()

	minter, err := newExampleMinter()
	if err != nil {
		panic(err)
	}

	login, err := minter.Mint(ctx, "magic_login", "user_123")
	if err != nil {
		panic(err)
	}

	unsubscribe, err := minter.Mint(ctx, "unsubscribe", "user_123")
	if err != nil {
		panic(err)
	}

	// One statement, across every action, and it reports what it moved.
	revoked, err := minter.RevokeForSubject(ctx, "user_123")
	if err != nil {
		panic(err)
	}

	fmt.Println("withdrew", revoked)

	_, err = minter.Redeem(ctx, login.Token)
	fmt.Println("login refused:", errors.Is(err, links.ErrLinkRevoked))

	_, err = minter.Redeem(ctx, unsubscribe.Token)
	fmt.Println("unsubscribe refused:", errors.Is(err, links.ErrLinkRevoked))

	// Output:
	// withdrew 2
	// login refused: true
	// unsubscribe refused: true
}

// exampleClientConfig is the minimum database.ClientConfig a SQLite client
// needs.
type exampleClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*exampleClientConfig)(nil)

func (c *exampleClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *exampleClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *exampleClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *exampleClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *exampleClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }
