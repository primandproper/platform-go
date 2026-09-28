package signin_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// verificationMailbox is the VerificationMailer these tests wire in: it keeps
// what it was handed, and can be told to fail.
type verificationMailbox struct {
	err  error
	sent []*signin.VerificationMail
	mu   sync.Mutex
}

var _ signin.VerificationMailer = (*verificationMailbox)(nil)

func (m *verificationMailbox) SendVerification(_ context.Context, mail *signin.VerificationMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return m.err
	}

	m.sent = append(m.sent, mail)

	return nil
}

func (m *verificationMailbox) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sent)
}

func (m *verificationMailbox) last(tb testing.TB) *signin.VerificationMail {
	tb.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	must.SliceNotEmpty(tb, m.sent)

	return m.sent[len(m.sent)-1]
}

// newResendEnv is the package's env with a verification mailer wired in.
func newResendEnv(t *testing.T, opts ...signin.ServiceOption) (*env, *verificationMailbox) {
	t.Helper()

	mailbox := &verificationMailbox{}

	return newEnv(t, append([]signin.ServiceOption{signin.WithVerificationMailer(mailbox)}, opts...)...), mailbox
}

// The property the door exists for: somebody whose link never arrived gets
// another, and it is the only one that works afterwards.
func TestService_RequestVerificationEmail(T *testing.T) {
	T.Parallel()

	e, mailbox := newResendEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)

	first := registered.EmailAddressVerificationToken

	must.NoError(T, e.svc.RequestVerificationEmail(T.Context(), testScope, registered.User.ID))

	must.EqOp(T, 1, mailbox.count())
	mail := mailbox.last(T)

	test.NotEq(T, "", mail.Token)
	test.NotEqOp(T, first, mail.Token)
	must.NotNil(T, mail.User)
	test.EqOp(T, registered.User.ID, mail.User.ID)
	test.EqOp(T, "ada@example.com", mail.User.EmailAddress)
	test.EqOp(T, "", mail.User.HashedPassword, test.Sprint("the mail's user was not redacted"))

	// The deadline is the registration's window, from this package's clock.
	test.True(T, mail.ExpiresAt.After(time.Now().Add(signin.DefaultVerificationLinkTTL-time.Hour)))
	test.True(T, mail.ExpiresAt.Before(time.Now().Add(signin.DefaultVerificationLinkTTL+time.Hour)))

	// The link mailed before is retired, and reads as every dead link does.
	err = e.svc.VerifyEmailAddress(T.Context(), testScope, first)
	test.ErrorIs(T, err, signin.ErrInvalidVerificationToken)

	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, mail.Token))

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.True(T, stored.EmailAddressVerified())
	test.EqOp(T, identity.StatusGood, stored.AccountStatus)
}

// A resend never un-proves anybody: a proven address is refused, nothing is
// mailed, and the proof is exactly as it was.
func TestService_RequestVerificationEmail_alreadyVerified(T *testing.T) {
	T.Parallel()

	e, mailbox := newResendEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)
	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	proven, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	must.NotNil(T, proven.EmailAddressVerifiedAt)

	err = e.svc.RequestVerificationEmail(T.Context(), testScope, registered.User.ID)
	test.ErrorIs(T, err, signin.ErrEmailAddressAlreadyVerified)

	test.EqOp(T, 0, mailbox.count())

	kept, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	must.NotNil(T, kept.EmailAddressVerifiedAt)
	test.EqOp(T, *proven.EmailAddressVerifiedAt, *kept.EmailAddressVerifiedAt)
	test.EqOp(T, "", kept.EmailAddressVerificationTokenDigest)
}

// An address change withdraws the proof, and this is the door that mails the new
// address its link.
func TestService_RequestVerificationEmail_afterAnAddressChange(T *testing.T) {
	T.Parallel()

	e, mailbox := newResendEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)
	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	proven, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)

	moved := e.changeEmailAddress(T, proven, "ada@elsewhere.example")
	must.False(T, moved.EmailAddressVerified())

	must.NoError(T, e.svc.RequestVerificationEmail(T.Context(), testScope, registered.User.ID))

	mail := mailbox.last(T)
	test.EqOp(T, "ada@elsewhere.example", mail.User.EmailAddress)

	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, mail.Token))

	reproven, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.True(T, reproven.EmailAddressVerified())
}

func TestService_RequestVerificationEmail_refusals(T *testing.T) {
	T.Parallel()

	T.Run("names nobody", func(t *testing.T) {
		t.Parallel()

		e, mailbox := newResendEnv(t)

		test.ErrorIs(t, e.svc.RequestVerificationEmail(t.Context(), testScope, ""), signin.ErrEmptyUserID)
		test.ErrorIs(t, e.svc.RequestVerificationEmail(t.Context(), testScope, "nobody"), identity.ErrUserNotFound)
		test.EqOp(t, 0, mailbox.count())
	})

	T.Run("without a mailer", func(t *testing.T) {
		t.Parallel()

		// A nil mailer is ignored rather than installed, so the service is the
		// one with no mailer rather than one holding a nil seam.
		e := newEnv(t, signin.WithVerificationMailer(nil))

		registered, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.NoPassword()))
		must.NoError(t, err)

		err = e.svc.RequestVerificationEmail(t.Context(), testScope, registered.User.ID)
		test.ErrorIs(t, err, signin.ErrVerificationMailerNotConfigured)

		// And nothing was minted: the link the registrant holds still works.
		must.NoError(t, e.svc.VerifyEmailAddress(t.Context(), testScope, registered.EmailAddressVerificationToken))
	})

	T.Run("without a verifications directory", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
			signin.WithVerificationMailer(&verificationMailbox{}),
		)
		must.NoError(t, err)

		err = svc.RequestVerificationEmail(t.Context(), testScope, e.user.ID)
		test.ErrorIs(t, err, signin.ErrVerificationsNotConfigured)
	})
}

// A mail that fails to send is reported, and the link it carried is committed
// all the same — so the one before it is retired and asking again is the remedy.
func TestService_RequestVerificationEmail_mailerFails(T *testing.T) {
	T.Parallel()

	e, mailbox := newResendEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	must.NoError(T, err)

	errUndeliverable := platformerrors.New("mail server said no")
	mailbox.err = errUndeliverable

	err = e.svc.RequestVerificationEmail(T.Context(), testScope, registered.User.ID)
	test.ErrorIs(T, err, errUndeliverable)

	test.ErrorIs(T,
		e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken),
		signin.ErrInvalidVerificationToken,
	)

	mailbox.err = nil

	must.NoError(T, e.svc.RequestVerificationEmail(T.Context(), testScope, registered.User.ID))
	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, mailbox.last(T).Token))
}

// provenInTheGapVerifications stands in for a proof landing between the read
// RequestVerificationEmail makes and the write it makes: the store's own
// predicate refuses, and the refusal it answers with is identity's.
type provenInTheGapVerifications struct {
	*identity.SQLStore
}

var _ signin.Verifications = (*provenInTheGapVerifications)(nil)

func (provenInTheGapVerifications) SetUserEmailAddressVerificationToken(
	context.Context, database.Tx, tenancy.Scope, string, string, time.Time,
) error {
	return identity.ErrEmailAddressAlreadyVerified
}

// The store's refusal reaches the caller as this package's, so one refusal is
// one answer whichever of the two checks made it.
func TestService_RequestVerificationEmail_storeRefusalIsTranslated(T *testing.T) {
	T.Parallel()

	mailbox := &verificationMailbox{}
	e := newEnv(T)

	svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
		signin.WithVerifications(&provenInTheGapVerifications{SQLStore: e.store}),
		signin.WithVerificationMailer(mailbox),
	)
	must.NoError(T, err)

	unverified := e.registerUnverified(T, "ada")

	err = svc.RequestVerificationEmail(T.Context(), testScope, unverified.ID)
	test.ErrorIs(T, err, signin.ErrEmailAddressAlreadyVerified)
	test.EqOp(T, 0, mailbox.count())
}

// The token is the whole authority of the mail it travels in, so a mail that
// is serialized — queued, logged, handed to an outbox — never carries it.
func TestVerificationMail_neverSerializesTheToken(T *testing.T) {
	T.Parallel()

	encoded, err := json.Marshal(&signin.VerificationMail{Token: "the-secret", User: &identity.User{ID: "u"}})
	must.NoError(T, err)

	test.StrNotContains(T, string(encoded), "the-secret")
}
