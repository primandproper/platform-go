package signin_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// reminderMailbox is the HandleReminderMailer these tests wire in: it keeps what
// it was handed, and can be told to fail.
type reminderMailbox struct {
	err  error
	sent []*signin.HandleReminderMail
	mu   sync.Mutex
}

var _ signin.HandleReminderMailer = (*reminderMailbox)(nil)

func (m *reminderMailbox) SendHandleReminder(_ context.Context, mail *signin.HandleReminderMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return m.err
	}

	m.sent = append(m.sent, mail)

	return nil
}

func (m *reminderMailbox) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sent)
}

func (m *reminderMailbox) last(tb testing.TB) *signin.HandleReminderMail {
	tb.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	must.SliceNotEmpty(tb, m.sent)

	return m.sent[len(m.sent)-1]
}

// newHandleReminderEnv is newEnv with the handle reminder door wired in. The
// floor goes in front of the caller's options, for newMagicLinkEnv's reason: a
// test that wants a real one says so and wins.
func newHandleReminderEnv(t *testing.T, opts ...signin.ServiceOption) (*env, *reminderMailbox) {
	t.Helper()

	mailbox := &reminderMailbox{}

	e := newEnv(t, append([]signin.ServiceOption{
		signin.WithHandleReminderFloor(time.Nanosecond),
		signin.WithHandleReminderMailer(mailbox),
	}, opts...)...)

	return e, mailbox
}

// TestRequestHandleReminder_mailsTheHandle is the happy path: the person who
// holds the address is mailed, and what they are mailed names the handle the
// password door takes.
func TestRequestHandleReminder_mailsTheHandle(t *testing.T) {
	t.Parallel()

	e, mailbox := newHandleReminderEnv(t)

	must.NoError(t, e.svc.RequestHandleReminder(t.Context(), testScope, e.user.EmailAddress))

	must.EqOp(t, 1, mailbox.count())

	mail := mailbox.last(t)

	test.EqOp(t, e.user.ID, mail.User.ID)
	test.EqOp(t, "jane", mail.User.Username)
	test.EqOp(t, e.user.EmailAddress, mail.User.EmailAddress)

	// Redacted, so a template rendered from it cannot put a hash in an email.
	test.EqOp(t, "", mail.User.HashedPassword)
	test.EqOp(t, "", mail.User.TwoFactorSecret)
}

// TestRequestHandleReminder_foldsTheAddressLikeTheDirectory pins that this door
// reads the row the password door would have read.
func TestRequestHandleReminder_foldsTheAddressLikeTheDirectory(t *testing.T) {
	t.Parallel()

	e, mailbox := newHandleReminderEnv(t)

	must.NoError(t, e.svc.RequestHandleReminder(t.Context(), testScope, "JANE@EXAMPLE.COM"))

	must.EqOp(t, 1, mailbox.count())
	test.EqOp(t, e.user.ID, mailbox.last(t).User.ID)
}

// TestRequestHandleReminder_answersTheSameWayForEverybody is the enumeration
// defense: every address that gets no mail gets the answer the one that does
// gets.
func TestRequestHandleReminder_answersTheSameWayForEverybody(T *testing.T) {
	T.Parallel()

	T.Run("an address nobody holds", func(t *testing.T) {
		t.Parallel()

		e, mailbox := newHandleReminderEnv(t)

		test.NoError(t, e.svc.RequestHandleReminder(t.Context(), testScope, "nobody@example.com"))
		test.EqOp(t, 0, mailbox.count())
	})

	for _, status := range []identity.AccountStatus{
		identity.StatusBanned,
		identity.StatusTerminated,
	} {
		T.Run("a "+string(status)+" owner", func(t *testing.T) {
			t.Parallel()

			e, mailbox := newHandleReminderEnv(t)
			e.setStatus(t, status, "for cause")

			test.NoError(t, e.svc.RequestHandleReminder(t.Context(), testScope, e.user.EmailAddress))
			test.EqOp(t, 0, mailbox.count())
		})
	}

	T.Run("an address in another directory", func(t *testing.T) {
		t.Parallel()

		e, mailbox := newHandleReminderEnv(t)

		test.NoError(t, e.svc.RequestHandleReminder(t.Context(), tenancy.Of("tenant_b"), e.user.EmailAddress))
		test.EqOp(t, 0, mailbox.count())
	})
}

// TestRequestHandleReminder_mailsAnUnverifiedRegistrant pins that this door
// reads standing the way the sign-in link door does.
func TestRequestHandleReminder_mailsAnUnverifiedRegistrant(t *testing.T) {
	t.Parallel()

	e, mailbox := newHandleReminderEnv(t)
	registrant := e.registerUnverified(t, "newcomer")

	must.EqOp(t, identity.StatusUnverified, registrant.AccountStatus)

	must.NoError(t, e.svc.RequestHandleReminder(t.Context(), testScope, registrant.EmailAddress))

	must.EqOp(t, 1, mailbox.count())
	test.EqOp(t, "newcomer", mailbox.last(t).User.Username)
}

// TestRequestHandleReminder_reportsItsOwnFailures pins that a mailer that will
// not send is this service failing, and is not answered with the silence an
// unknown address gets.
func TestRequestHandleReminder_reportsItsOwnFailures(t *testing.T) {
	t.Parallel()

	e, mailbox := newHandleReminderEnv(t)
	mailbox.err = platformerrors.New("the mail server is down")

	err := e.svc.RequestHandleReminder(t.Context(), testScope, e.user.EmailAddress)

	test.Error(t, err)
	test.StrContains(t, err.Error(), "mail server is down")
}

// TestRequestHandleReminder_refusals pins the argument it will not take and the
// wiring failure it reports rather than answering silently.
func TestRequestHandleReminder_refusals(T *testing.T) {
	T.Parallel()

	T.Run("an empty address", func(t *testing.T) {
		t.Parallel()

		e, _ := newHandleReminderEnv(t)

		test.ErrorIs(t, e.svc.RequestHandleReminder(t.Context(), testScope, ""), signin.ErrEmptyHandle)
	})

	T.Run("a service with no mailer", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		test.ErrorIs(t,
			e.svc.RequestHandleReminder(t.Context(), testScope, e.user.EmailAddress),
			signin.ErrHandleRemindersNotConfigured)
	})

	T.Run("a nil mailer is ignored", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer, []string{"owner"},
			signin.WithHandleReminderMailer(nil),
			signin.WithHandleReminderFloor(-time.Second),
		)
		must.NoError(t, err)

		test.ErrorIs(t,
			svc.RequestHandleReminder(t.Context(), testScope, e.user.EmailAddress),
			signin.ErrHandleRemindersNotConfigured)
	})
}

// TestRequestHandleReminder_padsItsOwnTiming is the timing half of the
// enumeration defense: the address that is mailed and the one nobody holds are
// held to the same floor.
func TestRequestHandleReminder_padsItsOwnTiming(T *testing.T) {
	T.Parallel()

	const floor = 150 * time.Millisecond

	for name, address := range map[string]string{
		"an address somebody holds": "jane@example.com",
		"an address nobody holds":   "nobody@example.com",
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			e, _ := newHandleReminderEnv(t, signin.WithHandleReminderFloor(floor))

			started := time.Now()
			must.NoError(t, e.svc.RequestHandleReminder(t.Context(), testScope, address))

			test.True(t, time.Since(started) >= floor,
				test.Sprintf("answered in %v, floor is %v", time.Since(started), floor))
		})
	}
}

// TestHandleReminderFloor_isItsOwn pins the ruling that the two anonymous mail
// doors are tuned apart: a magic-link floor does not hold the reminder door.
func TestHandleReminderFloor_isItsOwn(t *testing.T) {
	t.Parallel()

	e, _ := newHandleReminderEnv(t, signin.WithMagicLinkRequestFloor(time.Hour))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	must.NoError(t, e.svc.RequestHandleReminder(ctx, testScope, e.user.EmailAddress))
	must.NoError(t, ctx.Err(), must.Sprint("the reminder door waited on the sign-in link door's floor"))
}
