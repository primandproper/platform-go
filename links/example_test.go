package links_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	cachememory "github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	"github.com/primandproper/platform-go/v13/links"
)

// newExampleMinter wires a Minter over in-process pieces. A real deployment
// uses the redis cache provider and a redis or postgres locker: both of these
// are per-process, so a link minted by one replica would not exist for the next.
func newExampleMinter() (*links.Minter, error) {
	store, err := cachememory.NewInMemoryCache[links.Record](0)
	if err != nil {
		return nil, err
	}

	raw, err := dlmemory.NewLocker()
	if err != nil {
		return nil, err
	}

	locker, err := distributedlock.NewScopedLocker(raw)
	if err != nil {
		return nil, err
	}

	return links.NewMinter(store, locker,
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
