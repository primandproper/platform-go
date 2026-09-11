package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/sessions"

	"github.com/primandproper/primitives-go/authentication"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The flow in loginflow_example_test.go is the ruling in the package
// documentation of [github.com/primandproper/primitives-go/authentication]
// made executable, so the four mistakes that ruling names are asserted here
// rather than described. A change to the example that reintroduces one of them
// fails a test instead of passing review.

const examplePassword = "correct horse battery staple"

// countingAuthenticator records how many comparisons a sign-in spent, which is
// what the enumeration guarantee is actually made of: a handle that resolves to
// nobody has to cost a verification too.
type countingAuthenticator struct {
	inner      authentication.Authenticator
	comparison int
}

var _ authentication.Authenticator = (*countingAuthenticator)(nil)

func (a *countingAuthenticator) HashPassword(ctx context.Context, password string) (string, error) {
	return a.inner.HashPassword(ctx, password)
}

func (a *countingAuthenticator) PasswordMatches(ctx context.Context, hash, password string) (bool, error) {
	a.comparison++

	return a.inner.PasswordMatches(ctx, hash, password)
}

func TestLoginFlow_RefusesUnknownHandlesAndWrongPasswordsAlike(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	flow, scope := exampleFlow()
	exampleUser(ctx, flow, "ada", examplePassword, exampleTOTPSecret)

	counter := &countingAuthenticator{inner: flow.passwords}
	flow.passwords = counter

	// A handle nobody registered. Returning before the comparison is what makes
	// the response time the answer to "does this account exist".
	outcome, err := flow.SignIn(ctx, scope, &signInRequest{Handle: "nobody", Password: examplePassword})

	test.ErrorIs(t, err, errBadCredentials)
	test.Nil(t, outcome)
	test.EqOp(t, 1, counter.comparison)

	// A registered handle with the wrong password: the same value, and one more
	// comparison, so neither the timing nor the error tells the two apart.
	outcome, err = flow.SignIn(ctx, scope, &signInRequest{Handle: "ada", Password: "hunter2"})

	test.ErrorIs(t, err, errBadCredentials)
	test.Nil(t, outcome)
	test.EqOp(t, 2, counter.comparison)

	// Nothing about the account leaks through the refusal, so the two answers
	// are the same value rather than merely the same category.
	refusal := new(accountStatusError)
	test.False(t, errors.As(err, &refusal))
}

func TestLoginFlow_ChecksTheStatusAfterThePassword(T *testing.T) {
	T.Parallel()

	ctx := T.Context()
	flow, scope := exampleFlow()
	user := exampleUser(ctx, flow, "ada", examplePassword, exampleTOTPSecret)
	exampleBan(ctx, flow, scope, user.ID, "chargebacks")

	T.Run("a banned account with the wrong password says only that", func(t *testing.T) {
		t.Parallel()

		_, err := flow.SignIn(ctx, scope, &signInRequest{Handle: "ada", Password: "hunter2"})

		// The ban is a fact about somebody else. Checking the status first would
		// hand it, and the account's existence, to anyone who guessed a handle.
		test.ErrorIs(t, err, errBadCredentials)

		refusal := new(accountStatusError)
		test.False(t, errors.As(err, &refusal))
	})

	T.Run("a banned account with the right password gets the explanation", func(t *testing.T) {
		t.Parallel()

		outcome, err := flow.SignIn(ctx, scope, &signInRequest{Handle: "ada", Password: examplePassword})

		test.Nil(t, outcome)

		refusal := new(accountStatusError)
		must.True(t, errors.As(err, &refusal))
		test.EqOp(t, identity.StatusBanned, refusal.Status)
		test.EqOp(t, "chargebacks", refusal.Explanation)
	})
}

func TestLoginFlow_MintsNothingBeforeTheSecondFactor(T *testing.T) {
	T.Parallel()

	ctx := T.Context()
	flow, scope := exampleFlow()
	exampleUser(ctx, flow, "ada", examplePassword, exampleTOTPSecret)

	T.Run("a correct password with no code is not a sign-in", func(t *testing.T) {
		t.Parallel()

		outcome, err := flow.SignIn(ctx, scope, &signInRequest{Handle: "ada", Password: examplePassword})

		must.NoError(t, err)
		must.NotNil(t, outcome)
		test.True(t, outcome.SecondFactorRequired)
		// The bypass this forbids is a session established "for the second
		// step" and then never gated.
		test.Nil(t, outcome.Session)
	})

	T.Run("a correct password with a wrong code refuses", func(t *testing.T) {
		t.Parallel()

		outcome, err := flow.SignIn(ctx, scope, &signInRequest{
			Handle:   "ada",
			Password: examplePassword,
			Code:     "000000",
		})

		test.ErrorIs(t, err, errBadCredentials)
		test.Nil(t, outcome)
	})

	T.Run("a correct password with a valid code signs in", func(t *testing.T) {
		t.Parallel()

		outcome, err := flow.SignIn(ctx, scope, &signInRequest{
			Handle:   "ada",
			Password: examplePassword,
			Code:     exampleCode(),
		})

		must.NoError(t, err)
		must.NotNil(t, outcome)
		must.NotNil(t, outcome.Session)
		test.False(t, outcome.SecondFactorRequired)
	})
}

func TestLoginFlow_TreatsAnUnprovenSecretAsNoSecondFactor(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	flow, scope := exampleFlow()

	// Registered with a secret and never verified: a QR code they may have
	// closed. Gating on the column rather than on the proof locks them out.
	hashed, err := flow.passwords.HashPassword(ctx, examplePassword)
	must.NoError(t, err)

	user := &identity.User{
		Scope:           scope,
		Username:        "grace",
		EmailAddress:    "grace@example.com",
		HashedPassword:  hashed,
		TwoFactorSecret: exampleTOTPSecret,
		AccountStatus:   identity.StatusGood,
	}

	must.NoError(t, flow.client.WithTransaction(ctx, func(tx database.Tx) error {
		_, createErr := flow.store.CreateUser(ctx, tx, scope, user)

		return createErr
	}))

	outcome, err := flow.SignIn(ctx, scope, &signInRequest{Handle: "grace", Password: examplePassword})

	must.NoError(t, err)
	must.NotNil(t, outcome)
	test.False(t, outcome.SecondFactorRequired)
	must.NotNil(t, outcome.Session)
}

func TestLoginFlow_EstablishesAFreshIdentifier(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	flow, scope := exampleFlow()
	user := exampleUser(ctx, flow, "ada", examplePassword, exampleTOTPSecret)

	priorSessionID := exampleAnonymousSession(ctx, flow)

	outcome, err := flow.SignIn(ctx, scope, &signInRequest{
		Handle:         "ada",
		Password:       examplePassword,
		Code:           exampleCode(),
		PriorSessionID: priorSessionID,
	})
	must.NoError(t, err)
	must.NotNil(t, outcome.Session)

	// Session fixation: an identifier planted in the browser before the sign-in
	// must not be one afterwards.
	test.NotEqOp(t, priorSessionID, outcome.Session.ID)

	_, err = flow.sessions.Get(ctx, priorSessionID)
	test.ErrorIs(t, err, sessions.ErrNotFound)

	// And the new one is held by somebody, which is what a sign-out control
	// later aims at.
	read, err := flow.sessions.Get(ctx, outcome.Session.ID)
	must.NoError(t, err)
	test.EqOp(t, user.ID, read.Data.UserID)
	test.EqOp(t, user.ID, read.Holder.Principal)
	test.EqOp(t, tenancy.Global(), read.Holder.Scope)
}

func TestLoginFlow_DoesNotCollapseABrokenHashIntoARefusal(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	flow, scope := exampleFlow()

	// A hash the engine cannot parse is an operational failure. Reporting it as
	// a wrong password is how a column corrupted by a bad migration looks like
	// a fleet of users who forgot their passwords.
	user := &identity.User{
		Scope:          scope,
		Username:       "hopper",
		EmailAddress:   "hopper@example.com",
		HashedPassword: "not-an-argon2-hash",
		AccountStatus:  identity.StatusGood,
	}

	must.NoError(t, flow.client.WithTransaction(ctx, func(tx database.Tx) error {
		_, createErr := flow.store.CreateUser(ctx, tx, scope, user)

		return createErr
	}))

	outcome, err := flow.SignIn(ctx, scope, &signInRequest{Handle: "hopper", Password: examplePassword})

	must.Error(t, err)
	test.False(t, errors.Is(err, errBadCredentials))
	test.Nil(t, outcome)
}
