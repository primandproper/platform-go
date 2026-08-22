package sessions_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionscache "github.com/primandproper/platform-go/v13/sessions/cache"
)

// Principal is what a session carries: whatever the application needs to know
// about who is making the request.
type Principal struct {
	UserID string
	Admin  bool
}

// newStore builds a store over an in-memory cache, which is what a test wants.
// Production points cachecfg at redis instead.
func newStore(opts ...sessions.Option) sessions.Store[Principal] {
	c, err := memory.NewInMemoryCache[sessions.Record[Principal]](time.Hour)
	if err != nil {
		panic(err)
	}

	backend, err := sessionscache.NewBackend(c)
	if err != nil {
		panic(err)
	}

	store, err := sessions.NewStore(backend, opts...)
	if err != nil {
		panic(err)
	}

	return store
}

// The ordinary shape: establish a session after authenticating, read it back on
// the next request, end it on sign-out.
func Example() {
	ctx := context.Background()
	store := newStore()

	session, err := store.New(ctx, &Principal{UserID: "u_123"})
	if err != nil {
		panic(err)
	}

	// session.ID is what the client gets — nothing else leaves the server.
	read, err := store.Get(ctx, session.ID)
	if err != nil {
		panic(err)
	}

	fmt.Println("user:", read.Data.UserID)

	if err = store.Delete(ctx, session.ID); err != nil {
		panic(err)
	}

	_, err = store.Get(ctx, session.ID)
	fmt.Println("after sign-out:", stderrors.Is(err, sessions.ErrNotFound))

	// Output:
	// user: u_123
	// after sign-out: true
}

// Renew rotates the identifier on a privilege change. Anything a client held
// before the change stops working, which is what defeats session fixation.
func ExampleStore_Renew() {
	ctx := context.Background()
	store := newStore()

	session, err := store.New(ctx, &Principal{UserID: "u_123"})
	if err != nil {
		panic(err)
	}

	// The user just authenticated. Rotate before granting anything.
	newID, err := store.Renew(ctx, session.ID)
	if err != nil {
		// Assume the old identifier still works, and refuse the privilege
		// change rather than completing it.
		panic(err)
	}

	if err = store.Save(ctx, newID, &Principal{UserID: "u_123", Admin: true}); err != nil {
		panic(err)
	}

	_, err = store.Get(ctx, session.ID)
	fmt.Println("old identifier still works:", err == nil)

	read, err := store.Get(ctx, newID)
	if err != nil {
		panic(err)
	}

	fmt.Println("admin:", read.Data.Admin)
	fmt.Println("session start preserved:", read.CreatedAt.Equal(session.CreatedAt))

	// Output:
	// old identifier still works: false
	// admin: true
	// session start preserved: true
}

// The two timeouts answer different questions, and a caller can tell which one
// ended a session.
func ExampleStore_Get_expiry() {
	ctx := context.Background()
	store := newStore(
		sessions.WithAbsoluteTimeout(24*time.Hour),
		sessions.WithIdleTimeout(30*time.Minute),
	)

	session, err := store.New(ctx, &Principal{UserID: "u_123"})
	if err != nil {
		panic(err)
	}

	// ExpiresAt is the earlier of the two deadlines: the instant this session
	// stops being usable if nothing touches it again.
	fmt.Println("expires after:", session.ExpiresAt.Sub(session.CreatedAt))

	_, err = store.Get(ctx, session.ID)
	fmt.Println("still live:", err == nil)

	// Output:
	// expires after: 30m0s
	// still live: true
}
