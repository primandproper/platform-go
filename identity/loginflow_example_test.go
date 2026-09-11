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
	"github.com/primandproper/platform-go/v14/sessions"
	sessionscache "github.com/primandproper/platform-go/v14/sessions/cache"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/totp"
	"github.com/primandproper/primitives-go/v2/cache/memory"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	otp "github.com/pquerna/otp/totp"
)

// Example_loginFlow is the sign-in this module declines to ship, written the way
// [github.com/primandproper/primitives-go/v2/authentication]'s package
// documentation says to write it.
//
// Everything it calls is a package beside this one. What is written here — the
// order, the decoy comparison, the single refusal, the second-factor gate, the
// fresh identifier — is the part that ruling leaves to the consumer, and the
// part a second copy can get wrong.
//
// It is in this package rather than in authentication because it signs in
// against a real user store, and authentication is a primitive that may not
// import one — see that package's documentation for the whole reason.
func Example_loginFlow() {
	ctx := context.Background()
	flow, scope := exampleFlow()

	// Ada holds a password and a proven TOTP secret.
	user := exampleUser(ctx, flow, "ada", "correct horse battery staple", exampleTOTPSecret)

	// A handle nobody registered, and a registered handle with the wrong
	// password, are the same answer at the same cost.
	_, err := flow.SignIn(ctx, scope, &signInRequest{Handle: "nobody", Password: "hunter2"})
	fmt.Println("unknown handle:", errors.Is(err, errBadCredentials))

	_, err = flow.SignIn(ctx, scope, &signInRequest{Handle: "ada", Password: "hunter2"})
	fmt.Println("wrong password:", errors.Is(err, errBadCredentials))

	// The right password and no code is not a sign-in. What comes back says a
	// code is required and carries nothing a client could present instead of
	// one.
	outcome, err := flow.SignIn(ctx, scope, &signInRequest{
		Handle:   "ada",
		Password: "correct horse battery staple",
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("code required:", outcome.SecondFactorRequired, "- session minted:", outcome.Session != nil)

	// With the code, a session — established under a fresh identifier, so
	// whatever the browser was carrying before signing in is now worthless.
	priorSessionID := exampleAnonymousSession(ctx, flow)

	outcome, err = flow.SignIn(ctx, scope, &signInRequest{
		Handle:         "ada",
		Password:       "correct horse battery staple",
		Code:           exampleCode(),
		PriorSessionID: priorSessionID,
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("signed in:", outcome.Session.Data.UserID == user.ID)
	fmt.Println("identifier rotated:", outcome.Session.ID != priorSessionID)

	_, err = flow.sessions.Get(ctx, priorSessionID)
	fmt.Println("pre-sign-in session dead:", errors.Is(err, sessions.ErrNotFound))

	// A ban is answered after the password has been proven, so the explanation
	// reaches the person it was written for and nobody else can ask for it.
	exampleBan(ctx, flow, scope, user.ID, "chargebacks")

	_, err = flow.SignIn(ctx, scope, &signInRequest{Handle: "ada", Password: "hunter2"})
	fmt.Println("banned, wrong password:", errors.Is(err, errBadCredentials))

	_, err = flow.SignIn(ctx, scope, &signInRequest{
		Handle:   "ada",
		Password: "correct horse battery staple",
	})

	refusal := new(accountStatusError)
	if errors.As(err, &refusal) {
		fmt.Println("banned, right password:", refusal.Explanation)
	}

	// Output:
	// unknown handle: true
	// wrong password: true
	// code required: true - session minted: false
	// signed in: true
	// identifier rotated: true
	// pre-sign-in session dead: true
	// banned, wrong password: true
	// banned, right password: chargebacks
}

// principal is what this application's sessions carry. The user ID and nothing
// else: see the package documentation on why the permission map is a decision
// rather than a default.
type principal struct {
	UserID string
}

type (
	// signInRequest is what a sign-in handler has after parsing its form.
	signInRequest struct {
		Handle   string
		Password string
		// Code is the second factor, empty on the first of the two submissions
		// a user with one makes.
		Code string
		// PriorSessionID is whatever identifier the client arrived carrying,
		// which is not evidence of anything and is destroyed rather than reused.
		PriorSessionID string
		Metadata       sessions.Metadata
	}

	// signInOutcome is what the flow reports. Exactly one of its two states is
	// ever populated: a session, or the fact that a code is still owed.
	signInOutcome struct {
		Session              *sessions.Session[principal]
		SecondFactorRequired bool
	}
)

// errBadCredentials is the one refusal three packages' failures collapse into:
// an unknown handle, a wrong password, and a wrong code. It is declared here
// rather than in the module because it has to cover all three — see the package
// documentation.
var errBadCredentials = errors.New("those credentials do not sign anyone in")

// accountStatusError is the answer for somebody who proved their password and
// still may not sign in. It carries the operator's explanation, which is written
// to be read by the account's owner and by nobody who has not proven they are.
type accountStatusError struct {
	Status      identity.AccountStatus
	Explanation string
}

func (e *accountStatusError) Error() string {
	return fmt.Sprintf("account is %s: %s", e.Status, e.Explanation)
}

// loginFlow is the orchestration. Every field is a seam this module ships and
// none of the logic between them is.
type loginFlow struct {
	directory    identity.SignInReader
	passwords    authentication.Authenticator
	secondFactor totp.Verifier
	sessions     sessions.Store[principal]

	// reader is the executor the directory read runs on. Every read in that
	// store takes one, and a sign-in has nothing to commit alongside — so this
	// is the client's read side, outside any transaction, and the flow never
	// holds anything that could open one.
	reader database.SQLQueryExecutor

	// decoyHash is compared against when the handle resolved to nobody, so the
	// miss costs an argon2id verification like every other answer does.
	decoyHash string
}

// exampleDeployment is the flow plus the scaffolding around it: the write side
// of the identity store, which a sign-in has no business holding, and which the
// example needs to arrange somebody to sign in as.
type exampleDeployment struct {
	*loginFlow

	client database.Client
	store  identity.Store
}

// SignIn is the order the package documentation names, and the reason each step
// is where it is.
func (f *loginFlow) SignIn(ctx context.Context, scope tenancy.Scope, req *signInRequest) (*signInOutcome, error) {
	// A limiter belongs here, keyed on whatever the deployment decided to spend
	// its budget on. ratelimiting.RateLimiter is the seam; the key is the
	// policy, and this module has no opinion about it.

	user, err := f.directory.GetUserByUsername(ctx, f.reader, scope, req.Handle)
	switch {
	case errors.Is(err, identity.ErrUserNotFound):
		// Spend the comparison anyway. Returning here would make the response
		// time say whether the handle exists.
		if _, decoyErr := f.passwords.PasswordMatches(ctx, f.decoyHash, req.Password); decoyErr != nil {
			return nil, decoyErr
		}

		return nil, errBadCredentials
	case err != nil:
		return nil, err
	}

	// The password, before anything is said about the account behind it.
	matched, err := f.passwords.PasswordMatches(ctx, user.HashedPassword, req.Password)
	if err != nil {
		// A stored hash that will not parse is an operational failure, not a
		// wrong password, and collapsing it into the refusal would hide it.
		return nil, err
	}

	if !matched {
		return nil, errBadCredentials
	}

	// Only now: whether this user may sign in at all.
	if !user.AccountStatus.AdmitsSignIn() {
		return nil, &accountStatusError{Status: user.AccountStatus, Explanation: user.AccountStatusExplanation}
	}

	// A verified secret, not a non-empty one — an unproven secret is a QR code
	// somebody may have closed, and treating it as a factor locks them out.
	if user.TwoFactorSecretVerifiedAt != nil {
		if req.Code == "" {
			// Nothing is minted here. Whatever carries this user to their second
			// submission is a credential of its own, and this application asks
			// them to submit the password again rather than hold one.
			return &signInOutcome{SecondFactorRequired: true}, nil
		}

		if err = f.secondFactor.Verify(ctx, user.TwoFactorSecret, req.Code); err != nil {
			return nil, errBadCredentials
		}
	}

	// Whatever identifier the client arrived with stops resolving, so an
	// identifier planted before sign-in is not one afterwards.
	if req.PriorSessionID != "" {
		if err = f.sessions.Delete(ctx, req.PriorSessionID); err != nil && !errors.Is(err, sessions.ErrNotFound) {
			return nil, err
		}
	}

	session, err := f.sessions.NewFor(ctx,
		sessions.Holder{Scope: scope, Principal: user.ID},
		req.Metadata,
		&principal{UserID: user.ID},
	)
	if err != nil {
		return nil, err
	}

	// The event goes here, after the session exists, and a refusal above wants
	// one too. audit and eventstream are where it goes; the vocabulary is this
	// application's.

	return &signInOutcome{Session: session}, nil
}

// exampleTOTPSecret is a fixed secret so the example's output does not move.
const exampleTOTPSecret = "JBSWY3DPEHPK3PXP"

func exampleCode() string {
	code, err := otp.GenerateCode(exampleTOTPSecret, time.Now().UTC())
	if err != nil {
		panic(err)
	}

	return code
}

// exampleFlow wires the seams: an identity store over SQLite, argon2, totp, and
// a session store over an in-memory cache.
func exampleFlow() (*exampleDeployment, tenancy.Scope) {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "authentication-example")
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

	c, err := memory.NewInMemoryCache[sessions.Record[principal]](time.Hour)
	if err != nil {
		panic(err)
	}

	backend, err := sessionscache.NewBackend(c)
	if err != nil {
		panic(err)
	}

	sessionStore, err := sessions.NewStore[principal](backend)
	if err != nil {
		panic(err)
	}

	authenticator := argon2.NewArgon2Authenticator()

	// Minted once, at startup, and compared against for every handle that
	// resolves to nobody.
	decoy, err := authenticator.HashPassword(ctx, "a password nobody has")
	if err != nil {
		panic(err)
	}

	return &exampleDeployment{
		loginFlow: &loginFlow{
			directory:    store,
			reader:       client.Reader(),
			passwords:    authenticator,
			secondFactor: totp.NewVerifier(),
			sessions:     sessionStore,
			decoyHash:    decoy,
		},
		client: client,
		store:  store,
	}, tenancy.Global()
}

// exampleUser registers somebody who can sign in: good standing, a hashed
// password, and a second-factor secret they have proven they hold.
func exampleUser(ctx context.Context, flow *exampleDeployment, username, password, secret string) *identity.User {
	hashed, err := flow.passwords.HashPassword(ctx, password)
	if err != nil {
		panic(err)
	}

	scope := tenancy.Global()

	user := &identity.User{
		Scope:           scope,
		Username:        username,
		EmailAddress:    username + "@example.com",
		HashedPassword:  hashed,
		TwoFactorSecret: secret,
		AccountStatus:   identity.StatusGood,
	}

	// The registration and the verification that follows it are two
	// transactions because they are two acts; a deployment doing both at once
	// would pass one Tx to both.
	if err = flow.client.WithTransaction(ctx, func(tx database.Tx) error {
		registered, createErr := flow.store.CreateUser(ctx, tx, scope, user)
		user = registered

		return createErr
	}); err != nil {
		panic(err)
	}

	// Issuing a secret is not holding one. This is the write that turns the
	// column into a second factor.
	if err = flow.client.WithTransaction(ctx, func(tx database.Tx) error {
		verified, verifyErr := flow.store.MarkUserTwoFactorSecretVerified(ctx, tx, scope, user.ID)
		if verifyErr != nil {
			return verifyErr
		}

		user = verified

		return nil
	}); err != nil {
		panic(err)
	}

	return user
}

func exampleAnonymousSession(ctx context.Context, flow *exampleDeployment) string {
	session, err := flow.sessions.New(ctx, &principal{})
	if err != nil {
		panic(err)
	}

	return session.ID
}

func exampleBan(ctx context.Context, flow *exampleDeployment, scope tenancy.Scope, userID, explanation string) {
	if err := flow.client.WithTransaction(ctx, func(tx database.Tx) error {
		return flow.store.UpdateUserAccountStatus(ctx, tx, scope, userID, identity.StatusBanned, explanation)
	}); err != nil {
		panic(err)
	}
}

// The database.ClientConfig this uses is exampleClientConfig, declared once in
// example_test.go beside this package's other executable example.
