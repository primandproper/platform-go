package grpc_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/links"
	linksdatabase "github.com/primandproper/platform-go/v14/links/database"
	linksmigrations "github.com/primandproper/platform-go/v14/links/database/migrations"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// confirmationActions are the two actions a confirming deployment declares on
// its minter.
func confirmationActions() links.Option {
	return links.WithActions(map[links.Action]links.ActionPolicy{
		waitlistsgrpc.ConfirmAction: {
			URL: "https://example.com/waitlist/confirm/{token}",
			TTL: links.Duration(72 * time.Hour),
		},
		waitlistsgrpc.UnsubscribeAction: {
			URL: "https://example.com/waitlist/unsubscribe/{token}",
			TTL: links.Duration(365 * 24 * time.Hour),
		},
	})
}

// mailbox is the consumer's ConfirmationMailer as a test sees it: it keeps what
// it was handed, and fails when told to.
type mailbox struct {
	err   error
	mails []*waitlistsgrpc.ConfirmationMail
	mu    sync.Mutex
}

var _ waitlistsgrpc.ConfirmationMailer = (*mailbox)(nil)

func (m *mailbox) SendConfirmation(_ context.Context, _ tenancy.Scope, mail *waitlistsgrpc.ConfirmationMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return m.err
	}

	m.mails = append(m.mails, mail)

	return nil
}

func (m *mailbox) failWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.err = err
}

func (m *mailbox) sent() []*waitlistsgrpc.ConfirmationMail {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]*waitlistsgrpc.ConfirmationMail(nil), m.mails...)
}

// last is the most recent mail, failing if there is none.
func (m *mailbox) last(tb testing.TB) *waitlistsgrpc.ConfirmationMail {
	tb.Helper()

	sent := m.sent()
	must.SliceNotEmpty(tb, sent, must.Sprint("no confirmation was mailed"))

	return sent[len(sent)-1]
}

// confirming is a harness whose server runs the confirmation loop, over a real
// minter on the same database.
type confirming struct {
	*harness

	minter *links.Minter
	mail   *mailbox
}

// newLinksMinter migrates a links table beside the waitlist tables and builds a
// minter over it.
func newLinksMinter(tb testing.TB, db database.Client, opts ...links.Option) *links.Minter {
	tb.Helper()

	prefix := fmt.Sprintf("wlgl_%d", prefixCounter.Add(1))

	stmts, err := linksmigrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := linksdatabase.New(&linksdatabase.Config{TablePrefix: prefix}, db)
	must.NoError(tb, err)

	minter, err := links.NewMinter(store, opts...)
	must.NoError(tb, err)

	return minter
}

func newConfirmingHarness(tb testing.TB) *confirming {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "waitlists.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	minter := newLinksMinter(tb, db, confirmationActions())
	mail := &mailbox{}

	h := newHarnessOn(tb, db, dialect.SQLite, refuseEveryWithdrawal(),
		waitlistsScopeResolver(), waitlistsgrpc.WithConfirmation(minter, mail))

	return &confirming{harness: h, minter: minter, mail: mail}
}

// refuseEveryWithdrawal is an authorizer that permits nothing, so a withdrawal
// that lands in these tests landed on the strength of a link and nothing else.
func refuseEveryWithdrawal() waitlistsgrpc.SignupAuthorizer {
	return waitlistsgrpc.SignupAuthorizerFuncs{}
}

// signupFor reads the signup for contact through the store, whatever its status.
func (c *confirming) signupFor(tb testing.TB, listID, contact string) *waitlists.Signup {
	tb.Helper()

	signup, err := c.store.GetSignupByContact(tb.Context(), c.db.Reader(), testScope, listID, contact)
	must.NoError(tb, err)

	return signup
}

func (c *confirming) join(tb testing.TB, ctx context.Context, listID, contact string) {
	tb.Helper()

	res, err := c.server.Join(ctx, &waitlistspb.JoinRequest{ListId: listID, Contact: contact})
	must.NoError(tb, err)
	must.NotNil(tb, res)
}

func TestNewServer_WithConfirmation(T *testing.T) {
	T.Parallel()

	build := func(t *testing.T, opt waitlistsgrpc.Option) error {
		t.Helper()

		db, err := sqlite.NewDatabaseClient(t.Context(),
			&testClientConfig{connectionString: filepath.Join(t.TempDir(), "waitlists.db")})
		must.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

		store, err := waitlists.NewSQLStore(db)
		must.NoError(t, err)

		_, err = waitlistsgrpc.NewServer(store, db, extractPrincipal, permitWithdrawals(), opt)

		return err
	}

	T.Run("a nil minter is refused rather than quietly not confirming", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, build(t, waitlistsgrpc.WithConfirmation(nil, &mailbox{})), waitlistsgrpc.ErrNilLinks)
	})

	T.Run("a nil mailer is refused, since nothing could ever be confirmed", func(t *testing.T) {
		t.Parallel()

		db, err := sqlite.NewDatabaseClient(t.Context(),
			&testClientConfig{connectionString: filepath.Join(t.TempDir(), "links.db")})
		must.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

		minter := newLinksMinter(t, db, confirmationActions())

		test.ErrorIs(t, build(t, waitlistsgrpc.WithConfirmation(minter, nil)),
			waitlistsgrpc.ErrNilConfirmationMailer)
	})

	T.Run("a minter missing one of the two actions is refused at construction", func(t *testing.T) {
		t.Parallel()

		db, err := sqlite.NewDatabaseClient(t.Context(),
			&testClientConfig{connectionString: filepath.Join(t.TempDir(), "links.db")})
		must.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })

		minter := newLinksMinter(t, db, links.WithAction(waitlistsgrpc.ConfirmAction, links.ActionPolicy{
			URL: "https://example.com/waitlist/confirm/{token}",
			TTL: links.Duration(time.Hour),
		}))

		err = build(t, waitlistsgrpc.WithConfirmation(minter, &mailbox{}))
		test.ErrorIs(t, err, waitlistsgrpc.ErrConfirmationActionMissing)
		test.StrContains(t, err.Error(), string(waitlistsgrpc.UnsubscribeAction))
	})
}

func TestServer_ConfirmationLoop(T *testing.T) {
	T.Parallel()

	T.Run("a join is held pending, mailed, and waits once its link is followed", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.join(t, c.anonCtx(t), list.ID, "Ada@example.com")

		pending := c.signupFor(t, list.ID, "ada@example.com")
		test.EqOp(t, waitlists.StatusPending, pending.Status)

		mail := c.mail.last(t)
		test.EqOp(t, pending.ID, mail.Signup.ID)
		test.EqOp(t, "Ada@example.com", mail.Signup.Contact, test.Sprint("the mail was not addressed as typed"))
		test.EqOp(t, waitlistsgrpc.ConfirmAction, mail.Confirm.Action)
		test.EqOp(t, waitlistsgrpc.UnsubscribeAction, mail.Unsubscribe.Action)
		test.StrHasPrefix(t, "https://example.com/waitlist/confirm/", mail.Confirm.URL)

		// Nobody said yes, so nobody may be let in.
		_, err := c.server.Invite(c.ctx(t), &waitlistspb.InviteRequest{ListId: list.ID, SignupId: pending.ID})
		test.EqOp(t, codes.FailedPrecondition, status.Code(err))

		_, err = c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Confirm.Token)})
		must.NoError(t, err)

		confirmed := c.signupFor(t, list.ID, "ada@example.com")
		test.EqOp(t, waitlists.StatusWaiting, confirmed.Status)
		test.NotNil(t, confirmed.StatusChangedAt)

		// The link followed twice — by the person and by whatever fetched it
		// first — confirms once, and the second click is the one refusal.
		_, err = c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Confirm.Token)})
		test.EqOp(t, codes.NotFound, status.Code(err))
		test.ErrorIs(t, err, waitlistsgrpc.ErrInvalidLink)
	})

	T.Run("a second join mails a pending address again and nothing else", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.join(t, c.anonCtx(t), list.ID, "pending@example.com")
		c.join(t, c.anonCtx(t), list.ID, "PENDING@example.com")

		sent := c.mail.sent()
		must.SliceLen(t, 2, sent, must.Sprint("a pending address was not mailed again"))
		test.EqOp(t, sent[0].Signup.ID, sent[1].Signup.ID, test.Sprint("the resend was for a second signup"))
		test.NotEqOp(t, sent[0].Confirm.ID, sent[1].Confirm.ID, test.Sprint("the resend reused a link"))

		// Both links work until one is spent: the first message was not lost,
		// only late.
		_, err := c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(sent[0].Confirm.Token)})
		must.NoError(t, err)

		// Confirmed now, so a third join mails nothing.
		c.join(t, c.anonCtx(t), list.ID, "pending@example.com")
		test.SliceLen(t, 2, c.mail.sent(), test.Sprint("a confirmed address was mailed again"))

		// And one that withdrew is left alone.
		_, err = c.server.Unsubscribe(c.anonCtx(t), &waitlistspb.UnsubscribeRequest{
			Token: string(sent[1].Unsubscribe.Token),
		})
		must.NoError(t, err)

		c.join(t, c.anonCtx(t), list.ID, "pending@example.com")
		test.SliceLen(t, 2, c.mail.sent(), test.Sprint("a withdrawn address was mailed"))
	})

	T.Run("an unsubscribe link withdraws with no authorizer's leave, a pending signup included", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.join(t, c.anonCtx(t), list.ID, "notme@example.com")
		mail := c.mail.last(t)

		// The authorizer refuses everything, so Withdraw by identifier is
		// refused — the positive control for the link below.
		_, err := c.server.Withdraw(c.anonCtx(t),
			&waitlistspb.WithdrawRequest{ListId: list.ID, SignupId: mail.Signup.ID})
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = c.server.Unsubscribe(c.anonCtx(t), &waitlistspb.UnsubscribeRequest{Token: string(mail.Unsubscribe.Token)})
		must.NoError(t, err)

		left, err := c.store.GetSignup(t.Context(), c.db.Reader(), testScope, list.ID, mail.Signup.ID)
		must.NoError(t, err)
		test.EqOp(t, waitlists.StatusWithdrawn, left.Status)
		test.EqOp(t, "", left.Contact)

		// The confirmation link for the same signup now has nowhere to take it.
		_, err = c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Confirm.Token)})
		test.EqOp(t, codes.NotFound, status.Code(err))
	})

	T.Run("an unsubscribe link for somebody already off the list answers as done", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.join(t, withPrincipal(t.Context(), &testPrincipal{userID: testUser, scope: testScope}),
			list.ID, "erased@example.com")
		mail := c.mail.last(t)

		must.NoError(t, c.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := c.store.WithdrawSignupsForSubject(t.Context(), tx, testScope,
				waitlists.Subject{Type: waitlists.SubjectUser, ID: testUser})

			return err
		}))

		_, err := c.server.Unsubscribe(c.anonCtx(t), &waitlistspb.UnsubscribeRequest{Token: string(mail.Unsubscribe.Token)})
		test.NoError(t, err)
	})

	T.Run("a link at the wrong door is refused and left unspent", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.join(t, c.anonCtx(t), list.ID, "door@example.com")
		mail := c.mail.last(t)

		_, err := c.server.Unsubscribe(c.anonCtx(t), &waitlistspb.UnsubscribeRequest{Token: string(mail.Confirm.Token)})
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Unsubscribe.Token)})
		test.EqOp(t, codes.NotFound, status.Code(err))

		test.EqOp(t, waitlists.StatusPending, c.signupFor(t, list.ID, "door@example.com").Status,
			test.Sprint("a confirmation link presented at Unsubscribe withdrew somebody"))

		// Both still work at their own doors.
		_, err = c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Confirm.Token)})
		must.NoError(t, err)

		_, err = c.server.Unsubscribe(c.anonCtx(t), &waitlistspb.UnsubscribeRequest{Token: string(mail.Unsubscribe.Token)})
		must.NoError(t, err)
	})

	T.Run("a link on a connection placed in another tenant is refused and left unspent", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.join(t, c.anonCtx(t), list.ID, "tenant@example.com")
		mail := c.mail.last(t)

		_, err := c.server.Confirm(c.otherCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Confirm.Token)})
		test.EqOp(t, codes.NotFound, status.Code(err))

		_, err = c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Confirm.Token)})
		must.NoError(t, err, must.Sprint("the refusal in the wrong tenant spent the link"))
	})

	T.Run("every refusal reads the same", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.join(t, c.anonCtx(t), list.ID, "same@example.com")
		mail := c.mail.last(t)

		_, err := c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Confirm.Token)})
		must.NoError(t, err)

		_, spent := c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Confirm.Token)})
		_, madeUp := c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: "bm90IGEgcmVhbCB0b2tlbg"})
		_, empty := c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{})
		_, wrongDoor := c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(mail.Unsubscribe.Token)})

		for name, refused := range map[string]error{"made up": madeUp, "empty": empty, "wrong door": wrongDoor} {
			test.EqOp(t, status.Code(spent), status.Code(refused), test.Sprintf("%s: a different code", name))
			test.EqOp(t, status.Convert(spent).Message(), status.Convert(refused).Message(),
				test.Sprintf("%s: different words", name))
		}
	})

	T.Run("a mailer that fails is the service's failure, and the next join recovers", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.mail.failWith(errors.New("the mail provider is down"))

		_, err := c.server.Join(c.anonCtx(t), &waitlistspb.JoinRequest{ListId: list.ID, Contact: "later@example.com"})
		test.EqOp(t, codes.Internal, status.Code(err))

		// The signup committed before the send, and is waiting on a message.
		test.EqOp(t, waitlists.StatusPending, c.signupFor(t, list.ID, "later@example.com").Status)

		c.mail.failWith(nil)
		c.join(t, c.anonCtx(t), list.ID, "later@example.com")

		_, err = c.server.Confirm(c.anonCtx(t), &waitlistspb.ConfirmRequest{Token: string(c.mail.last(t).Confirm.Token)})
		must.NoError(t, err)
	})

	T.Run("a later message's unsubscribe link lands on the same door", func(t *testing.T) {
		t.Parallel()

		c := newConfirmingHarness(t)
		list := c.seedOpenList(t, testScope)

		c.join(t, c.anonCtx(t), list.ID, "invited@example.com")
		signup := c.mail.last(t).Signup

		link, err := waitlistsgrpc.MintUnsubscribeLink(t.Context(), c.minter, testScope, list.ID, signup.ID)
		must.NoError(t, err)
		test.StrHasPrefix(t, "https://example.com/waitlist/unsubscribe/", link.URL)

		_, err = c.server.Unsubscribe(c.anonCtx(t), &waitlistspb.UnsubscribeRequest{Token: string(link.Token)})
		must.NoError(t, err)

		test.EqOp(t, waitlists.StatusWithdrawn, c.signupFor(t, list.ID, "invited@example.com").Status)

		_, err = waitlistsgrpc.MintUnsubscribeLink(t.Context(), nil, testScope, list.ID, signup.ID)
		test.ErrorIs(t, err, waitlistsgrpc.ErrNilLinks)
	})

	T.Run("a server that mints no links has no such links to redeem", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		list := h.seedOpenList(t, testScope)

		_, err := h.server.Join(h.anonCtx(t), &waitlistspb.JoinRequest{ListId: list.ID, Contact: "plain@example.com"})
		must.NoError(t, err)

		signup, err := h.store.GetSignupByContact(t.Context(), h.db.Reader(), testScope, list.ID, "plain@example.com")
		must.NoError(t, err)
		test.EqOp(t, waitlists.StatusWaiting, signup.Status, test.Sprint("a server that does not confirm held a signup"))

		_, err = h.server.Confirm(h.anonCtx(t), &waitlistspb.ConfirmRequest{Token: "anything"})
		test.EqOp(t, codes.Unimplemented, status.Code(err))

		_, err = h.server.Unsubscribe(h.anonCtx(t), &waitlistspb.UnsubscribeRequest{Token: "anything"})
		test.EqOp(t, codes.Unimplemented, status.Code(err))
	})
}
