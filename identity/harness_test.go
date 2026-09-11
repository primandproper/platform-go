package identity

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	clockmock "github.com/primandproper/primitives-go/v2/clock/mock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// The directories the suite registers users in. testScope is what a
// multi-directory consumer passes; otherScope is the neighbor whose rows must
// never appear in testScope's answers.
var (
	testScope  = tenancy.Of("dir_1")
	otherScope = tenancy.Of("dir_2")
)

// prefixCounter names a fresh set of tables per subtest. Subtests share one
// database and must not share tables — a directory read is global to the users
// table within a scope, so one test's users would be another's.
var prefixCounter atomic.Uint64

// storeEnv is one live database plus the dialect to emit SQL for.
type storeEnv struct {
	client  database.Client
	dialect dialect.Dialect
}

// newStore migrates a uniquely prefixed set of identity tables and returns a
// Store over them.
func (e *storeEnv) newStore(t *testing.T) *SQLStore {
	t.Helper()

	store, err := NewSQLStore(e.client, WithTablePrefix(e.migrate(t)))
	must.NoError(t, err)

	return store
}

// migrate renders a uniquely prefixed set of identity tables and returns the
// prefix.
func (e *storeEnv) migrate(t *testing.T) string {
	t.Helper()

	prefix := fmt.Sprintf("id_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(e.dialect, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return prefix
}

// newSQLiteEnv builds a SQLite-backed environment. SQLite exercises the real
// SQL — placeholder rendering, the upsert's conflict target, the roster join,
// the LIKE escape — without a container.
func newSQLiteEnv(t *testing.T) *storeEnv {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "identity.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return &storeEnv{client: client, dialect: dialect.SQLite}
}

// fixedClock is a clock the suite can step, for the tests that need a
// registration and an expiry to be a known distance apart.
//
// Built on the generated mock so the methods nothing here calls fail loudly
// instead of lying. This store reads Now and nothing else; a stub returning
// zero from Sleep or a real ticker from NewTicker would quietly absorb a future
// change that started using them.
type fixedClock struct {
	*clockmock.ClockMock

	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*fixedClock)(nil)

func newFixedClock(at time.Time) *fixedClock {
	c := &fixedClock{now: at}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:   c.read,
		SinceFunc: func(t time.Time) time.Duration { return c.read().Sub(t) },
	}

	return c
}

func (c *fixedClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// baseTime is where the fixed clock starts. A round UTC instant, so a failure
// message reads.
var baseTime = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

// newUser builds a valid User in testScope, with everything the store requires
// and nothing it does not.
func newUser(username string) *User {
	return &User{
		ID:             identifiers.New(),
		Scope:          testScope,
		Username:       username,
		EmailAddress:   username + "@example.com",
		FirstName:      "Ada",
		LastName:       "Lovelace",
		HashedPassword: "argon2$" + username,
		AccountStatus:  StatusGood,
	}
}

// newAccount builds a valid Account in testScope owned by ownerID.
func newAccount(name, ownerID string) *Account {
	return &Account{
		ID:            identifiers.New(),
		Scope:         testScope,
		Name:          name,
		OwnerUserID:   ownerID,
		BillingStatus: BillingUnpaid,
	}
}

// inTx runs fn inside a transaction on the environment's database and reports
// what fn returned.
//
// Every write in this store takes the caller's transaction, so a test that wants
// a row written opens one — which is what a consumer does. It hands back fn's
// error rather than asserting on it, because a refused write is what a good
// number of these cases are about and RunInTransaction returns the callback's
// error unwrapped.
func (e *storeEnv) inTx(t *testing.T, fn func(tx database.Tx) error) error {
	t.Helper()

	return e.client.WithTransaction(t.Context(), fn)
}

// reader is the executor an ordinary read runs on: the client's, outside any
// transaction. The cases about a read that joins a transaction pass the Tx
// instead, and they are in the caller-transaction suite.
func (e *storeEnv) reader() database.SQLQueryExecutor { return e.client.Reader() }

// The thirty writes, each in a transaction of its own, reporting what the write
// returned.
//
// The transaction is a detail in most of these cases rather than the subject: a
// consumer with nothing to commit alongside opens exactly this. What an identity
// row commits *with* is the caller-transaction suite.

// The ten writes that answer with a row each have two helpers: one handing the
// row back, and an Err sibling for the cases whose subject is the refusal, so a
// refused write still reads as one expression.

func (e *storeEnv) createUser(t *testing.T, store *SQLStore, scope tenancy.Scope, user *User) (*User, error) {
	t.Helper()

	var created *User

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		created, txErr = store.CreateUser(t.Context(), tx, scope, user)

		return txErr
	})

	return created, err
}

func (e *storeEnv) createUserErr(t *testing.T, store *SQLStore, scope tenancy.Scope, user *User) error {
	t.Helper()

	_, err := e.createUser(t, store, scope, user)

	return err
}

func (e *storeEnv) createAccount(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	account *Account,
) (*Account, error) {
	t.Helper()

	var created *Account

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		created, txErr = store.CreateAccount(t.Context(), tx, scope, account)

		return txErr
	})

	return created, err
}

func (e *storeEnv) createAccountErr(t *testing.T, store *SQLStore, scope tenancy.Scope, account *Account) error {
	t.Helper()

	_, err := e.createAccount(t, store, scope, account)

	return err
}

func (e *storeEnv) createMembership(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	membership *Membership,
) (*Membership, error) {
	t.Helper()

	var created *Membership

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		created, txErr = store.CreateMembership(t.Context(), tx, scope, membership)

		return txErr
	})

	return created, err
}

func (e *storeEnv) createMembershipErr(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	membership *Membership,
) error {
	t.Helper()

	_, err := e.createMembership(t, store, scope, membership)

	return err
}

func (e *storeEnv) updateUserPassword(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID, hashedPassword string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.UpdateUserPassword(t.Context(), tx, scope, userID, hashedPassword)
	})
}

func (e *storeEnv) setUserRequiresPasswordChange(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
	requires bool,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.SetUserRequiresPasswordChange(t.Context(), tx, scope, userID, requires)
	})
}

func (e *storeEnv) updateUserTwoFactorSecret(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID, secret string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.UpdateUserTwoFactorSecret(t.Context(), tx, scope, userID, secret)
	})
}

func (e *storeEnv) markUserTwoFactorSecretVerified(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
) (*User, error) {
	t.Helper()

	var verified *User

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		verified, txErr = store.MarkUserTwoFactorSecretVerified(t.Context(), tx, scope, userID)

		return txErr
	})

	return verified, err
}

func (e *storeEnv) markUserTwoFactorSecretVerifiedErr(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
) error {
	t.Helper()

	_, err := e.markUserTwoFactorSecretVerified(t, store, scope, userID)

	return err
}

func (e *storeEnv) setUserEmailAddressVerificationToken(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID, token string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.SetUserEmailAddressVerificationToken(t.Context(), tx, scope, userID, token)
	})
}

func (e *storeEnv) markUserEmailAddressVerified(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID, token string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.MarkUserEmailAddressVerified(t.Context(), tx, scope, userID, token)
	})
}

func (e *storeEnv) markUserEmailAddressUnverified(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
) (*User, error) {
	t.Helper()

	var unverified *User

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		unverified, txErr = store.MarkUserEmailAddressUnverified(t.Context(), tx, scope, userID)

		return txErr
	})

	return unverified, err
}

func (e *storeEnv) markUserEmailAddressUnverifiedErr(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
) error {
	t.Helper()

	_, err := e.markUserEmailAddressUnverified(t, store, scope, userID)

	return err
}

func (e *storeEnv) updateUser(t *testing.T, store *SQLStore, scope tenancy.Scope, user *User) (*User, error) {
	t.Helper()

	var updated *User

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		updated, txErr = store.UpdateUser(t.Context(), tx, scope, user)

		return txErr
	})

	return updated, err
}

func (e *storeEnv) updateUserErr(t *testing.T, store *SQLStore, scope tenancy.Scope, user *User) error {
	t.Helper()

	_, err := e.updateUser(t, store, scope, user)

	return err
}

func (e *storeEnv) updateAccount(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	account *Account,
) (*Account, error) {
	t.Helper()

	var updated *Account

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		updated, txErr = store.UpdateAccount(t.Context(), tx, scope, account)

		return txErr
	})

	return updated, err
}

func (e *storeEnv) updateAccountErr(t *testing.T, store *SQLStore, scope tenancy.Scope, account *Account) error {
	t.Helper()

	_, err := e.updateAccount(t, store, scope, account)

	return err
}

func (e *storeEnv) recordAgreement(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
	agreements ...Agreement,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.RecordAgreement(t.Context(), tx, scope, userID, agreements...)
	})
}

func (e *storeEnv) setMembershipRoles(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID, accountID string,
	roles []string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.SetMembershipRoles(t.Context(), tx, scope, userID, accountID, roles)
	})
}

func (e *storeEnv) setDefaultAccount(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID, accountID string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.SetDefaultAccount(t.Context(), tx, scope, userID, accountID)
	})
}

func (e *storeEnv) transferAccountOwnership(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	accountID, newOwnerUserID string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.TransferAccountOwnership(t.Context(), tx, scope, accountID, newOwnerUserID)
	})
}

func (e *storeEnv) removeMembership(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID, accountID string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.RemoveMembership(t.Context(), tx, scope, userID, accountID)
	})
}

func (e *storeEnv) updateUserAccountStatus(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
	status AccountStatus,
	explanation string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.UpdateUserAccountStatus(t.Context(), tx, scope, userID, status, explanation)
	})
}

func (e *storeEnv) setUserServiceRoles(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	userID string,
	roles []string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.SetUserServiceRoles(t.Context(), tx, scope, userID, roles)
	})
}

func (e *storeEnv) archiveUser(t *testing.T, store *SQLStore, scope tenancy.Scope, userID string) (*User, error) {
	t.Helper()

	var archived *User

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		archived, txErr = store.ArchiveUser(t.Context(), tx, scope, userID)

		return txErr
	})

	return archived, err
}

func (e *storeEnv) archiveUserErr(t *testing.T, store *SQLStore, scope tenancy.Scope, userID string) error {
	t.Helper()

	_, err := e.archiveUser(t, store, scope, userID)

	return err
}

func (e *storeEnv) eraseUser(t *testing.T, store *SQLStore, scope tenancy.Scope, userID string) (int64, error) {
	t.Helper()

	var erased int64

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		erased, txErr = store.EraseUser(t.Context(), tx, scope, userID)

		return txErr
	})

	return erased, err
}

func (e *storeEnv) archiveAccount(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	accountID string,
) (*Account, error) {
	t.Helper()

	var archived *Account

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		archived, txErr = store.ArchiveAccount(t.Context(), tx, scope, accountID)

		return txErr
	})

	return archived, err
}

func (e *storeEnv) archiveAccountErr(t *testing.T, store *SQLStore, scope tenancy.Scope, accountID string) error {
	t.Helper()

	_, err := e.archiveAccount(t, store, scope, accountID)

	return err
}

func (e *storeEnv) recordAccountSubscription(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	accountID string,
	status BillingStatus,
	planID string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.RecordAccountSubscription(t.Context(), tx, scope, accountID, status, planID)
	})
}

func (e *storeEnv) recordAccountSubscriptionEnded(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	accountID string,
	status BillingStatus,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.RecordAccountSubscriptionEnded(t.Context(), tx, scope, accountID, status)
	})
}

func (e *storeEnv) setAccountBillingStatus(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	accountID string,
	status BillingStatus,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.SetAccountBillingStatus(t.Context(), tx, scope, accountID, status)
	})
}

func (e *storeEnv) setAccountPaymentProcessorCustomerID(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	accountID, customerID string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.SetAccountPaymentProcessorCustomerID(t.Context(), tx, scope, accountID, customerID)
	})
}

func (e *storeEnv) markAccountBillingSynced(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	accountID string,
) (*Account, error) {
	t.Helper()

	var synced *Account

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		synced, txErr = store.MarkAccountBillingSynced(t.Context(), tx, scope, accountID)

		return txErr
	})

	return synced, err
}

func (e *storeEnv) markAccountBillingSyncedErr(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	accountID string,
) error {
	t.Helper()

	_, err := e.markAccountBillingSynced(t, store, scope, accountID)

	return err
}

func (e *storeEnv) createInvitation(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	invitation *Invitation,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.CreateInvitation(t.Context(), tx, scope, invitation)
	})
}

func (e *storeEnv) acceptInvitation(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	invitationID, token, acceptingUserID, statusNote string,
) (*Membership, error) {
	t.Helper()

	var membership *Membership

	err := e.inTx(t, func(tx database.Tx) error {
		var txErr error
		membership, txErr = store.AcceptInvitation(
			t.Context(), tx, scope, invitationID, token, acceptingUserID, statusNote)

		return txErr
	})

	return membership, err
}

func (e *storeEnv) setInvitationStatus(
	t *testing.T,
	store *SQLStore,
	scope tenancy.Scope,
	invitationID string,
	status InvitationStatus,
	statusNote string,
) error {
	t.Helper()

	return e.inTx(t, func(tx database.Tx) error {
		return store.SetInvitationStatus(t.Context(), tx, scope, invitationID, status, statusNote)
	})
}

// The fixtures the suites start from, built on the helpers above. They assert
// rather than report, because a failure here is the test not getting to its
// subject.

// seedUser writes a user through a transaction, the way a registration would.
func seedUser(t *testing.T, env *storeEnv, store *SQLStore, user *User) *User {
	t.Helper()

	created, err := env.createUser(t, store, user.Scope, user)
	must.NoError(t, err)

	return created
}

// seedAccountFor writes an account and the owner's membership, which is what a
// registration does and what almost every test needs before it can start.
//
// Both writes go in one transaction, because that is the invariant Registrar
// exists to make visible: an account without an owner has nobody its ownership
// checks resolve to.
func seedAccountFor(t *testing.T, env *storeEnv, store *SQLStore, owner *User, name string, roles ...string) *Account {
	t.Helper()

	if len(roles) == 0 {
		roles = []string{"account_admin"}
	}

	var created *Account

	must.NoError(t, env.inTx(t, func(tx database.Tx) error {
		account, err := store.CreateAccount(t.Context(), tx, owner.Scope, newAccount(name, owner.ID))
		if err != nil {
			return err
		}

		created = account

		_, err = store.CreateMembership(t.Context(), tx, owner.Scope, &Membership{
			BelongsToUser:    owner.ID,
			BelongsToAccount: account.ID,
			Roles:            roles,
		})

		return err
	}))

	return created
}

// seedUserInto writes a user and puts them in an existing account.
func seedUserInto(t *testing.T, env *storeEnv, store *SQLStore, user *User, accountID string, roles ...string) *User {
	t.Helper()

	if len(roles) == 0 {
		roles = []string{"account_member"}
	}

	var created *User

	must.NoError(t, env.inTx(t, func(tx database.Tx) error {
		registered, err := store.CreateUser(t.Context(), tx, user.Scope, user)
		if err != nil {
			return err
		}

		created = registered

		_, err = store.CreateMembership(t.Context(), tx, user.Scope, &Membership{
			BelongsToUser:    registered.ID,
			BelongsToAccount: accountID,
			Roles:            roles,
		})

		return err
	}))

	return created
}

// senderNote is what every invitation this harness builds was sent with: the
// message rendered into the invite email, which no answer may overwrite.
const senderNote = "come and join us"

// newInvitation builds a pending invitation from one user to an address.
func newInvitation(from *User, accountID, toEmail, token string, expires time.Time) *Invitation {
	return &Invitation{
		ID:               identifiers.New(),
		Scope:            from.Scope,
		BelongsToAccount: accountID,
		FromUser:         from.ID,
		ToEmail:          toEmail,
		Token:            token,
		Status:           InvitationPending,
		// The sender's message, carried by every invitation the suite builds
		// so that any answer written on top of one has something to destroy.
		Note:      senderNote,
		ExpiresAt: expires,
		Roles:     []string{"account_member"},
	}
}
