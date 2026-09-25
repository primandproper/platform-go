package signin_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes"
	recoverymigrations "github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/migrations"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHookRefused is a consumer's hook declining to record what it was handed.
var errHookRefused = platformerrors.New("the consumer's hook refused")

// lateRecoveryStore is the recovery code store a test env is built with.
//
// It exists because the env builds its own database client, and the service
// that is handed this store has to exist before the client does — so the
// service is handed this, and the real store goes in once the env is up. What
// it adds beyond delegation is a point between a door's check and its spend,
// which is where a concurrent sign-in lands in production and the only place a
// SQLite suite can put one deterministically.
type lateRecoveryStore struct {
	*recoverycodes.SQLStore

	// afterVerify, where set, runs once a check has passed and before the door
	// that made it opens its transaction.
	afterVerify func()
}

var _ signin.RecoveryCodeStore = (*lateRecoveryStore)(nil)

func (s *lateRecoveryStore) Verify(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, code string,
) error {
	if err := s.SQLStore.Verify(ctx, q, scope, userID, code); err != nil {
		return err
	}

	if s.afterVerify != nil {
		s.afterVerify()
	}

	return nil
}

// recoveryHooks records what the recovery code suite asserts on. It is its own
// type rather than more fields on recordingHooks, so the suites that predate
// recovery codes are exactly the files they were.
type recoveryHooks struct {
	signin.NoopHooks

	usedErr  error
	issueErr error

	calls     []string
	used      []*identity.User
	remaining []int
	failures  []*signin.FailedSignIn
	replaced  int

	mu sync.Mutex
}

func (h *recoveryHooks) AfterRecoveryCodeUsed(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	user *identity.User,
	remaining int,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.calls = append(h.calls, "recovery_code_used")
	h.used = append(h.used, user)
	h.remaining = append(h.remaining, remaining)

	return h.usedErr
}

func (h *recoveryHooks) AfterReplaceRecoveryCodes(context.Context, database.Tx, tenancy.Scope, *identity.User) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.calls = append(h.calls, "replace")
	h.replaced++

	return nil
}

func (h *recoveryHooks) AfterAuthenticate(context.Context, database.Tx, tenancy.Scope, *signin.Authentication) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.calls = append(h.calls, "authenticate")

	return nil
}

func (h *recoveryHooks) AfterIssueToken(context.Context, database.Tx, tenancy.Scope, *signin.SignIn) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.calls = append(h.calls, "issue")

	return h.issueErr
}

func (h *recoveryHooks) AfterFailedSignIn(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	attempt *signin.FailedSignIn,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.failures = append(h.failures, attempt)

	return nil
}

// recoveryEnv is an env with a live recovery code store and a user who holds a
// proven TOTP secret and a set of codes.
type recoveryEnv struct {
	*env

	store  *lateRecoveryStore
	hooks  *recoveryHooks
	secret string
	codes  []string
}

// newRecoveryEnv builds one over whichever env builder a test needs — the plain
// one, the refresh one, the magic link one — so the recovery store sits beside
// whatever else the door under test mints.
func newRecoveryEnv(
	t *testing.T,
	build func(*testing.T, ...signin.ServiceOption) *env,
	opts ...signin.ServiceOption,
) *recoveryEnv {
	t.Helper()

	store := &lateRecoveryStore{}
	hooks := &recoveryHooks{}

	e := build(t, append([]signin.ServiceOption{
		signin.WithHooks(hooks),
		signin.WithRecoveryCodeStore(store),
	}, opts...)...)

	stmts, err := recoverymigrations.Statements(dialect.SQLite, "")
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := e.client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	store.SQLStore, err = recoverycodes.NewSQLStore(&recoverycodes.Config{}, e.client)
	must.NoError(t, err)

	r := &recoveryEnv{env: e, store: store, hooks: hooks}
	r.secret = e.enrollTOTP(t)

	r.codes, err = e.svc.ReplaceRecoveryCodes(t.Context(), testScope, e.user.ID, &signin.RecoveryCodeReplacement{
		CurrentPassword: e.password,
		TOTPCode:        code(t, r.secret),
	})
	must.NoError(t, err)
	must.SliceNotEmpty(t, r.codes)

	// What the set-up wrote is not what any test is about.
	hooks.calls, hooks.replaced = nil, 0

	return r
}

// remaining is how many codes the user holds, asked of the store directly so it
// is not an assertion about the door under test.
func (r *recoveryEnv) remaining(t *testing.T) int {
	t.Helper()

	n, err := r.store.Remaining(t.Context(), r.client.Reader(), testScope, r.user.ID)
	must.NoError(t, err)

	return n
}

// withCode is the happy-path credential set with a recovery code as its second
// factor.
func (r *recoveryEnv) withCode(recoveryCode string) *signin.Credentials {
	credentials := r.credentials()
	credentials.TOTPCode = recoveryCode

	return credentials
}

// spendBehindTheDoor arranges for a code to be spent by somebody else in the
// instant between a door's check and its spend — the concurrent sign-in with the
// same code, landed deterministically.
func (r *recoveryEnv) spendBehindTheDoor(t *testing.T, recoveryCode string) {
	t.Helper()

	var once sync.Once

	r.store.afterVerify = func() {
		once.Do(func() {
			must.NoError(t, r.client.WithTransaction(t.Context(), func(tx database.Tx) error {
				return r.store.Consume(t.Context(), tx, testScope, r.user.ID, recoveryCode)
			}))
		})
	}
}

func TestRecoveryCodes_withoutAStore(T *testing.T) {
	T.Parallel()

	T.Run("the two recovery code doors say they are not configured", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		e.enrollTOTP(t)

		_, err := e.svc.ReplaceRecoveryCodes(t.Context(), testScope, e.user.ID, &signin.RecoveryCodeReplacement{
			CurrentPassword: e.password,
		})
		test.ErrorIs(t, err, signin.ErrRecoveryCodesNotConfigured)

		_, err = e.svc.RecoveryCodesRemaining(t.Context(), testScope, e.user.ID)
		test.ErrorIs(t, err, signin.ErrRecoveryCodesNotConfigured)
	})

	// Naming no store leaves every door exactly as it was: a code that is not
	// the TOTP code is a code that did not verify, whatever it looks like.
	T.Run("a code shaped like a recovery code is a wrong second-factor code", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		e.enrollTOTP(t)

		credentials := e.credentials()
		credentials.TOTPCode = "ABCD-EFGH-IJKL"

		_, err := e.svc.LoginForToken(t.Context(), testScope, credentials)
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})
}

func TestService_ReplaceRecoveryCodes(T *testing.T) {
	T.Parallel()

	T.Run("mints a set behind the password and the second factor", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		codes, err := r.svc.ReplaceRecoveryCodes(t.Context(), testScope, r.user.ID, &signin.RecoveryCodeReplacement{
			CurrentPassword: r.password,
			TOTPCode:        code(t, r.secret),
		})
		must.NoError(t, err)
		must.SliceLen(t, signin.DefaultRecoveryCodeCount, codes)

		test.EqOp(t, 1, r.hooks.replaced)
		test.EqOp(t, signin.DefaultRecoveryCodeCount, r.remaining(t))

		// The set it replaced is gone, every code of it.
		for _, old := range r.codes {
			_, loginErr := r.svc.LoginForToken(t.Context(), testScope, r.withCode(old))
			test.ErrorIs(t, loginErr, signin.ErrInvalidCredentials)
		}

		_, err = r.svc.LoginForToken(t.Context(), testScope, r.withCode(codes[0]))
		test.NoError(t, err)
	})

	T.Run("mints as many as the service was told to", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv, signin.WithRecoveryCodeCount(3))

		test.SliceLen(t, 3, r.codes)
	})

	// The lost-phone case for the set itself: the codes being replaced are a
	// second factor, so one of them is enough to mint the next set.
	T.Run("takes one of the codes being replaced as the second factor", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		codes, err := r.svc.ReplaceRecoveryCodes(t.Context(), testScope, r.user.ID, &signin.RecoveryCodeReplacement{
			CurrentPassword: r.password,
			TOTPCode:        r.codes[0],
		})
		must.NoError(t, err)
		test.SliceLen(t, signin.DefaultRecoveryCodeCount, codes)

		test.Eq(t, []string{"recovery_code_used", "replace"}, r.hooks.calls)
		test.EqOp(t, signin.DefaultRecoveryCodeCount, r.remaining(t))
	})

	T.Run("refuses a code another request spent first", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)
		r.spendBehindTheDoor(t, r.codes[0])

		_, err := r.svc.ReplaceRecoveryCodes(t.Context(), testScope, r.user.ID, &signin.RecoveryCodeReplacement{
			CurrentPassword: r.password,
			TOTPCode:        r.codes[0],
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		// Nothing was replaced: the set the person holds is the one they had,
		// less the code the other request spent.
		test.EqOp(t, 0, r.hooks.replaced)
		test.EqOp(t, signin.DefaultRecoveryCodeCount-1, r.remaining(t))
	})

	T.Run("refuses a user who holds no second factor to recover", func(t *testing.T) {
		t.Parallel()

		store := &lateRecoveryStore{}
		e := newEnv(t, signin.WithRecoveryCodeStore(store))

		_, err := e.svc.ReplaceRecoveryCodes(t.Context(), testScope, e.user.ID, &signin.RecoveryCodeReplacement{
			CurrentPassword: e.password,
		})
		test.ErrorIs(t, err, signin.ErrSecondFactorNotEnrolled)
	})

	T.Run("refuses a user who holds no password", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)
		passwordless := r.registerPasswordless(t, "ada")

		_, err := r.svc.ReplaceRecoveryCodes(t.Context(), testScope, passwordless.ID, &signin.RecoveryCodeReplacement{
			CurrentPassword: r.password,
		})
		test.ErrorIs(t, err, signin.ErrNoPasswordCredential)
	})

	T.Run("refuses what reauthentication refuses", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		for name, tc := range map[string]struct {
			replacement *signin.RecoveryCodeReplacement
			want        error
		}{
			"no password":    {&signin.RecoveryCodeReplacement{TOTPCode: r.codes[0]}, signin.ErrEmptyPassword},
			"wrong password": {&signin.RecoveryCodeReplacement{CurrentPassword: "nope", TOTPCode: r.codes[0]}, signin.ErrInvalidCredentials},
			"no code":        {&signin.RecoveryCodeReplacement{CurrentPassword: r.password}, signin.ErrSecondFactorRequired},
			"wrong code":     {&signin.RecoveryCodeReplacement{CurrentPassword: r.password, TOTPCode: "AAAA-AAAA-AAAA"}, signin.ErrInvalidCredentials},
		} {
			_, err := r.svc.ReplaceRecoveryCodes(t.Context(), testScope, r.user.ID, tc.replacement)
			test.ErrorIs(t, err, tc.want, test.Sprintf("%s", name))
		}

		// None of those spent anything or replaced anything.
		test.EqOp(t, 0, r.hooks.replaced)
		test.EqOp(t, signin.DefaultRecoveryCodeCount, r.remaining(t))
	})

	T.Run("refuses a request with nothing on it", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		_, err := r.svc.ReplaceRecoveryCodes(t.Context(), testScope, r.user.ID, nil)
		test.ErrorIs(t, err, signin.ErrNilRecoveryCodeReplacement)

		_, err = r.svc.ReplaceRecoveryCodes(t.Context(), testScope, "", &signin.RecoveryCodeReplacement{})
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})
}

func TestService_RecoveryCodesRemaining(T *testing.T) {
	T.Parallel()

	T.Run("counts what is left as codes are spent", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		remaining, err := r.svc.RecoveryCodesRemaining(t.Context(), testScope, r.user.ID)
		must.NoError(t, err)
		test.EqOp(t, signin.DefaultRecoveryCodeCount, remaining)

		_, err = r.svc.LoginForToken(t.Context(), testScope, r.withCode(r.codes[0]))
		must.NoError(t, err)

		remaining, err = r.svc.RecoveryCodesRemaining(t.Context(), testScope, r.user.ID)
		must.NoError(t, err)
		test.EqOp(t, signin.DefaultRecoveryCodeCount-1, remaining)
	})

	T.Run("refuses a count of nobody", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		_, err := r.svc.RecoveryCodesRemaining(t.Context(), testScope, "")
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})
}

func TestLoginForToken_recoveryCode(T *testing.T) {
	T.Parallel()

	T.Run("signs somebody in on a recovery code, and spends it first", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		signIn, err := r.svc.LoginForToken(t.Context(), testScope, r.withCode(r.codes[0]))
		must.NoError(t, err)
		test.EqOp(t, r.user.ID, signIn.Principal.User.ID)

		// The spend is the first thing the sign-in's transaction does, and its
		// hook is handed what is left once it commits.
		test.Eq(t, []string{"recovery_code_used", "authenticate", "issue"}, r.hooks.calls)
		test.Eq(t, []int{signin.DefaultRecoveryCodeCount - 1}, r.hooks.remaining)

		// Redacted: the hook that must mail somebody is not the one to hand a
		// password hash or a TOTP secret to.
		must.SliceLen(t, 1, r.hooks.used)
		test.EqOp(t, r.user.ID, r.hooks.used[0].ID)
		test.EqOp(t, "", r.hooks.used[0].HashedPassword)
		test.EqOp(t, "", r.hooks.used[0].TwoFactorSecret)

		test.EqOp(t, signin.DefaultRecoveryCodeCount-1, r.remaining(t))
	})

	T.Run("a spent code is a wrong code, and a failed sign-in", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		_, err := r.svc.LoginForToken(t.Context(), testScope, r.withCode(r.codes[0]))
		must.NoError(t, err)

		_, err = r.svc.LoginForToken(t.Context(), testScope, r.withCode(r.codes[0]))
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		must.SliceLen(t, 1, r.hooks.failures)
		test.ErrorIs(t, r.hooks.failures[0].Reason, signin.ErrInvalidCredentials)
		test.EqOp(t, r.user.ID, r.hooks.failures[0].UserID)
	})

	// The disclosure rule: a wrong recovery code and a wrong TOTP code are one
	// refusal, and both count against the account.
	T.Run("a wrong recovery code is refused as a wrong TOTP code is", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		_, wrongRecovery := r.svc.LoginForToken(t.Context(), testScope, r.withCode("AAAA-BBBB-CCCC"))
		_, wrongTOTP := r.svc.LoginForToken(t.Context(), testScope, r.withCode("000000"))

		test.ErrorIs(t, wrongRecovery, signin.ErrInvalidCredentials)
		test.ErrorIs(t, wrongTOTP, signin.ErrInvalidCredentials)
		test.EqOp(t, wrongTOTP.Error(), wrongRecovery.Error())

		must.SliceLen(t, 2, r.hooks.failures)
		test.EqOp(t, signin.DefaultRecoveryCodeCount, r.remaining(t))
	})

	T.Run("takes a code however a person copied it off paper", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		typed := strings.ToLower(strings.ReplaceAll(r.codes[0], "-", ""))

		_, err := r.svc.LoginForToken(t.Context(), testScope, r.withCode(typed))
		must.NoError(t, err)
		test.EqOp(t, signin.DefaultRecoveryCodeCount-1, r.remaining(t))
	})

	T.Run("a TOTP code spends nothing", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		_, err := r.svc.LoginForToken(t.Context(), testScope, r.withCode(code(t, r.secret)))
		must.NoError(t, err)

		test.Eq(t, []string{"authenticate", "issue"}, r.hooks.calls)
		test.EqOp(t, signin.DefaultRecoveryCodeCount, r.remaining(t))
	})

	// The concurrent case, landed deterministically: another sign-in spends the
	// code between this one's check and its spend. This one is refused, recorded
	// as a failed sign-in, and nothing is minted or written for it.
	T.Run("refuses a sign-in whose code another request spent first", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newRefreshEnv)
		r.spendBehindTheDoor(t, r.codes[0])

		_, err := r.svc.LoginForToken(t.Context(), testScope, r.withCode(r.codes[0]))
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		must.SliceLen(t, 1, r.hooks.failures)
		test.ErrorIs(t, r.hooks.failures[0].Reason, signin.ErrInvalidCredentials)

		test.SliceEmpty(t, r.hooks.calls)
		test.EqOp(t, 0, rowsIn(t, r.env, refreshTable(t, r.env)))
	})

	T.Run("a hook that refuses the spend leaves the code unspent", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)
		r.hooks.usedErr = errHookRefused

		_, err := r.svc.LoginForToken(t.Context(), testScope, r.withCode(r.codes[0]))
		test.ErrorIs(t, err, errHookRefused)
		test.EqOp(t, signin.DefaultRecoveryCodeCount, r.remaining(t))
	})

	T.Run("a sign-in the consumer cannot record leaves the code unspent", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)
		r.hooks.issueErr = errHookRefused

		_, err := r.svc.LoginForToken(t.Context(), testScope, r.withCode(r.codes[0]))
		test.ErrorIs(t, err, errHookRefused)
		test.EqOp(t, signin.DefaultRecoveryCodeCount, r.remaining(t))

		// Not a failed sign-in: the credentials were proven, and the consumer's
		// refusal is theirs rather than an attempt at the account.
		test.SliceEmpty(t, r.hooks.failures)
	})

	T.Run("the administrative door takes one too", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv, signin.WithAdminServiceRoles("service_admin"))
		r.setServiceRoles(t, "service_admin")

		signIn, err := r.svc.AdminLoginForToken(t.Context(), testScope, r.withCode(r.codes[0]))
		must.NoError(t, err)
		test.True(t, signIn.Administrative)
		test.EqOp(t, signin.DefaultRecoveryCodeCount-1, r.remaining(t))
	})
}

func TestAuthenticate_recoveryCode(T *testing.T) {
	T.Parallel()

	T.Run("proves somebody on a recovery code and spends it", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		principal, err := r.svc.Authenticate(t.Context(), testScope, r.withCode(r.codes[0]))
		must.NoError(t, err)
		test.EqOp(t, r.user.ID, principal.User.ID)

		test.Eq(t, []string{"recovery_code_used", "authenticate"}, r.hooks.calls)
		test.EqOp(t, signin.DefaultRecoveryCodeCount-1, r.remaining(t))
	})

	T.Run("refuses an authentication whose code another request spent first", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)
		r.spendBehindTheDoor(t, r.codes[0])

		_, err := r.svc.Authenticate(t.Context(), testScope, r.withCode(r.codes[0]))
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		must.SliceLen(t, 1, r.hooks.failures)
		test.SliceEmpty(t, r.hooks.calls)
	})
}

func TestRefreshTOTPSecret_recoveryCode(T *testing.T) {
	T.Parallel()

	// The lost-phone door, and the reason this lives in signin at all.
	T.Run("re-enrolls a user who has lost their authenticator", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)

		enrollment, err := r.svc.RefreshTOTPSecret(t.Context(), testScope, r.user.ID, &signin.SecretRefresh{
			CurrentPassword: r.password,
			TOTPCode:        r.codes[0],
		})
		must.NoError(t, err)
		test.NotEq(t, r.secret, enrollment.Secret)

		test.Eq(t, []string{"recovery_code_used"}, r.hooks.calls)
		test.EqOp(t, signin.DefaultRecoveryCodeCount-1, r.remaining(t))

		// The rest of the set survives a re-enrollment: it recovers the person,
		// not one particular phone.
		must.NoError(t, r.svc.VerifyTOTPSecret(t.Context(), testScope, r.user.ID, code(t, enrollment.Secret)))

		_, err = r.svc.LoginForToken(t.Context(), testScope, r.withCode(r.codes[1]))
		test.NoError(t, err)
	})

	T.Run("refuses a re-enrollment whose code another request spent first", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newEnv)
		r.spendBehindTheDoor(t, r.codes[0])

		_, err := r.svc.RefreshTOTPSecret(t.Context(), testScope, r.user.ID, &signin.SecretRefresh{
			CurrentPassword: r.password,
			TOTPCode:        r.codes[0],
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		// The secret the person held is still the one they hold.
		_, err = r.svc.LoginForToken(t.Context(), testScope, r.withCode(code(t, r.secret)))
		test.NoError(t, err)
	})
}

// A recovery code guards the second factor, not the password: somebody who has
// lost their authenticator re-enrolls one first.
func TestUpdatePassword_takesNoRecoveryCode(t *testing.T) {
	t.Parallel()

	r := newRecoveryEnv(t, newEnv)

	err := r.svc.UpdatePassword(t.Context(), testScope, r.user.ID, &signin.PasswordUpdate{
		CurrentPassword: r.password,
		NewPassword:     "a different horse",
		TOTPCode:        r.codes[0],
	})
	test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	test.EqOp(t, signin.DefaultRecoveryCodeCount, r.remaining(t))
}

func TestRedeemMagicLink_recoveryCode(T *testing.T) {
	T.Parallel()

	T.Run("signs somebody in on a link and a recovery code, and spends both", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newMagicLinkEnv)

		must.NoError(t, r.svc.RequestMagicLink(t.Context(), testScope, r.user.EmailAddress))

		_, err := r.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{
			Token:    redeemToken(t, r.env),
			TOTPCode: r.codes[0],
		})
		must.NoError(t, err)

		test.Eq(t, []string{"recovery_code_used", "authenticate", "issue"}, r.hooks.calls)
		test.EqOp(t, signin.DefaultRecoveryCodeCount-1, r.remaining(t))
	})

	// This door checks the code on the transaction that spends the link, so the
	// check and the spend are one snapshot and there is no gap between them for
	// another request to land in. What is left to assert is that a code already
	// spent is refused there and costs the person nothing but the code.
	T.Run("refuses a redemption on a spent code, and keeps the link", func(t *testing.T) {
		t.Parallel()

		r := newRecoveryEnv(t, newMagicLinkEnv)

		must.NoError(t, r.svc.RequestMagicLink(t.Context(), testScope, r.user.EmailAddress))
		token := redeemToken(t, r.env)

		must.NoError(t, r.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			return r.store.Consume(t.Context(), tx, testScope, r.user.ID, r.codes[0])
		}))

		_, err := r.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{
			Token:    token,
			TOTPCode: r.codes[0],
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		must.SliceLen(t, 1, r.hooks.failures)

		// The refusal rolled the spend of the link back with it, so the person
		// tries again with another code rather than asking for another mail.
		_, err = r.svc.RedeemMagicLink(t.Context(), testScope, &signin.MagicLinkCredentials{
			Token:    token,
			TOTPCode: r.codes[1],
		})
		test.NoError(t, err)
	})
}

// rowsIn counts the rows in one table of an env's database.
func rowsIn(t *testing.T, e *env, table string) int {
	t.Helper()

	var count int
	must.NoError(t, e.client.Writer().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count))

	return count
}
