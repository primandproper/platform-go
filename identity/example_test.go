package identity_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Example_registration shows the flow this package exists for, written against
// the store: a user, an account, and a membership, in one transaction.
//
// This is the shape [identity.Service.Register] performs, and it is spelled out
// here because a registration is not always these three writes — one by
// invitation joins somebody else's account instead of creating one, and one
// behind an SSO provider may create neither. What the store guarantees either
// way is that whichever writes a flow makes commit together, so a user without
// an account or an account without an owner is never left behind. See
// Example_service for the same registration through the service layer.
func Example_registration() {
	ctx := context.Background()
	client, store := exampleWiring()

	// Everything an application runs its own way stays its own: hashing,
	// generating the second-factor secret, minting the verification token.
	hashedPassword := "argon2id$v=19$m=65536,t=3,p=2$..."
	verificationToken := identifiers.New()

	// One directory, so the scope is global — the shape a single-tenant
	// application has, behaving exactly as it would without the column.
	scope := tenancy.Global()

	now := time.Now().UTC()

	user := &identity.User{
		Scope:                         scope,
		Username:                      "ada",
		EmailAddress:                  "ada@example.com",
		FirstName:                     "Ada",
		HashedPassword:                hashedPassword,
		TwoFactorSecret:               "JBSWY3DPEHPK3PXP",
		EmailAddressVerificationToken: verificationToken,
		AccountStatus:                 identity.StatusUnverified,
		ServiceRoles:                  []string{"service_user"},

		// Acceptance is set on the value rather than through RecordAgreement,
		// so it commits with the row it belongs to.
		LastAcceptedTermsOfService: &now,
		LastAcceptedPrivacyPolicy:  &now,
	}

	account := &identity.Account{
		Scope: scope,
		Name:  "Ada's account",
	}

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		// CreateUser fills in the ID and CreatedAt, so the account below can
		// name its owner.
		if err := store.CreateUser(ctx, tx, scope, user); err != nil {
			return err
		}

		account.OwnerUserID = user.ID

		if err := store.CreateAccount(ctx, tx, scope, account); err != nil {
			return err
		}

		// The first membership becomes the default whatever this says, because
		// a user with memberships and no default has nowhere to land.
		return store.CreateMembership(ctx, tx, scope, &identity.Membership{
			BelongsToUser:    user.ID,
			BelongsToAccount: account.ID,
			Roles:            []string{"account_admin"},
		})
	})

	switch {
	case errors.Is(err, identity.ErrUsernameTaken):
		fmt.Println("that username is taken")
	case errors.Is(err, identity.ErrEmailAddressTaken):
		fmt.Println("that email address is already registered")
	case err != nil:
		fmt.Println("registration failed:", err)
	default:
		fmt.Println("registered", user.Username)
	}

	// A second registration on the same handle is refused with an error the
	// caller can act on, rather than a driver's constraint violation they would
	// have to parse a SQLSTATE out of.
	err = client.WithTransaction(ctx, func(tx database.Tx) error {
		return store.CreateUser(ctx, tx, scope, &identity.User{
			Username:       "ada",
			EmailAddress:   "someone-else@example.com",
			HashedPassword: hashedPassword,
		})
	})
	fmt.Println(errors.Is(err, identity.ErrUsernameTaken))

	// Output:
	// registered ada
	// true
}

// auditHooks is what a consumer hangs off the service's operations: a write of
// its own, on the transaction the operation is running in.
//
// Embedding NoopHooks is what lets it implement one method out of ten — and
// what keeps an operation added to identity.Hooks later from breaking it.
type auditHooks struct {
	identity.NoopHooks
}

func (h *auditHooks) AfterRegister(
	_ context.Context, _ database.Tx, _ tenancy.Scope, registration *identity.Registration,
) error {
	// A real one writes its audit entry, data change event or outbox row on the
	// tx it was handed, so that row and the registration commit together. What
	// does not belong here is the welcome email: a hook runs with a write
	// transaction open, and a mail provider being down would fail the
	// registration.
	fmt.Println("recorded the registration of", registration.User.Username,
		"into", registration.Account.Name)

	return nil
}

// Example_service shows the layer above the store: the operations that are more
// than one write, each run as one transaction, with the consumer's own writes
// joining through a hook.
func Example_service() {
	ctx := context.Background()
	client, store := exampleWiring()

	scope := tenancy.Global()

	service, err := identity.NewService(client, store, identity.WithHooks(&auditHooks{}))
	if err != nil {
		panic(err)
	}

	// Policy stays the caller's: this flow decided that a password is required
	// and hashed it, and that the first account is named after its owner.
	registration, err := service.Register(ctx, scope,
		&identity.User{
			Username:       "ada",
			EmailAddress:   "ada@example.com",
			HashedPassword: "argon2id$v=19$m=65536,t=3,p=2$...",
			AccountStatus:  identity.StatusUnverified,
		},
		&identity.Account{Name: "Ada's account"},
		[]string{"account_admin"},
	)
	if err != nil {
		panic(err)
	}

	// The user, the account and the owner membership, all committed, and the
	// membership is where Ada lands because it is the only one she holds.
	fmt.Println(registration.User.Username, "owns", registration.Account.Name)
	fmt.Println("default account:", registration.Membership.DefaultAccount)

	// A hook that returns an error rolls its operation back. Nothing here is a
	// veto on policy — whether Ada may be banned at all was decided before the
	// call — but the record of the ban commits with the ban or neither does.
	banned, err := service.UpdateUserAccountStatus(ctx, scope,
		registration.User.ID, identity.StatusBanned, "spam")
	if err != nil {
		panic(err)
	}

	fmt.Println("status:", banned.AccountStatus)

	// Output:
	// recorded the registration of ada into Ada's account
	// ada owns Ada's account
	// default account: true
	// status: banned
}

// Example_authenticatedRequest shows the read every authenticated request makes.
//
// GetPrincipal is one call and, more to the point, one place where the active
// account is checked against the user's memberships. A hand-built equivalent is
// the same reads and an easily-omitted check, and omitting it hands one
// account's data to another account's member.
func Example_authenticatedRequest() {
	ctx := context.Background()
	client, store := exampleWiring()

	scope := tenancy.Global()
	user, account := exampleRegister(ctx, client, store, "ada", "Ada's account")

	// Both come off the session: who is calling, and which account they are
	// looking at. An empty account means their default.
	principal, err := store.GetPrincipal(ctx, client.Reader(), scope, user.ID, "")
	if err != nil {
		fmt.Println("cannot serve this request:", err)

		return
	}

	fmt.Println(principal.ActiveAccountID == account.ID)

	// Roles is the union of the user's service roles and the roles they hold in
	// the active account — what an authorization.PolicyResolver expands into
	// permissions. Passing only half is how an operator's support access stops
	// working inside a customer's account.
	fmt.Println(principal.Roles())

	// A Principal never claims an account its user is not a member of.
	stranger, _ := exampleRegister(ctx, client, store, "mallory", "Mallory's account")

	_, err = store.GetPrincipal(ctx, client.Reader(), scope, stranger.ID, account.ID)
	fmt.Println(errors.Is(err, identity.ErrMembershipNotFound))

	// Output:
	// true
	// [service_user account_admin]
	// true
}

// Example_signIn shows what the authentication engines are handed.
//
// This package stores credentials and never evaluates them: it produces the
// hash for argon2 to compare and the secret for totp to validate, and does
// neither itself.
func Example_signIn() {
	ctx := context.Background()
	client, store := exampleWiring()

	scope := tenancy.Global()
	exampleRegister(ctx, client, store, "ada", "Ada's account")

	user, err := store.GetUserByUsername(ctx, client.Reader(), scope, "ada")
	if err != nil {
		// Archived users are excluded from this read, so a deleted account
		// cannot authenticate.
		fmt.Println("no such user")

		return
	}

	// Status first: a banned user's password is still correct.
	fmt.Println(user.AccountStatus.AdmitsSignIn())

	// A secret that has been issued and never proven is not a second factor, so
	// this is the check rather than whether the secret is set.
	fmt.Println(user.TwoFactorEnabled())

	// The verification is a write, so it runs in a transaction — the one a
	// consumer would already be holding for the audit entry beside it.
	if err = client.WithTransaction(ctx, func(tx database.Tx) error {
		return store.MarkUserTwoFactorSecretVerified(ctx, tx, scope, user.ID)
	}); err != nil {
		fmt.Println(err)

		return
	}

	proven, err := store.GetUserByUsername(ctx, client.Reader(), scope, "ada")
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(proven.TwoFactorEnabled())

	// Then whether there is a password to compare at all: a passkey-only user
	// has none, and an engine handed an empty hash has no way to know it was
	// never set. Only then argon2.Compare(proven.HashedPassword, submitted),
	// which is the engine's job and not this package's.
	fmt.Println(proven.HasPassword())

	// Output:
	// true
	// false
	// true
	// true
}

// Example_invitation shows an invitation being accepted alongside a
// registration, in one transaction.
//
// The two are one call because an accepted invitation without a membership is a
// user who was told they joined and did not — and the roles come off the
// invitation rather than from a parameter, because what somebody was invited to
// is what they get.
func Example_invitation() {
	ctx := context.Background()
	client, store := exampleWiring()

	scope := tenancy.Global()
	owner, account := exampleRegister(ctx, client, store, "ada", "Acme")

	invitation := &identity.Invitation{
		Scope:            scope,
		BelongsToAccount: account.ID,
		FromUser:         owner.ID,
		ToEmail:          "grace@example.com",
		ToName:           "Grace",
		Token:            identifiers.New(),
		// Required. An invitation link is a bearer credential for joining
		// somebody else's account, and one that never expires is still valid in
		// a mailbox somebody lost control of two years ago.
		ExpiresAt: time.Now().Add(72 * time.Hour),
		Roles:     []string{"account_member"},
	}

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		return store.CreateInvitation(ctx, tx, scope, invitation)
	}); err != nil {
		fmt.Println(err)

		return
	}

	newcomer := &identity.User{
		Scope:          scope,
		Username:       "grace",
		EmailAddress:   "grace@example.com",
		HashedPassword: "argon2id$v=19$...",
	}

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		if createErr := store.CreateUser(ctx, tx, scope, newcomer); createErr != nil {
			return createErr
		}

		// Both halves of the link. Naming the row and then comparing the token
		// keeps the secret out of an index.
		membership, acceptErr := store.AcceptInvitation(
			ctx, tx, scope, invitation.ID, invitation.Token, newcomer.ID, "")
		if acceptErr != nil {
			return acceptErr
		}

		fmt.Println(membership.BelongsToAccount == account.ID, membership.Roles)

		return nil
	})

	switch {
	case errors.Is(err, identity.ErrInvitationExpired):
		fmt.Println("that invitation has expired — ask for another")
	case errors.Is(err, identity.ErrInvitationNotFound):
		fmt.Println("that invitation link is not valid")
	case err != nil:
		fmt.Println("could not accept:", err)
	}

	// Clicking the link twice produces one membership: the second finds nothing
	// pending.
	err = client.WithTransaction(ctx, func(tx database.Tx) error {
		_, acceptErr := store.AcceptInvitation(
			ctx, tx, scope, invitation.ID, invitation.Token, newcomer.ID, "")

		return acceptErr
	})
	fmt.Println(errors.Is(err, identity.ErrInvitationNotFound))

	// Output:
	// true [account_member]
	// true
}

// Example_migrations shows the tables being created by the consumer's own
// migration run, rather than by DDL copied into their repository.
func Example_migrations() {
	// The version number is the consumer's, because migration files are
	// numbered globally per consumer and a platform-owned number would collide
	// with theirs the moment either side added one:
	//
	//	migrate.New(dialect.Postgres, myMigrations,
	//	    migrate.WithGeneratedMigration(44, "create_identity_tables", ddl))
	ddl, err := migrations.SQL(dialect.Postgres, identity.DefaultTablePrefix)
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(ddl != "")

	// A namespace, for a database shared between applications. It must match
	// what the store is built with — nothing can check that, and a mismatch
	// surfaces as a missing table on the first query.
	prefixed, err := migrations.SQL(dialect.Postgres, "ddb")
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(prefixed != ddl)

	// Output:
	// true
	// true
}

// exampleRegister is the registration above, as a helper the other examples
// start from.
func exampleRegister(
	ctx context.Context,
	client database.Client,
	store identity.Store,
	username, accountName string,
) (*identity.User, *identity.Account) {
	scope := tenancy.Global()

	// None of the three names a scope of its own: an entity that names none
	// adopts the argument, which is what a single-directory deployment writes.
	user := &identity.User{
		Username:        username,
		EmailAddress:    username + "@example.com",
		HashedPassword:  "argon2id$v=19$...",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP",
		AccountStatus:   identity.StatusGood,
		ServiceRoles:    []string{"service_user"},
	}

	account := &identity.Account{Name: accountName}

	if err := client.WithTransaction(ctx, func(tx database.Tx) error {
		if err := store.CreateUser(ctx, tx, scope, user); err != nil {
			return err
		}

		account.OwnerUserID = user.ID

		if err := store.CreateAccount(ctx, tx, scope, account); err != nil {
			return err
		}

		return store.CreateMembership(ctx, tx, scope, &identity.Membership{
			BelongsToUser:    user.ID,
			BelongsToAccount: account.ID,
			Roles:            []string{"account_admin"},
		})
	}); err != nil {
		panic(err)
	}

	return user, account
}

// exampleWiring stands in for a DI container: a SQLite database with the
// identity tables in it, and a Store over them.
func exampleWiring() (database.Client, identity.Store) {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "identity-example")
	if err != nil {
		panic(err)
	}

	client, err := sqlite.NewDatabaseClient(ctx, &exampleClientConfig{
		connectionString: filepath.Join(dir, "identity.db"),
	})
	if err != nil {
		panic(err)
	}

	stmts, err := migrations.Statements(dialect.SQLite, identity.DefaultTablePrefix)
	if err != nil {
		panic(err)
	}

	for _, stmt := range stmts {
		if _, err = client.Writer().ExecContext(ctx, stmt); err != nil {
			panic(err)
		}
	}

	store, err := identity.NewSQLStore(client)
	if err != nil {
		panic(err)
	}

	return client, store
}

type exampleClientConfig struct {
	connectionString string
}

func (c *exampleClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *exampleClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *exampleClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *exampleClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *exampleClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *exampleClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *exampleClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }
