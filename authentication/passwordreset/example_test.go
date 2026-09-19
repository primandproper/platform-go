package passwordreset_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The whole flow, in one transaction: issue a token, mail the secret, then spend
// it, write the password and revoke what is left over — together.
//
// The password write goes between Consume and RevokeForUser and inside the same
// callback, which is the point. Two transactions would leave either a spent
// token over an unchanged password, which costs the user another email, or a
// changed password with a live reset link still outstanding, which is a
// vulnerability. One commit has neither failure.
func Example() {
	ctx := context.Background()

	client, cleanup := exampleClient(ctx)
	defer cleanup()

	store, err := passwordreset.NewSQLStore(&passwordreset.Config{}, client)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Global()

	// Somebody asked for a reset. Say the same thing whether or not the address
	// belonged to a user — the response that differs is an account enumeration
	// oracle built out of a feature meant to protect accounts.
	//
	// Nothing else belongs in this transaction, so it holds only the issuance.
	var issuance *passwordreset.Issuance
	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		var issueErr error
		issuance, issueErr = store.Issue(ctx, tx, scope, "user_01", time.Hour)

		return issueErr
	}); err != nil {
		panic(err)
	}

	// The secret exists here and nowhere else. It goes into the link and is
	// forgotten; the store holds a digest it cannot reverse. Mail it after the
	// commit — a link sent for a transaction that rolled back is a reset nobody
	// can complete.
	fmt.Println("mailing a link carrying", len(issuance.Secret), "characters")

	// They followed the link. Verify spends nothing, so reloading the page —
	// or thinking better of it and coming back later — leaves the token usable.
	// It takes an executor rather than a transaction: this one holds nothing to
	// join, and passes the write pool rather than a replica, which would answer
	// "not found" for a link that arrived seconds ago.
	if _, err = store.Verify(ctx, client.Writer(), scope, issuance.Secret); err != nil {
		panic(err)
	}

	// They submitted the form. Everything the submit does is one transaction.
	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		// Consume is the decision: exactly one caller leaves this line holding
		// the right to change that password.
		token, consumeErr := store.Consume(ctx, tx, scope, issuance.Secret)
		if consumeErr != nil {
			return consumeErr
		}

		fmt.Println("resetting the password for", token.UserID)

		// ... write the new password hash here, through whatever store owns
		// users, on this same tx.

		// Everything else they had outstanding stops working, and stops working
		// at the instant the password changes rather than shortly after it.
		revoked, revokeErr := store.RevokeForUser(ctx, tx, scope, token.UserID)
		if revokeErr != nil {
			return revokeErr
		}

		fmt.Println("revoked", revoked, "other outstanding tokens")

		return nil
	}); err != nil {
		panic(err)
	}

	// The link cannot be used twice, and the store says so rather than the
	// caller having to remember.
	err = client.WithTransaction(ctx, func(tx database.Tx) error {
		_, consumeErr := store.Consume(ctx, tx, scope, issuance.Secret)

		return consumeErr
	})
	fmt.Println("second attempt:", errors.Is(err, passwordreset.ErrTokenRedeemed))

	// Output:
	// mailing a link carrying 43 characters
	// resetting the password for user_01
	// revoked 0 other outstanding tokens
	// second attempt: true
}

// exampleClient stands up a throwaway SQLite database with the token table
// created from the DDL this package ships.
func exampleClient(ctx context.Context) (client database.Client, cleanup func()) {
	dir, err := os.MkdirTemp("", "passwordreset-example")
	if err != nil {
		panic(err)
	}

	client, err = sqlite.NewDatabaseClient(ctx,
		&exampleConfig{path: filepath.Join(dir, "reset.db")})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, passwordreset.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			panic(err)
		}
	}

	return client, func() {
		_ = client.Close()
		_ = os.RemoveAll(dir)
	}
}

type exampleConfig struct {
	path string
}

var _ database.ClientConfig = (*exampleConfig)(nil)

func (c *exampleConfig) GetReadConnectionString() string   { return c.path }
func (c *exampleConfig) GetWriteConnectionString() string  { return c.path }
func (c *exampleConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *exampleConfig) GetMaxIdleConns() int              { return 1 }
func (c *exampleConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// The same flow through [passwordreset.Service], which owns the order and the
// transaction so a consumer does not.
//
// What changes is not the number of lines. It is that the three writes the
// submit performs cannot come apart: Consume, the password write and
// RevokeForUser commit together, so there is no ordering left for a caller to
// get wrong. The mail is sequenced after the commit for the same reason, and an
// address nobody holds is answered exactly as one somebody does.
func ExampleService() {
	ctx := context.Background()

	client, cleanup := exampleClient(ctx)
	defer cleanup()

	store, err := passwordreset.NewSQLStore(&passwordreset.Config{}, client)
	if err != nil {
		panic(err)
	}

	// secret is what the example's mailer keeps, so the second half of the flow
	// can spend the link the first half sent. A real one puts it in a URL and
	// forgets it.
	var secret string

	// The mailer is the seam for the one message this flow sends. It is handed
	// the secret, which exists here and nowhere else, and renders whatever link
	// the application's front end serves.
	mailer := passwordreset.MailerFunc(func(_ context.Context, mail *passwordreset.Mail) error {
		secret = mail.Issuance.Secret

		fmt.Printf("mailing %s a link carrying %d characters\n",
			mail.User.EmailAddress, len(mail.Issuance.Secret))

		return nil
	})

	service, err := passwordreset.NewService(
		client,
		store,
		&exampleDirectory{users: map[string]*identity.User{
			"someone@example.com": {ID: "user_01", EmailAddress: "someone@example.com"},
		}},
		// In a deployment this is argon2.NewArgon2Authenticator(); the example
		// uses something instant so the output is not a second of hashing.
		exampleAuthenticator{},
		mailer,
		// Left at DefaultRequestFloor in production. Zero here because an
		// example that padded half a second would be an example nobody runs.
		passwordreset.WithRequestFloor(0),
	)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Global()

	// Somebody asked for a reset. This mints the token, commits it, and mails
	// the secret afterwards.
	if err = service.Request(ctx, scope, "someone@example.com"); err != nil {
		panic(err)
	}

	// And somebody typed an address nobody holds. Same answer, same delay, and
	// no mail — which is the whole of the account enumeration position.
	if err = service.Request(ctx, scope, "nobody@example.com"); err != nil {
		panic(err)
	}

	// They followed the link. Verify spends nothing, so reloading the page
	// leaves the token usable.
	if _, err = service.Verify(ctx, scope, secret); err != nil {
		panic(err)
	}

	// They submitted the form. One transaction: the redemption, the password
	// write, and the revocation of every other link they were holding.
	token, err := service.Complete(ctx, scope, secret, "correct horse battery staple")
	if err != nil {
		panic(err)
	}

	fmt.Println("reset the password for", token.UserID)

	// The link cannot be used twice, and the flow says so rather than the
	// caller having to remember.
	_, err = service.Complete(ctx, scope, secret, "second attempt")
	fmt.Println("second attempt:", errors.Is(err, passwordreset.ErrTokenRedeemed))

	// Output:
	// mailing someone@example.com a link carrying 43 characters
	// reset the password for user_01
	// second attempt: true
}

// exampleDirectory stands in for identity.Store, which satisfies
// passwordreset.Directory as it is.
//
// A real one writes the password on the database.Tx it is handed, which is what
// puts it in the same transaction as the redemption. This one writes to a map,
// because an example is not the place to stand up a user table.
type exampleDirectory struct {
	users map[string]*identity.User
}

func (d *exampleDirectory) GetUserByEmailAddress(
	_ context.Context,
	_ database.SQLQueryExecutor,
	_ tenancy.Scope,
	emailAddress string,
) (*identity.User, error) {
	user, ok := d.users[emailAddress]
	if !ok {
		return nil, identity.ErrUserNotFound
	}

	return user, nil
}

func (d *exampleDirectory) UpdateUserPassword(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	userID, hashedPassword string,
) error {
	for _, user := range d.users {
		if user.ID == userID {
			user.HashedPassword = hashedPassword
		}
	}

	return nil
}

// exampleAuthenticator hashes by prefixing. Use argon2 for real.
type exampleAuthenticator struct{}

func (exampleAuthenticator) HashPassword(_ context.Context, password string) (string, error) {
	return "hashed:" + password, nil
}

func (exampleAuthenticator) PasswordMatches(_ context.Context, hash, password string) (bool, error) {
	return hash == "hashed:"+password, nil
}
